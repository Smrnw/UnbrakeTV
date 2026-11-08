// Package app is the HTTP API that the web UI talks to.
package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

// ConfirmPhrase must be typed by the user to remove critical packages.
const ConfirmPhrase = "УДАЛИТЬ"

// hardBlocked can never be removed: the program itself would stop working.
var hardBlocked = map[string]string{
	"android":           "Это ядро Android — без него телевизор не включится.",
	"com.android.shell": "Через этот компонент работает сама программа.",
}

type Server struct {
	Token   string
	DataDir string
	Version string

	key *rsa.PrivateKey

	mu        sync.Mutex
	dev       *tv.Device
	info      *tv.DeviceInfo
	status    string // idle | connecting | waiting_auth | connected | error
	statusErr string
	quit      chan struct{}
}

func New(dataDir, version string) (*Server, error) {
	b := make([]byte, 16)
	rand.Read(b)
	k, err := adb.LoadOrCreateKey(filepath.Join(dataDir, "adbkey.pem"))
	if err != nil {
		return nil, err
	}
	return &Server{Token: hex.EncodeToString(b), DataDir: dataDir, Version: version, key: k, status: "idle", quit: make(chan struct{})}, nil
}

// Quit is closed when the user presses "Выход".
func (s *Server) Quit() <-chan struct{} { return s.quit }

func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", static)
	api := func(path string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != s.Token {
				httpErr(w, http.StatusForbidden, "неверный токен")
				return
			}
			h(w, r)
		})
	}
	api("/api/state", s.handleState)
	api("/api/scan", s.handleScan)
	api("/api/connect", s.handleConnect)
	api("/api/disconnect", s.handleDisconnect)
	api("/api/packages", s.handlePackages)
	api("/api/stats", s.handleStats)
	api("/api/remove", s.handleRemove)
	api("/api/restore", s.handleRestore)
	api("/api/history", s.handleHistory)
	api("/api/tweaks", s.handleTweaks)
	api("/api/tweak", s.handleTweak)
	api("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		select {
		case <-s.quit:
		default:
			close(s.quit)
		}
	})
	return mux
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

func (s *Server) device() (*tv.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev == nil {
		return nil, errors.New("телевизор не подключён")
	}
	if !s.dev.Conn.Alive() {
		s.dev = nil
		s.status, s.statusErr = "error", "связь с телевизором потеряна — подключитесь заново"
		return nil, errors.New(s.statusErr)
	}
	return s.dev, nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev != nil && !s.dev.Conn.Alive() {
		s.dev, s.status, s.statusErr = nil, "error", "связь с телевизором потеряна — подключитесь заново"
	}
	writeJSON(w, map[string]any{
		"status": s.status, "error": s.statusErr, "device": s.info,
		"version": s.Version, "lastAddress": s.lastAddress(),
	})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, map[string]any{"found": Scan(ctx)})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Address string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Address) == "" {
		httpErr(w, 400, "укажите IP-адрес телевизора")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "5555")
	}
	s.mu.Lock()
	if s.status == "connecting" || s.status == "waiting_auth" {
		s.mu.Unlock()
		httpErr(w, 409, "подключение уже идёт")
		return
	}
	if s.dev != nil {
		s.dev.Conn.Close()
		s.dev, s.info = nil, nil
	}
	s.status, s.statusErr = "connecting", ""
	s.mu.Unlock()

	go func() {
		c, err := adb.Dial(addr, s.key, 90*time.Second, func() {
			s.mu.Lock()
			s.status = "waiting_auth"
			s.mu.Unlock()
		})
		if err != nil {
			s.mu.Lock()
			s.status, s.statusErr = "error", err.Error()
			s.mu.Unlock()
			return
		}
		d := tv.New(c)
		info, err := d.Info()
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			c.Close()
			s.status, s.statusErr = "error", err.Error()
			return
		}
		s.dev, s.info, s.status = d, &info, "connected"
		os.WriteFile(filepath.Join(s.DataDir, "last_address.txt"), []byte(addr), 0o600)
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) lastAddress() string {
	b, _ := os.ReadFile(filepath.Join(s.DataDir, "last_address.txt"))
	return strings.TrimSpace(string(b))
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.dev != nil {
		s.dev.Conn.Close()
	}
	s.dev, s.info, s.status, s.statusErr = nil, nil, "idle", ""
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
	writeJSON(w, map[string]any{"packages": p, "blocked": keys(hardBlocked), "confirmPhrase": ConfirmPhrase})
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
	writeJSON(w, st)
}

type changeReq struct {
	Packages []string `json:"packages"`
	Confirm  string   `json:"confirm"`  // must equal ConfirmPhrase for critical packages
	Accepted bool     `json:"accepted"` // user acknowledged warnings for caution/unknown
	Method   string   `json:"method"`   // "uninstall" (default) or "disable"
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
		httpErr(w, 409, "Вы удаляете все рабочие столы. Установите другой лаунчер (например, Projectivy Launcher из Play Маркета) или подтвердите удаление фразой "+ConfirmPhrase+".")
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
	var res []tv.Result
	if req.Method == "disable" {
		res = d.Disable(req.Packages)
	} else {
		res = d.Remove(req.Packages)
	}
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
	res := d.Restore(req.Packages)
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
	dev := ""
	if s.info != nil {
		dev = s.info.Manufacturer + " " + s.info.Model + " (" + s.info.Address + ")"
	}
	s.mu.Unlock()
	e := historyEntry{Time: time.Now().Format("2006-01-02 15:04:05"), Action: action, Device: dev, Results: res}
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(s.DataDir, "history.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	b, _ := os.ReadFile(filepath.Join(s.DataDir, "history.jsonl"))
	var out []historyEntry
	for _, l := range strings.Split(string(b), "\n") {
		var e historyEntry
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
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
	if req.Action == "reboot" || req.Action == "adb_off" {
		s.mu.Lock()
		if s.dev != nil {
			s.dev.Conn.Close()
		}
		s.dev, s.info, s.status, s.statusErr = nil, nil, "idle", ""
		s.mu.Unlock()
	}
	writeJSON(w, map[string]string{"message": msg})
}
