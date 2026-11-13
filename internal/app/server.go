// Package app is the HTTP API that the web UI talks to.
package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
	"github.com/Smrnw/UnbrakeTV/internal/faketv"
	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

// ConfirmPhrase must be typed by the user to remove critical packages.
const ConfirmPhrase = "УДАЛИТЬ"

// maxAPKSize limits "install APK from this computer".
const maxAPKSize = 1 << 30

// hardBlocked can never be removed: the program itself would stop working.
var hardBlocked = map[string]string{
	"android":           "Это ядро Android — без него телевизор не включится.",
	"com.android.shell": "Через этот компонент работает сама программа.",
}

type baseline struct {
	host                   string
	memAvailKB, storUsedKB int64
}

type Server struct {
	Token   string
	DataDir string
	Version string

	key  *rsa.PrivateKey
	http *http.Client

	mu           sync.Mutex
	dev          *tv.Device
	info         *tv.DeviceInfo
	status       string // idle | connecting | waiting_auth | connected | rebooting | error
	statusErr    string
	needsPairing bool
	gen          int // bumped by every connect/disconnect; stale goroutines compare it and give up
	demo         bool
	demoTV       *faketv.TV
	demoAddr     string
	demoHist     []historyEntry
	base         *baseline
	changed      bool   // something was removed or tuned since the baseline was taken
	busy         bool   // an APK is being installed
	progress     string // what the long operation is doing right now
	settings     settings
	update       UpdateInfo
	knowledge    KnowledgeInfo
	quit         chan struct{}

	fileMu      sync.Mutex    // guards the JSON files in the data folder
	demoDir     string        // throw-away data folder of the demo TV
	trialPeriod time.Duration // how long a trial switch-off lasts
}

func New(dataDir, version string) (*Server, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	k, err := adb.LoadOrCreateKey(filepath.Join(dataDir, "adbkey.pem"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		Token: hex.EncodeToString(b), DataDir: dataDir, Version: version, key: k, status: "idle",
		quit: make(chan struct{}), http: &http.Client{Timeout: 10 * time.Minute},
	}
	s.settings = s.loadSettings()
	for _, f := range onStart {
		f(s)
	}
	return s, nil
}

// Quit is closed when the user presses "Выход".
func (s *Server) Quit() <-chan struct{} { return s.quit }

func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", static)
	api := func(path, method string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != s.Token {
				httpErr(w, http.StatusForbidden, "неверный токен")
				return
			}
			if method != "" && r.Method != method || method == "" && r.Method != "GET" && r.Method != "POST" {
				httpErr(w, http.StatusMethodNotAllowed, "неверный метод запроса")
				return
			}
			h(w, r)
		})
	}
	api("/api/state", "GET", s.handleState)
	api("/api/scan", "POST", s.handleScan)
	api("/api/connect", "POST", s.handleConnect)
	api("/api/pair", "POST", s.handlePair)
	api("/api/demo", "POST", s.handleDemo)
	api("/api/disconnect", "POST", s.handleDisconnect)
	api("/api/packages", "GET", s.handlePackages)
	api("/api/stats", "GET", s.handleStats)
	api("/api/remove", "POST", s.handleRemove)
	api("/api/restore", "POST", s.handleRestore)
	api("/api/history", "GET", s.handleHistory)
	api("/api/tweaks", "GET", s.handleTweaks)
	api("/api/tweak", "POST", s.handleTweak)
	api("/api/app", "POST", s.handleApp)
	api("/api/presets", "GET", s.handlePresets)
	api("/api/launchers", "GET", s.handleLaunchers)
	api("/api/launcher/install", "POST", s.handleLauncherInstall)
	api("/api/launcher/home", "POST", s.handleLauncherHome)
	api("/api/launcher/store", "POST", s.handleLauncherStore)
	api("/api/install", "POST", s.handleInstall)
	api("/api/catalog", "POST", s.handleCatalog)
	for _, f := range featureRoutes {
		f(s, api)
	}
	api("/api/update", "POST", s.handleUpdate)
	api("/api/settings", "POST", s.handleSettings)
	api("/api/quit", "POST", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		select {
		case <-s.quit:
		default:
			close(s.quit)
		}
	})
	// The interface is for this computer only: refuse requests that reach us
	// under any other host name (DNS rebinding).
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if h != "127.0.0.1" && h != "localhost" {
			httpErr(w, http.StatusForbidden, "интерфейс доступен только с этого компьютера")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

const lostMsg = "связь с телевизором потеряна — подключитесь заново"

func (s *Server) device() (*tv.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev == nil {
		return nil, errors.New("телевизор не подключён")
	}
	if !s.dev.Conn.Alive() {
		s.dev = nil
		s.status, s.statusErr = "error", lostMsg
		return nil, errors.New(s.statusErr)
	}
	return s.dev, nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev != nil && !s.dev.Conn.Alive() {
		s.dev, s.status, s.statusErr = nil, "error", lostMsg
	}
	m := map[string]any{
		"status": s.status, "error": s.statusErr, "needsPairing": s.needsPairing, "device": s.info,
		"version": s.Version, "lastAddress": s.lastAddress(), "recent": s.recent(),
		"demo": s.demo && s.status != "idle", "progress": s.progress, "busy": s.busy,
		"update": s.update, "updateCheck": s.settings.UpdateCheck,
	}
	for _, f := range stateExtra {
		f(s, m)
	}
	writeJSON(w, m)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var services []adb.Service
	done := make(chan struct{})
	go func() {
		defer close(done)
		dctx, dcancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer dcancel()
		services = adb.Discover(dctx)
	}()
	found := Scan(ctx)
	<-done
	seen := map[string]bool{}
	for _, f := range found {
		seen[f.Address] = true
	}
	pairing := []Found{}
	for _, sv := range services {
		f := Found{Address: sv.Address, Name: "Отладка по Wi-Fi · " + sv.Name, Wireless: true}
		if sv.Pairing {
			pairing = append(pairing, f)
		} else if !seen[sv.Address] {
			found = append(found, f)
		}
	}
	if found == nil {
		found = []Found{}
	}
	writeJSON(w, map[string]any{"found": found, "pairing": pairing})
}

type connectOpts struct {
	addr     string
	demo     bool
	reboot   bool // wait for the TV to come back after a reboot
	wireless bool
}

var errConnecting = errors.New("подключение уже идёт")

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// connect starts connecting in the background; the UI follows /api/state.
func (s *Server) connect(o connectOpts) error {
	s.mu.Lock()
	if s.status == "connecting" || s.status == "waiting_auth" {
		s.mu.Unlock()
		return errConnecting
	}
	if s.dev != nil {
		s.dev.Conn.Close()
	}
	s.dev, s.info = nil, nil
	s.gen++
	g := s.gen
	s.status, s.statusErr, s.needsPairing, s.demo = "connecting", "", false, o.demo
	if o.reboot {
		s.status = "rebooting"
	}
	s.mu.Unlock()
	go s.dial(g, o)
	return nil
}

func (s *Server) dial(g int, o connectOpts) {
	setStatus := func(st string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen != g {
			return false
		}
		s.status = st
		return true
	}
	attempts, wait, authWait := 1, time.Duration(0), 90*time.Second
	if o.reboot {
		attempts, wait, authWait = 48, 20*time.Second, 30*time.Second
		if o.demo {
			wait = 2 * time.Second
		}
	}
	addr := o.addr
	var c *adb.Conn
	var err error
	for i := 0; i < attempts; i++ {
		if wait > 0 {
			time.Sleep(wait)
			wait = 5 * time.Second
		}
		if o.reboot && !setStatus("rebooting") {
			return
		}
		if o.reboot && o.wireless {
			// Wireless debugging picks a new port after every reboot.
			if a := discoverConnect(hostOf(addr)); a != "" {
				addr = a
			}
		}
		c, err = adb.Dial(addr, s.key, authWait, func() { setStatus("waiting_auth") })
		if err == nil || errors.Is(err, adb.ErrNeedsPairing) || errors.Is(err, adb.ErrAuthRejected) {
			break
		}
	}
	var info tv.DeviceInfo
	var d *tv.Device
	if err == nil {
		d = tv.New(c)
		if info, err = d.Info(); err != nil {
			c.Close()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != g {
		if err == nil {
			c.Close()
		}
		return
	}
	if err != nil {
		s.status, s.statusErr, s.needsPairing = "error", err.Error(), errors.Is(err, adb.ErrNeedsPairing)
		if o.reboot && !s.needsPairing && !errors.Is(err, adb.ErrAuthRejected) {
			s.statusErr = "Телевизор не вернулся в сеть после перезагрузки. Проверьте, что он включён, и подключитесь заново."
		}
		return
	}
	if o.demo {
		info.Model += " (демо)"
	}
	s.dev, s.info, s.status = d, &info, "connected"
	if s.base != nil && s.base.host != hostOf(addr) {
		s.base, s.changed = nil, false
	}
	if !o.demo {
		os.WriteFile(filepath.Join(s.DataDir, "last_address.txt"), []byte(addr), 0o600)
		s.addRecent(recentTV{Address: addr, Name: strings.TrimSpace(info.Manufacturer + " " + info.Model), Wireless: info.Wireless})
	}
}

// discoverConnect finds the current wireless-debugging port of host.
func discoverConnect(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, sv := range adb.Discover(ctx) {
		if !sv.Pairing && hostOf(sv.Address) == host {
			return sv.Address
		}
	}
	return ""
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Address string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Address) == "" {
		httpErr(w, 400, "укажите IP-адрес телевизора")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "5555")
	}
	if err := s.connect(connectOpts{addr: addr}); err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

var codeRe = regexp.MustCompile(`^\d{6}$`)

func validPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n > 0 && n < 65536
}

// handlePair registers this computer on a TV with "Отладка по Wi-Fi"
// (Android 11+) using the six-digit code, then connects.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct{ Address, Code, ConnectPort string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(req.Address))
	if err != nil || host == "" || !validPort(port) {
		httpErr(w, 400, "Укажите адрес и порт сопряжения с экрана телевизора, например 192.168.1.50:37123.")
		return
	}
	code := strings.ReplaceAll(strings.TrimSpace(req.Code), " ", "")
	if !codeRe.MatchString(code) {
		httpErr(w, 400, "Код сопряжения — это шесть цифр с экрана телевизора.")
		return
	}
	cport := strings.TrimSpace(req.ConnectPort)
	if cport != "" && !validPort(cport) {
		httpErr(w, 400, "Порт подключения указан неверно.")
		return
	}
	s.mu.Lock()
	busy := s.status == "connecting" || s.status == "waiting_auth"
	s.mu.Unlock()
	if busy {
		httpErr(w, 409, errConnecting.Error())
		return
	}
	if err := adb.Pair(net.JoinHostPort(host, port), code, s.key, 30*time.Second); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	addr := ""
	if cport != "" {
		addr = net.JoinHostPort(host, cport)
	} else {
		addr = discoverConnect(host)
	}
	if addr == "" {
		writeJSON(w, map[string]any{"ok": true, "needPort": true})
		return
	}
	if err := s.connect(connectOpts{addr: addr}); err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "address": addr})
}

// handleDemo connects to a built-in emulated TV: every feature can be tried
// without a television, and nothing real is changed.
func (s *Server) handleDemo(w http.ResponseWriter, r *http.Request) {
	addr, err := s.StartDemo()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if err := s.connect(connectOpts{addr: addr, demo: true}); err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// StartDemo launches the emulated TV (once) and returns its address.
func (s *Server) StartDemo() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.demoTV != nil {
		return s.demoAddr, nil
	}
	t, err := faketv.New(faketv.Options{})
	if err != nil {
		return "", err
	}
	if s.demoDir, err = os.MkdirTemp("", "unbrake-demo-"); err != nil {
		return "", err
	}
	addr, err := t.Listen("127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("не удалось запустить демо-телевизор: %w", err)
	}
	s.demoTV, s.demoAddr = t, addr
	return addr, nil
}

// ConnectDemo is used by the -demo command-line flag.
func (s *Server) ConnectDemo() error {
	addr, err := s.StartDemo()
	if err != nil {
		return err
	}
	return s.connect(connectOpts{addr: addr, demo: true})
}

func (s *Server) lastAddress() string {
	b, _ := os.ReadFile(filepath.Join(s.DataDir, "last_address.txt"))
	return strings.TrimSpace(string(b))
}

type recentTV struct {
	Address  string `json:"address"`
	Name     string `json:"name"`
	Wireless bool   `json:"wireless"`
}

func (s *Server) recent() []recentTV {
	list := []recentTV{}
	b, err := os.ReadFile(filepath.Join(s.DataDir, "recent.json"))
	if err == nil {
		json.Unmarshal(b, &list)
	}
	return list
}

func (s *Server) addRecent(t recentTV) {
	list := []recentTV{t}
	for _, x := range s.recent() {
		// One entry per TV: the wireless-debugging port changes all the time.
		if hostOf(x.Address) != hostOf(t.Address) && len(list) < 5 {
			list = append(list, x)
		}
	}
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		os.WriteFile(filepath.Join(s.DataDir, "recent.json"), b, 0o600)
	}
}

func (s *Server) drop() {
	if s.dev != nil {
		s.dev.Conn.Close()
	}
	s.gen++
	s.dev, s.info, s.status, s.statusErr, s.needsPairing = nil, nil, "idle", "", false
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.drop()
	s.base, s.changed = nil, false
	s.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePackages(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	p, err := d.Packages()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	for i := range p {
		if why, ok := hardBlocked[p[i].ID]; ok {
			p[i].Level = tv.Critical
			p[i].Reason = "Удаление запрещено. " + why
		}
	}
	m := map[string]any{"packages": p, "blocked": keys(hardBlocked), "confirmPhrase": ConfirmPhrase}
	for _, f := range packagesExtra {
		f(s, d, m)
	}
	writeJSON(w, m)
}

func keys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	st, err := d.Stats()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.mu.Lock()
	if s.base == nil && s.info != nil && st.MemTotalKB > 0 {
		s.base = &baseline{host: hostOf(s.info.Address), memAvailKB: st.MemAvailKB, storUsedKB: st.StorUsedKB}
	}
	out := struct {
		tv.Stats
		BaseMemAvailKB int64 `json:"baseMemAvailKb"`
		BaseStorUsedKB int64 `json:"baseStorUsedKb"`
		Changed        bool  `json:"changed"`
	}{Stats: st, Changed: s.changed}
	if s.base != nil {
		out.BaseMemAvailKB, out.BaseStorUsedKB = s.base.memAvailKB, s.base.storUsedKB
	}
	s.mu.Unlock()
	writeJSON(w, out)
}

type changeReq struct {
	Packages []string `json:"packages"`
	Confirm  string   `json:"confirm"`  // must equal ConfirmPhrase for critical packages
	Accepted bool     `json:"accepted"` // user acknowledged warnings for caution/unknown
	Method   string   `json:"method"`   // "uninstall" (default), "disable" or "trial"
	Backup   bool     `json:"backup"`   // save a copy of user-installed apps before removing them
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req changeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Packages) == 0 {
		httpErr(w, 400, "не выбрано ни одного приложения")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	// Re-check every package on the server side: the UI is not trusted.
	all, err := d.Packages()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	byID := map[string]tv.Package{}
	for _, p := range all {
		byID[p.ID] = p
	}
	sel := map[string]bool{}
	var critical, warn []string
	for _, id := range req.Packages {
		if why, ok := hardBlocked[id]; ok {
			httpErr(w, 403, id+": удаление запрещено. "+why)
			return
		}
		p, ok := byID[id]
		if !ok || !tv.ValidPackage(id) {
			httpErr(w, 400, "неизвестное приложение: "+id)
			return
		}
		sel[id] = true
		switch p.Level {
		case tv.Critical:
			critical = append(critical, id)
		case tv.Caution, tv.Unknown:
			warn = append(warn, id)
		}
	}
	// Never leave the TV without any home screen.
	launcherLeft := false
	hadLauncher := false
	for _, p := range all {
		if p.IsLauncher && p.State == "installed" {
			hadLauncher = true
			if !sel[p.ID] {
				launcherLeft = true
			}
		}
	}
	if hadLauncher && !launcherLeft && strings.TrimSpace(req.Confirm) != ConfirmPhrase {
		httpErr(w, 409, "Вы удаляете все рабочие столы. Установите другой рабочий стол на вкладке «Установка» или подтвердите удаление фразой "+ConfirmPhrase+".")
		return
	}
	if len(critical) > 0 && strings.TrimSpace(req.Confirm) != ConfirmPhrase {
		httpErr(w, 409, "Для удаления критичных компонентов ("+strings.Join(critical, ", ")+") нужно ввести слово "+ConfirmPhrase+".")
		return
	}
	if len(warn) > 0 && !req.Accepted {
		httpErr(w, 409, "Нужно подтвердить удаление компонентов с предупреждением: "+strings.Join(warn, ", "))
		return
	}
	res := removePackages(s, d, all, req)
	s.logHistory("remove", res)
	writeJSON(w, map[string]any{"results": res})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req changeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Packages) == 0 {
		httpErr(w, 400, "не выбрано ни одного приложения")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	res := restorePackages(s, d, req.Packages)
	s.logHistory("restore", res)
	writeJSON(w, map[string]any{"results": res})
}

type historyEntry struct {
	Time    string      `json:"time"`
	Action  string      `json:"action"`
	Device  string      `json:"device"`
	Results []tv.Result `json:"results"`
}

func (s *Server) logHistory(action string, res []tv.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dev := ""
	if s.info != nil {
		dev = s.info.Manufacturer + " " + s.info.Model + " (" + s.info.Address + ")"
	}
	s.changed = true
	e := historyEntry{Time: time.Now().Format("2006-01-02 15:04:05"), Action: action, Device: dev, Results: res}
	if s.demo {
		// The demo TV is not real: keep its journal in memory only.
		s.demoHist = append(s.demoHist, e)
		if len(s.demoHist) > 100 {
			s.demoHist = s.demoHist[len(s.demoHist)-100:]
		}
		return
	}
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(s.DataDir, "history.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	out := []historyEntry{}
	s.mu.Lock()
	demo := s.demo && s.status != "idle"
	if demo {
		out = append(out, s.demoHist...)
	}
	s.mu.Unlock()
	if !demo {
		b, _ := os.ReadFile(filepath.Join(s.DataDir, "history.jsonl"))
		for _, l := range strings.Split(string(b), "\n") {
			var e historyEntry
			if strings.TrimSpace(l) != "" && json.Unmarshal([]byte(l), &e) == nil {
				if e.Results == nil {
					e.Results = []tv.Result{}
				}
				out = append(out, e)
			}
		}
	}
	// newest first, at most 100
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > 100 {
		out = out[:100]
	}
	writeJSON(w, map[string]any{"history": out})
}

func (s *Server) handleTweaks(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	writeJSON(w, d.Tweaks())
}

func (s *Server) handleTweak(w http.ResponseWriter, r *http.Request) {
	var req struct{ Action string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	msg, err := d.Tweak(req.Action)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.logHistory("tweak", []tv.Result{{ID: req.Action, OK: true, Method: "tweak", Output: msg}})
	resp := map[string]any{"message": msg}
	switch req.Action {
	case "reboot":
		// Come back on our own once the TV has restarted.
		s.mu.Lock()
		o := connectOpts{addr: d.Conn.Addr(), demo: s.demo, reboot: true, wireless: d.Conn.TLS}
		s.status = "idle" // let connect() take over even if a poll marked the link as lost
		s.mu.Unlock()
		if s.connect(o) == nil {
			resp["reconnecting"] = true
		}
	case "adb_off":
		s.mu.Lock()
		s.drop()
		s.mu.Unlock()
	}
	writeJSON(w, resp)
}

// handleApp stops one running app (it starts again when the user opens it).
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	var req struct{ Action, Package string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !tv.ValidPackage(req.Package) {
		httpErr(w, 400, "неверный запрос")
		return
	}
	if act, ok := appActions[req.Action]; ok {
		if _, blocked := hardBlocked[req.Package]; blocked || tv.Lookup(req.Package, false).Level == tv.Critical {
			httpErr(w, 403, "Этот компонент нужен системе — менять его нельзя.")
			return
		}
		d, err := s.device()
		if err != nil {
			httpErr(w, 409, err.Error())
			return
		}
		msg, err := act(s, d, req.Package)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		s.logHistory("tweak", []tv.Result{{ID: req.Package, OK: true, Method: "tweak", Output: msg}})
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if req.Action != "stop" {
		httpErr(w, 400, "неверный запрос")
		return
	}
	if _, ok := hardBlocked[req.Package]; ok || tv.Lookup(req.Package, false).Level == tv.Critical {
		httpErr(w, 403, "Этот компонент нужен системе — останавливать его нельзя.")
		return
	}
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	if err := d.ForceStop(req.Package); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.logHistory("tweak", []tv.Result{{ID: req.Package, OK: true, Method: "tweak", Output: "Остановлено приложение " + req.Package}})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	m := ""
	if s.info != nil {
		m = s.info.Manufacturer
	}
	s.mu.Unlock()
	list, suggested := tv.Presets(m)
	writeJSON(w, map[string]any{"presets": list, "suggested": suggested})
}

// begin marks a long operation; only one may run at a time.
func (s *Server) begin(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy, s.progress = true, text
	return true
}

func (s *Server) setProgress(text string) {
	s.mu.Lock()
	s.progress = text
	s.mu.Unlock()
}

func (s *Server) end() {
	s.mu.Lock()
	s.busy, s.progress = false, ""
	s.mu.Unlock()
}

const busyMsg = "Уже идёт установка — дождитесь её окончания."

// handleInstall installs an APK sent by the browser from this computer.
func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	name, _ := url.QueryUnescape(r.Header.Get("X-Filename"))
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "" {
		name = "приложение.apk"
	}
	if r.ContentLength > maxAPKSize {
		httpErr(w, 413, "Файл слишком большой.")
		return
	}
	body := bufio.NewReader(http.MaxBytesReader(w, r.Body, maxAPKSize))
	if magic, err := body.Peek(4); err != nil || string(magic[:2]) != "PK" {
		httpErr(w, 400, "Это не APK-файл.")
		return
	}
	if !s.begin("Передаю " + name + " на телевизор…") {
		httpErr(w, 409, busyMsg)
		return
	}
	defer s.end()
	total := r.ContentLength
	err = d.InstallAPK(body, func(sent int64) {
		if total > 0 {
			s.setProgress(fmt.Sprintf("Передаю %s на телевизор… %d%%", name, sent*100/total))
		}
		if sent >= total {
			s.setProgress("Телевизор устанавливает приложение…")
		}
	})
	if err != nil {
		s.logHistory("install", []tv.Result{{ID: name, Method: "install", Output: err.Error()}})
		httpErr(w, 500, err.Error())
		return
	}
	s.logHistory("install", []tv.Result{{ID: name, OK: true, Method: "install", Output: "Установлено"}})
	writeJSON(w, map[string]any{"ok": true, "name": name})
}
