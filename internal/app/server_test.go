package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/faketv"
)

type client struct {
	t     *testing.T
	url   string
	token string
}

// do sends a request and decodes the JSON answer; it returns the HTTP status.
func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, c.url+path, rd)
	req.Header.Set("X-Token", c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: bad JSON %q", method, path, raw)
		}
	}
	return resp.StatusCode
}

type state struct {
	Status       string
	Error        string
	NeedsPairing bool
	Demo         bool
	Device       *struct{ Manufacturer, Model, Address string }
	Recent       []recentTV
}

func (c *client) waitStatus(want string) state {
	c.t.Helper()
	var st state
	for i := 0; i < 200; i++ {
		st = state{}
		c.do("GET", "/api/state", nil, &st)
		if st.Status == want {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("status is %q (%s), want %q", st.Status, st.Error, want)
	return st
}

func newServer(t *testing.T) (*Server, *client) {
	t.Helper()
	s, err := New(t.TempDir(), "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s.Handler(http.NotFoundHandler()))
	t.Cleanup(h.Close)
	return s, &client{t: t, url: h.URL, token: s.Token}
}

type pkg struct {
	ID, Level, State                      string
	System, IsLauncher, IsDefaultLauncher bool
}

func (c *client) packages() map[string]pkg {
	c.t.Helper()
	var r struct{ Packages []pkg }
	if code := c.do("GET", "/api/packages", nil, &r); code != 200 {
		c.t.Fatalf("packages: %d", code)
	}
	m := map[string]pkg{}
	for _, p := range r.Packages {
		m[p.ID] = p
	}
	return m
}

func TestAccessControl(t *testing.T) {
	s, c := newServer(t)
	if code := (&client{t: t, url: c.url, token: "wrong"}).do("GET", "/api/state", nil, nil); code != 403 {
		t.Errorf("wrong token: %d", code)
	}
	if code := c.do("GET", "/api/quit", nil, nil); code != 405 {
		t.Errorf("GET on a changing endpoint: %d", code)
	}
	// DNS rebinding: the right token under a foreign host name is still refused.
	req, _ := http.NewRequest("GET", c.url+"/api/state", nil)
	req.Host = "evil.example:80"
	req.Header.Set("X-Token", s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("foreign host: %d", resp.StatusCode)
	}
	if code := c.do("GET", "/api/packages", nil, nil); code != 409 {
		t.Errorf("no TV connected: %d", code)
	}
	for _, bad := range []map[string]string{
		{"address": "192.168.1.5", "code": "123456"},
		{"address": "192.168.1.5:37000", "code": "12345"},
		{"address": "192.168.1.5:37000", "code": "123456", "connectPort": "99999"},
	} {
		if code := c.do("POST", "/api/pair", bad, nil); code != 400 {
			t.Errorf("pair %v: %d", bad, code)
		}
	}
}

func TestDemoSession(t *testing.T) {
	s, c := newServer(t)
	if code := c.do("POST", "/api/demo", map[string]any{}, nil); code != 200 {
		t.Fatalf("demo: %d", code)
	}
	st := c.waitStatus("connected")
	if !st.Demo || st.Device == nil || !strings.Contains(st.Device.Model, "демо") {
		t.Fatalf("state: %+v", st)
	}

	pk := c.packages()
	if len(pk) < 100 || pk["android"].Level != "critical" || pk["com.miui.tv.analytics"].Level != "recommended" {
		t.Fatalf("packages: %d", len(pk))
	}

	// The first reading becomes the baseline of the session.
	if code := c.do("GET", "/api/stats", nil, nil); code != 200 {
		t.Fatalf("stats: %d", code)
	}

	// --- protections, all enforced on the server ---
	var e struct{ Error string }
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"android"}, Confirm: ConfirmPhrase, Accepted: true}, &e); code != 403 {
		t.Errorf("core of Android: %d %s", code, e.Error)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.google.android.gms"}, Accepted: true}, &e); code != 409 || !strings.Contains(e.Error, ConfirmPhrase) {
		t.Errorf("critical without the phrase: %d %s", code, e.Error)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.google.android.katniss"}}, &e); code != 409 {
		t.Errorf("caution without acknowledgement: %d", code)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.mitv.tvhome.atv", "com.google.android.tvlauncher"}, Accepted: true}, &e); code != 409 || !strings.Contains(e.Error, "рабочие столы") {
		t.Errorf("all launchers: %d %s", code, e.Error)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.no.such.app"}, Accepted: true}, nil); code != 400 {
		t.Errorf("unknown package: %d", code)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"x; reboot"}, Accepted: true}, nil); code != 400 {
		t.Errorf("shell injection attempt: %d", code)
	}
	if got := c.packages(); got["com.google.android.gms"].State != "installed" || got["com.mitv.tvhome.atv"].State != "installed" {
		t.Fatal("a refused request changed the TV")
	}

	// --- presets ---
	var pr struct {
		Presets []struct {
			ID             string
			Packages       []string
			AllRecommended bool
		}
		Suggested string
	}
	if code := c.do("GET", "/api/presets", nil, &pr); code != 200 || pr.Suggested != "xiaomi-ads" || len(pr.Presets) < 5 {
		t.Fatalf("presets: %d %+v", code, pr)
	}
	var ads []string
	for _, p := range pr.Presets {
		if p.ID == "xiaomi-ads" {
			for _, id := range p.Packages {
				if pk[id].State == "installed" {
					if pk[id].Level == "critical" {
						t.Errorf("preset contains a critical package: %s", id)
					}
					ads = append(ads, id)
				}
			}
		}
	}
	if len(ads) < 5 {
		t.Fatalf("the Xiaomi preset matched only %d apps of the demo TV", len(ads))
	}

	// --- remove by preset, then restore ---
	var res struct {
		Results []struct {
			ID, Method string
			OK         bool
		}
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: ads, Accepted: true}, &res); code != 200 || len(res.Results) != len(ads) {
		t.Fatalf("remove: %d", code)
	}
	for _, r := range res.Results {
		if !r.OK {
			t.Errorf("not removed: %+v", r)
		}
	}
	if got := c.packages(); got[ads[0]].State != "removed" {
		t.Fatalf("state after removal: %s", got[ads[0]].State)
	}
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.netflix.ninja"}, Method: "disable"}, &res); code != 200 || !res.Results[0].OK || res.Results[0].Method != "disable" {
		t.Fatalf("disable: %d %+v", code, res)
	}
	if got := c.packages(); got["com.netflix.ninja"].State != "disabled" {
		t.Fatal("Netflix must be disabled")
	}

	// The session result: more free memory and storage than at the start.
	var stats struct {
		MemAvailKb, BaseMemAvailKb, StorUsedKb, BaseStorUsedKb int64
		Changed                                                bool
		Top                                                    []struct{ Package string }
	}
	if code := c.do("GET", "/api/stats", nil, &stats); code != 200 || !stats.Changed || stats.BaseStorUsedKb <= stats.StorUsedKb || len(stats.Top) == 0 {
		t.Fatalf("stats: %d %+v", code, stats)
	}

	if code := c.do("POST", "/api/restore", changeReq{Packages: append(ads, "com.netflix.ninja")}, &res); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	for _, r := range res.Results {
		if !r.OK {
			t.Errorf("not restored: %+v", r)
		}
	}
	if got := c.packages(); got[ads[0]].State != "installed" || got["com.netflix.ninja"].State != "installed" {
		t.Fatal("restore did not bring the apps back")
	}

	// --- tweaks and stopping an app ---
	var msg struct{ Message string }
	for _, a := range []string{"anim_fast", "kill_bg", "trim_caches"} {
		if code := c.do("POST", "/api/tweak", map[string]string{"action": a}, &msg); code != 200 || msg.Message == "" {
			t.Errorf("tweak %s: %d", a, code)
		}
	}
	if code := c.do("POST", "/api/tweak", map[string]string{"action": "format_tv"}, nil); code != 500 {
		t.Errorf("unknown tweak: %d", code)
	}
	var tw struct{ AnimScale string }
	if c.do("GET", "/api/tweaks", nil, &tw); tw.AnimScale != "0.5" {
		t.Errorf("animation scale: %q", tw.AnimScale)
	}
	if code := c.do("POST", "/api/app", map[string]string{"action": "stop", "package": "com.netflix.ninja"}, nil); code != 200 {
		t.Errorf("stop app: %d", code)
	}
	if code := c.do("POST", "/api/app", map[string]string{"action": "stop", "package": "com.android.systemui"}, nil); code != 403 {
		t.Errorf("stopping a critical component: %d", code)
	}

	// --- one-click launcher ---
	var ln struct {
		Catalog   []Launcher
		Installed []string
		Current   string
	}
	if code := c.do("GET", "/api/launchers", nil, &ln); code != 200 || len(ln.Catalog) < 2 || len(ln.Installed) != 2 || ln.Current != "com.mitv.tvhome.atv" {
		t.Fatalf("launchers: %d %+v", code, ln)
	}
	var inst struct{ Package, Version string }
	if code := c.do("POST", "/api/launcher/install", map[string]string{"id": "demo-home"}, &inst); code != 200 || inst.Package != "com.example.demohome" {
		t.Fatalf("install launcher: %d %+v", code, inst)
	}
	if code := c.do("POST", "/api/launcher/install", map[string]string{"id": "nope"}, nil); code != 400 {
		t.Errorf("unknown launcher: %d", code)
	}
	if code := c.do("POST", "/api/launcher/home", map[string]string{"package": "com.example.demohome"}, nil); code != 200 {
		t.Fatalf("set home: %d", code)
	}
	if c.do("GET", "/api/launchers", nil, &ln); ln.Current != "com.example.demohome" || len(ln.Installed) != 3 {
		t.Fatalf("after install: %+v", ln)
	}
	if code := c.do("POST", "/api/launcher/store", map[string]string{"id": "demo-home"}, nil); code != 200 {
		t.Errorf("open store: %d", code)
	}
	// With a second home screen in place, PatchWall can go without the phrase.
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.mitv.tvhome.atv"}, Accepted: true}, &res); code != 200 || !res.Results[0].OK {
		t.Fatalf("remove PatchWall after installing a launcher: %d", code)
	}

	// --- APK from the computer ---
	apk := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{1}, 200_000)...)
	if code := c.do("POST", "/api/install", apk, nil); code != 200 {
		t.Fatalf("install APK: %d", code)
	}
	if code := c.do("POST", "/api/install", []byte("<html>not an apk</html>"), &e); code != 400 {
		t.Errorf("not an APK: %d %s", code, e.Error)
	}
	if got := c.packages(); got["com.example.sideloaded"].State != "installed" || got["com.example.sideloaded"].System {
		t.Fatal("the sideloaded app is missing")
	}

	// --- the demo journal stays in memory ---
	var hist struct {
		History []struct {
			Action  string
			Results []struct{ OK bool }
		}
	}
	if code := c.do("GET", "/api/history", nil, &hist); code != 200 || len(hist.History) < 8 || hist.History[0].Action != "install" {
		t.Fatalf("history: %d, %d entries", code, len(hist.History))
	}
	for _, f := range []string{"history.jsonl", "last_address.txt", "recent.json"} {
		if _, err := os.Stat(filepath.Join(s.DataDir, f)); err == nil {
			t.Errorf("demo mode must not write %s", f)
		}
	}

	// --- reboot: the program reconnects on its own ---
	var rb struct{ Reconnecting bool }
	if code := c.do("POST", "/api/tweak", map[string]string{"action": "reboot"}, &rb); code != 200 || !rb.Reconnecting {
		t.Fatalf("reboot: %d %+v", code, rb)
	}
	c.waitStatus("rebooting")
	c.waitStatus("connected")
	if got := c.packages(); got["com.mitv.tvhome.atv"].State != "removed" {
		t.Fatal("the TV state must survive the reboot")
	}

	// --- leaving the demo ---
	if code := c.do("POST", "/api/disconnect", map[string]any{}, nil); code != 200 {
		t.Fatal("disconnect")
	}
	if st := c.waitStatus("idle"); st.Demo || st.Device != nil {
		t.Fatalf("after disconnect: %+v", st)
	}
	if c.do("GET", "/api/history", nil, &hist); len(hist.History) != 0 {
		t.Error("the real journal must be empty after a demo session")
	}
}

// A real (non-demo) connection to the emulator: classic and wireless.
func TestRealConnectionAndPairing(t *testing.T) {
	s, c := newServer(t)

	// Nothing listens here: a clear error, not a hang.
	c.do("POST", "/api/connect", map[string]string{"address": "127.0.0.1:1"}, nil)
	if st := c.waitStatus("error"); st.Error == "" || st.NeedsPairing {
		t.Fatalf("refused connection: %+v", st)
	}

	classic, err := faketv.New(faketv.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer classic.Close()
	addr, _ := classic.Listen("127.0.0.1:0")
	if code := c.do("POST", "/api/connect", map[string]string{"address": addr}, nil); code != 200 {
		t.Fatalf("connect: %d", code)
	}
	st := c.waitStatus("connected")
	if st.Demo || len(st.Recent) != 1 || st.Recent[0].Address != addr || st.Recent[0].Name != "Xiaomi Mi TV A2 43" {
		t.Fatalf("state: %+v", st)
	}
	var res struct{ Results []struct{ OK bool } }
	if code := c.do("POST", "/api/remove", changeReq{Packages: []string{"com.xiaomi.statistic"}}, &res); code != 200 || !res.Results[0].OK {
		t.Fatalf("remove: %d", code)
	}
	if b, err := os.ReadFile(filepath.Join(s.DataDir, "history.jsonl")); err != nil || !strings.Contains(string(b), "com.xiaomi.statistic") {
		t.Fatal("a real session must be written to the journal")
	}
	// The TV drops off the network: the UI is told, nothing hangs.
	classic.Close()
	s.mu.Lock()
	s.dev.Conn.Close()
	s.mu.Unlock()
	if st := c.waitStatus("error"); !strings.Contains(st.Error, "потеряна") {
		t.Fatalf("lost connection: %+v", st)
	}

	// Wireless debugging.
	wl, err := faketv.New(faketv.Options{Wireless: true, PairCode: "135790"})
	if err != nil {
		t.Fatal(err)
	}
	defer wl.Close()
	waddr, _ := wl.Listen("127.0.0.1:0")
	paddr, _ := wl.ListenPairing("127.0.0.1:0")
	_, wport, _ := strings.Cut(waddr, ":")

	c.do("POST", "/api/connect", map[string]string{"address": waddr}, nil)
	if st := c.waitStatus("error"); !st.NeedsPairing {
		t.Fatalf("an unpaired computer must be offered pairing: %+v", st)
	}
	var e struct{ Error string }
	if code := c.do("POST", "/api/pair", map[string]string{"address": paddr, "code": "000000", "connectPort": wport}, &e); code != 502 || !strings.Contains(e.Error, "не подошёл") {
		t.Fatalf("wrong code: %d %s", code, e.Error)
	}
	var pr struct {
		OK      bool
		Address string
	}
	if code := c.do("POST", "/api/pair", map[string]string{"address": paddr, "code": "135 790", "connectPort": wport}, &pr); code != 200 || pr.Address != waddr {
		t.Fatalf("pair: %d %+v", code, pr)
	}
	st = c.waitStatus("connected")
	if st.NeedsPairing || len(st.Recent) != 1 {
		t.Fatalf("after pairing: %+v", st)
	}
	if pk := c.packages(); len(pk) < 100 {
		t.Fatal("packages over TLS")
	}
	// Disconnect while idle-connecting must cancel cleanly.
	c.do("POST", "/api/disconnect", map[string]any{}, nil)
	c.waitStatus("idle")
}

func TestUpdateCheck(t *testing.T) {
	release := `{"tag_name":"v1.2.0","html_url":"https://github.com/Smrnw/UnbrakeTV/releases/tag/v1.2.0","body":"Новое","assets":[]}`
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Smrnw/UnbrakeTV/releases/latest" || !strings.HasPrefix(r.UserAgent(), "UnbrakeTV/") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, release)
	}))
	defer gh.Close()
	old := githubAPI
	githubAPI = gh.URL
	defer func() { githubAPI = old }()

	_, c := newServer(t)
	var u UpdateInfo
	if code := c.do("POST", "/api/update", map[string]any{}, &u); code != 200 || !u.Newer || u.Latest != "1.2.0" || u.Notes != "Новое" || u.Error != "" {
		t.Fatalf("update: %d %+v", code, u)
	}
	var st struct {
		Update      UpdateInfo
		UpdateCheck bool
	}
	if c.do("GET", "/api/state", nil, &st); !st.Update.Newer || !st.UpdateCheck {
		t.Fatalf("state: %+v", st)
	}

	// A link that points away from the project page is not shown to the user.
	release = `{"tag_name":"v1.1.0","html_url":"https://evil.example/x","body":""}`
	if c.do("POST", "/api/update", map[string]any{}, &u); u.Newer || u.URL != releasesPage {
		t.Fatalf("same version / foreign link: %+v", u)
	}
	release = `oops`
	if c.do("POST", "/api/update", map[string]any{}, &u); u.Error == "" || u.Newer {
		t.Fatalf("broken answer: %+v", u)
	}

	// The switch is remembered across restarts.
	if code := c.do("POST", "/api/settings", map[string]bool{"updateCheck": false}, nil); code != 200 {
		t.Fatal("settings")
	}
	if c.do("GET", "/api/state", nil, &st); st.UpdateCheck {
		t.Fatal("the setting was not applied")
	}

	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.1.0", "1.0.0", true}, {"1.0.1", "1.0.0", true}, {"2.0", "1.9.9", true}, {"1.10.0", "1.9.0", true},
		{"1.0.0", "1.0.0", false}, {"1.0.0", "1.1.0", false}, {"1.1", "1.1.0", false},
		{"v1.2.0", "dev", false}, {"nightly", "1.0.0", false}, {"", "1.0.0", false}, {"1.2.0-beta", "1.1.0", true},
	} {
		if got := newerVersion(tc.latest, tc.current); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v", tc.latest, tc.current, got)
		}
	}
}

func TestPickAPK(t *testing.T) {
	fl := []ghAsset{
		{Name: "app-arm64-v8a-release.apk"}, {Name: "app-armeabi-release.apk"},
		{Name: "app-armeabi-v7a-release.apk"}, {Name: "notes.txt"},
	}
	for abi, want := range map[string]string{
		"armeabi-v7a": "app-armeabi-v7a-release.apk",
		"arm64-v8a":   "app-arm64-v8a-release.apk",
		"armeabi":     "app-armeabi-release.apk",
	} {
		if a, err := pickAPK(fl, abi); err != nil || a.Name != want {
			t.Errorf("%s: %s %v", abi, a.Name, err)
		}
	}
	if _, err := pickAPK(fl, "x86"); err == nil {
		t.Error("x86 TV must not get an ARM build")
	}
	if a, err := pickAPK(fl[1:], "arm64-v8a"); err != nil || a.Name != "app-armeabi-v7a-release.apk" {
		t.Errorf("64-bit TV must fall back to the 32-bit build: %s %v", a.Name, err)
	}
	uni := []ghAsset{{Name: "app-4.73-release.apk"}}
	if a, err := pickAPK(uni, "armeabi-v7a"); err != nil || a.Name != uni[0].Name {
		t.Errorf("universal build: %v", err)
	}
	if _, err := pickAPK(nil, "arm64-v8a"); err == nil {
		t.Error("a release without APK files must be an error")
	}
}

// A release that points anywhere but the developer's GitHub page is refused.
func TestDownloadRefusesForeignLinks(t *testing.T) {
	link := ""
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/app/releases/latest" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(ghRelease{Tag: "1.0", Assets: []ghAsset{{Name: "app-1.0.apk", Size: 1000, URL: link}}})
	}))
	defer gh.Close()
	old := githubAPI
	githubAPI = gh.URL
	defer func() { githubAPI = old }()

	s, _ := newServer(t)
	for _, bad := range []string{gh.URL + "/file.apk", "https://github.com/someone/else/releases/download/1/x.apk", "http://github.com/example/app/releases/download/1.0/x.apk"} {
		link = bad
		_, _, err := s.downloadAPK(context.Background(), Launcher{Name: "App", Repo: "example/app"}, "armeabi-v7a")
		if err == nil || !strings.Contains(err.Error(), "неожиданную ссылку") {
			t.Errorf("link %s: %v", bad, err)
		}
	}
}
