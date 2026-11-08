// Unbrake TV — очистка Android TV от заводского мусора.
//
// Программа поднимает локальный веб-сервер на 127.0.0.1, открывает интерфейс
// в браузере и общается с телевизором по протоколу ADB напрямую (adb.exe не нужен).
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/app"
	"github.com/Smrnw/UnbrakeTV/internal/web"
)

var version = "1.0.0"

func main() {
	port := flag.Int("port", 0, "порт веб-интерфейса (0 — случайный)")
	noBrowser := flag.Bool("no-browser", false, "не открывать браузер автоматически")
	flag.Parse()

	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	dataDir := filepath.Join(dir, "UnbrakeTV")
	os.MkdirAll(dataDir, 0o700)
	migrateFromTVClean(filepath.Join(dir, "TVClean"), dataDir)

	srv, err := app.New(dataDir, version)
	if err != nil {
		log.Fatal(err)
	}
	sub, _ := fs.Sub(web.Files, "static")
	h := srv.Handler(http.FileServer(http.FS(sub)))

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	url := fmt.Sprintf("http://%s/#t=%s", ln.Addr(), srv.Token)

	fmt.Println("==============================================")
	fmt.Println("  Unbrake TV", version, "— очистка Android TV")
	fmt.Println("==============================================")
	fmt.Println("Интерфейс открыт в браузере. Если не открылся, скопируйте адрес:")
	fmt.Println("  ", url)
	fmt.Println()
	fmt.Println("Чтобы выйти — нажмите «Выход» в интерфейсе или закройте это окно.")

	go func() {
		if !*noBrowser {
			time.Sleep(300 * time.Millisecond)
			openBrowser(url)
		}
	}()
	go func() {
		<-srv.Quit()
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
	log.Fatal(http.Serve(ln, h))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// migrateFromTVClean keeps the ADB key and history from the pre-release
// "TV Clean" builds, so an already authorized TV does not ask again.
func migrateFromTVClean(oldDir, newDir string) {
	for _, f := range []string{"adbkey.pem", "history.jsonl", "last_address.txt"} {
		dst := filepath.Join(newDir, f)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(oldDir, f)); err == nil {
			os.WriteFile(dst, b, 0o600)
		}
	}
}
