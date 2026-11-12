package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/faketv"
	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

// Launcher is a lightweight home screen the program can install.
// The APK is downloaded from the developer's own GitHub releases at the
// moment the user asks for it; nothing is bundled with the program.
type Launcher struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Repo        string `json:"repo"`    // GitHub "owner/name" with APK files in releases
	Package     string `json:"package"` // Android package name
	Store       bool   `json:"store"`   // also available in Google Play on the TV
	Site        string `json:"site"`
}

var launcherCatalog = []Launcher{
	{
		ID: "projectivy", Name: "Projectivy Launcher",
		Description: "Быстрый рабочий стол без рекламы: свои обои, категории, скрытие ненужных приложений. Самая популярная замена PatchWall и главного экрана Google TV.",
		Repo:        "spocky/miproja1", Package: "com.spocky.projengmenu", Store: true,
		Site: "https://github.com/spocky/miproja1",
	},
	{
		ID: "flauncher", Name: "FLauncher",
		Description: "Минималистичный рабочий стол с открытым исходным кодом: только ваши приложения и ничего лишнего. Хорош для слабых телевизоров.",
		Repo:        "osrosal/flauncher", Package: "me.efesser.flauncher",
		Site: "https://github.com/osrosal/flauncher",
	},
}

func findLauncher(id string) (Launcher, bool) {
	for _, l := range launcherCatalog {
		if l.ID == id {
			return l, true
		}
	}
	return Launcher{}, false
}

var knownABIs = []string{"arm64-v8a", "armeabi-v7a", "x86_64", "x86", "armeabi"}

// assetABI returns the processor type an APK file name mentions, if any.
func assetABI(name string) string {
	low := strings.ToLower(name)
	for _, a := range knownABIs {
		for _, sep := range []string{"-", "_", "."} {
			if strings.Contains(low, a+sep) {
				return a
			}
		}
	}
	return ""
}

// pickAPK chooses the release file that fits the TV's processor.
func pickAPK(assets []ghAsset, abi string) (ghAsset, error) {
	var universal, exact, compat *ghAsset
	for i := range assets {
		a := &assets[i]
		if !strings.HasSuffix(strings.ToLower(a.Name), ".apk") {
			continue
		}
		switch assetABI(a.Name) {
		case "":
			if universal == nil {
				universal = a
			}
		case abi:
			if exact == nil {
				exact = a
			}
		case "armeabi-v7a":
			// 64-bit ARM televisions also run 32-bit builds.
			if abi == "arm64-v8a" && compat == nil {
				compat = a
			}
		}
	}
	for _, a := range []*ghAsset{exact, universal, compat} {
		if a != nil {
			return *a, nil
		}
	}
	return ghAsset{}, errors.New("в последнем выпуске нет сборки для процессора этого телевизора")
}

const maxLauncherSize = 200 << 20

// downloadAPK fetches a release file from GitHub into memory.
func (s *Server) downloadAPK(ctx context.Context, l Launcher, abi string) ([]byte, string, error) {
	rel, err := s.latestRelease(ctx, l.Repo)
	if err != nil {
		return nil, "", err
	}
	a, err := pickAPK(rel.Assets, abi)
	if err != nil {
		return nil, "", err
	}
	// Only the developer's own release files, only over HTTPS.
	if !strings.HasPrefix(a.URL, "https://github.com/"+l.Repo+"/releases/download/") || a.Size > maxLauncherSize {
		return nil, "", errors.New("GitHub вернул неожиданную ссылку на файл — установка отменена")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "UnbrakeTV/"+s.Version)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", errors.New("не удалось скачать файл с GitHub — проверьте интернет на компьютере")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub ответил ошибкой %d при скачивании", resp.StatusCode)
	}
	var buf bytes.Buffer
	chunk := make([]byte, 256*1024)
	for {
		n, err := resp.Body.Read(chunk)
		buf.Write(chunk[:n])
		if buf.Len() > maxLauncherSize {
			return nil, "", errors.New("файл слишком большой — установка отменена")
		}
		if a.Size > 0 {
			s.setProgress(fmt.Sprintf("Скачиваю %s %s… %d%%", l.Name, rel.Tag, min(100, int64(buf.Len())*100/a.Size)))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", errors.New("скачивание прервалось — попробуйте ещё раз")
		}
	}
	if buf.Len() < 4 || string(buf.Bytes()[:2]) != "PK" {
		return nil, "", errors.New("скачанный файл не похож на APK — установка отменена")
	}
	return buf.Bytes(), rel.Tag, nil
}

func (s *Server) handleLaunchers(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	installed, current := d.Launchers()
	if installed == nil {
		installed = []string{}
	}
	names := map[string]string{}
	for _, p := range installed {
		names[p] = tv.Lookup(p, true).Name
	}
	writeJSON(w, map[string]any{"catalog": launcherCatalog, "installed": installed, "current": current, "names": names})
}

func (s *Server) handleLauncherInstall(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	l, ok := findLauncher(req.ID)
	if !ok {
		httpErr(w, 400, "неизвестный рабочий стол")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	s.mu.Lock()
	demo, abi := s.demo, ""
	if s.info != nil {
		abi = s.info.ABI
	}
	s.mu.Unlock()
	if !s.begin("Ищу последнюю версию " + l.Name + "…") {
		httpErr(w, 409, busyMsg)
		return
	}
	defer s.end()

	var apk []byte
	version := "демо"
	if demo {
		// The demo TV is offline by design: it gets a stand-in file.
		time.Sleep(700 * time.Millisecond)
		apk = []byte(faketv.FakeAPKPrefix + l.Package + ";launcher\n")
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
		defer cancel()
		if apk, version, err = s.downloadAPK(ctx, l, abi); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
	}
	before, _ := d.Launchers()
	total := int64(len(apk))
	err = d.InstallAPK(bytes.NewReader(apk), func(sent int64) {
		s.setProgress(fmt.Sprintf("Передаю %s на телевизор… %d%%", l.Name, sent*100/total))
		if sent >= total {
			s.setProgress("Телевизор устанавливает " + l.Name + "…")
		}
	})
	if err != nil {
		s.logHistory("install", []tv.Result{{ID: l.Name, Method: "install", Output: err.Error()}})
		httpErr(w, 500, err.Error())
		return
	}
	// Learn the package name from the TV itself rather than trusting the catalog.
	after, _ := d.Launchers()
	pkg := ""
	was := map[string]bool{}
	for _, p := range before {
		was[p] = true
	}
	for _, p := range after {
		if !was[p] {
			pkg = p
		}
	}
	if pkg == "" {
		for _, p := range after {
			if p == l.Package {
				pkg = p
			}
		}
	}
	s.logHistory("install", []tv.Result{{ID: l.Name + " " + version, OK: true, Method: "install", Output: "Установлено"}})
	writeJSON(w, map[string]any{"ok": true, "package": pkg, "version": version, "name": l.Name})
}

func (s *Server) handleLauncherHome(w http.ResponseWriter, r *http.Request) {
	var req struct{ Package string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !tv.ValidPackage(req.Package) {
		httpErr(w, 400, "неверный запрос")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	if err := d.SetHome(req.Package); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.logHistory("tweak", []tv.Result{{ID: req.Package, OK: true, Method: "tweak", Output: "Основной рабочий стол: " + req.Package}})
	writeJSON(w, map[string]bool{"ok": true})
}

// handleLauncherStore opens the launcher's Google Play page on the TV screen,
// for those who prefer to install from the store.
func (s *Server) handleLauncherStore(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	l, ok := findLauncher(req.ID)
	if !ok || !l.Store {
		httpErr(w, 400, "неизвестный рабочий стол")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	if err := d.OpenStore(l.Package); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
