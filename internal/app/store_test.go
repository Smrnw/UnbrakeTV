package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Smrnw/UnbrakeTV/internal/faketv"
)

// testSHA is the commit the fake GitHub serves the app-store repository at.
const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestMain(m *testing.M) {
	// Tests never talk to the real GitHub.
	githubRaw, githubAPI = "http://127.0.0.1:1", "http://127.0.0.1:1"
	os.Exit(m.Run())
}

func demo(t *testing.T) (*Server, *client) {
	t.Helper()
	s, c := newServer(t)
	if code := c.do("POST", "/api/demo", map[string]any{}, nil); code != 200 {
		t.Fatalf("demo: %d", code)
	}
	c.waitStatus("connected")
	return s, c
}

type results struct {
	Results []struct {
		ID, Method, Output string
		OK                 bool
	}
}

type launchersResp struct {
	Catalog []Launcher
	Have    map[string]bool
	Hidden  int
	Store   struct {
		Repo, Error            string
		Active, Default, Stale bool
		Updated                int64
	}
}

func ids(l []Launcher) string {
	var s []string
	for _, x := range l {
		s = append(s, x.ID)
	}
	return strings.Join(s, ",")
}

// The user shapes the list: add an own source, remove entries, bring them back.
func TestCatalogManagement(t *testing.T) {
	_, c := demo(t)
	var ln launchersResp
	if c.do("GET", "/api/launchers", nil, &ln); ids(ln.Catalog) != "demo-home,demo-home-lite,demo-browser,demo-player,demo-saver" || ln.Store.Repo != DefaultStoreRepo {
		t.Fatalf("built-in catalog: %s %+v", ids(ln.Catalog), ln.Store)
	}
	var e struct{ Error string }
	for _, bad := range []string{"", "not a repo", "a/b/../c", "https://evil.example/x/y"} {
		if code := c.do("POST", "/api/catalog", map[string]string{"action": "add", "repo": bad}, &e); code != 400 {
			t.Errorf("repo %q: %d", bad, code)
		}
	}
	if code := c.do("POST", "/api/catalog", map[string]string{"action": "add", "repo": "https://github.com/example/Lean-Home/releases", "kind": "launcher"}, nil); code != 200 {
		t.Fatalf("add: %d", code)
	}
	if code := c.do("POST", "/api/catalog", map[string]string{"action": "add", "repo": "example/Lean-Home"}, nil); code != 409 {
		t.Errorf("the same repository twice: %d", code)
	}
	c.do("GET", "/api/launchers", nil, &ln)
	mine := ln.Catalog[len(ln.Catalog)-1]
	if len(ln.Catalog) != 6 || mine.Repo != "example/Lean-Home" || mine.Kind != "launcher" || mine.Source != "custom" || mine.Name != "Lean Home" {
		t.Fatalf("custom entry: %+v", mine)
	}
	// Install it: the package name is learned from the TV and remembered.
	var inst struct {
		Package  string
		Launcher bool
	}
	if code := c.do("POST", "/api/launcher/install", map[string]string{"id": mine.ID}, &inst); code != 200 || inst.Package == "" || !inst.Launcher {
		t.Fatalf("install custom: %d %+v", code, inst)
	}
	if c.do("GET", "/api/launchers", nil, &ln); !ln.Have[mine.ID] || ln.Have["demo-browser"] {
		t.Fatalf("installed marks: %+v", ln.Have)
	}
	// An ordinary app is not offered as a home screen.
	if code := c.do("POST", "/api/launcher/install", map[string]string{"id": "demo-browser"}, &inst); code != 200 || inst.Launcher || inst.Package == "" {
		t.Fatalf("install app: %d %+v", code, inst)
	}
	// Remove: own entries are deleted, the others are hidden.
	for _, id := range []string{mine.ID, "demo-home-lite", "demo-player"} {
		if code := c.do("POST", "/api/catalog", map[string]string{"action": "remove", "id": id}, nil); code != 200 {
			t.Errorf("remove %s: %d", id, code)
		}
	}
	if code := c.do("POST", "/api/catalog", map[string]string{"action": "remove", "id": "nope"}, nil); code != 404 {
		t.Errorf("remove unknown: %d", code)
	}
	if c.do("GET", "/api/launchers", nil, &ln); ids(ln.Catalog) != "demo-home,demo-browser,demo-saver" || ln.Hidden != 2 {
		t.Fatalf("after removing: %s hidden=%d", ids(ln.Catalog), ln.Hidden)
	}
	if code := c.do("POST", "/api/launcher/install", map[string]string{"id": "demo-home-lite"}, nil); code != 400 {
		t.Errorf("a hidden entry must not be installable: %d", code)
	}
	c.do("POST", "/api/catalog", map[string]string{"action": "reset"}, nil)
	if c.do("GET", "/api/launchers", nil, &ln); ids(ln.Catalog) != "demo-home,demo-home-lite,demo-browser,demo-player,demo-saver" || ln.Hidden != 0 {
		t.Fatalf("after reset: %s", ids(ln.Catalog))
	}
}

// The app set lives in a separate GitHub repository.
func TestAppStoreRepository(t *testing.T) {
	const store = "me/tv-apps"
	apk := append([]byte("PK\x03\x04"), make([]byte, 120_000)...)
	treeJSON := `{"tree":[
	  {"path":"catalog.json","type":"blob"},{"path":"README.md","type":"blob"},{"path":"apps","type":"tree"},
	  {"path":"apps/My Player (1).apk","type":"blob"},{"path":"apps/media-21.apk","type":"blob"},{"path":"apps/readme.md","type":"blob"},
	  {"path":"apps/Media","type":"tree"},{"path":"apps/Media/media-21-arm64-v8a.apk","type":"blob"},{"path":"apps/Media/media-21-armeabi-v7a.apk","type":"blob"},{"path":"apps/Media/description.txt","type":"blob"},
	  {"path":"Лаунчер TVX/tvx.apk","type":"blob"},{"path":"Лаунчер TVX/info.json","type":"blob"},{"path":"Лаунчер TVX/README.md","type":"blob"},
	  {"path":"apps/Stream/info.json","type":"blob"},
	  {"path":"apps/Broken/info.json","type":"blob"},{"path":"apps/Broken/b.apk","type":"blob"},
	  {"path":"apps/Empty/description.txt","type":"blob"},
	  {"path":".github/x/y.apk","type":"blob"},{"path":"apps/Deep/more/z.apk","type":"blob"},{"path":"docs/manual.apk","type":"blob"}
	]}`
	catalogJSON := `{"apps":[
	  {"id":"player","name":"Мой плеер","description":"Лучший плеер","file":"apps/My Player (1).apk"},
	  {"name":"Launcher One","kind":"launcher","repo":"example/launcher","package":"com.example.launcher","store":true},
	  {"name":"Плохая запись","file":"../../etc/passwd"},
	  {"name":"","file":"apps/x.apk"}
	]}`
	picked := make(chan string, 4)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/apps/Media/") && strings.HasSuffix(r.URL.Path, ".apk") {
			picked <- r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		}
		switch r.URL.Path {
		case "/" + store + "/" + testSHA + "/catalog.json":
			io.WriteString(w, catalogJSON)
		case "/repos/" + store + "/commits/HEAD":
			io.WriteString(w, testSHA)
		case "/repos/" + store + "/git/trees/" + testSHA:
			io.WriteString(w, treeJSON)
		case "/" + store + "/" + testSHA + "/apps/Media/description.txt":
			io.WriteString(w, "\xef\xbb\xbfМедиацентр для фильмов,\r\nмузыки и фото.\r\n\r\n- работает с сетевыми папками\r\n")
		case "/" + store + "/" + testSHA + "/Лаунчер TVX/info.json":
			io.WriteString(w, `{"kind":"launcher","package":"com.example.tvxhome","store":true}`)
		case "/" + store + "/" + testSHA + "/Лаунчер TVX/README.md":
			io.WriteString(w, "# TVX Home\n\nПростой **рабочий стол** с виджетами.\n")
		case "/" + store + "/" + testSHA + "/apps/Stream/info.json":
			io.WriteString(w, `{"name":"Stream","description":"Берётся у разработчика.","repo":"example/stream"}`)
		case "/" + store + "/" + testSHA + "/apps/Broken/info.json":
			io.WriteString(w, `{"name": "Сломанный",`)
		case "/" + store + "/" + testSHA + "/apps/Media/media-21-armeabi-v7a.apk", "/" + store + "/" + testSHA + "/Лаунчер TVX/tvx.apk":
			w.Write(apk)
		case "/repos/" + store + "/releases/latest":
			json.NewEncoder(w).Encode(ghRelease{Tag: "2026.11", Assets: []ghAsset{
				{Name: "big-game.apk", URL: "https://github.com/" + store + "/releases/download/2026.11/big-game.apk"},
				{Name: "evil.apk", URL: "https://evil.example/evil.apk"},
			}})
		case "/" + store + "/" + testSHA + "/apps/My Player (1).apk", "/" + store + "/" + testSHA + "/apps/media-21.apk":
			w.Write(apk)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	oldRaw, oldAPI := githubRaw, githubAPI
	githubRaw, githubAPI = gh.URL, gh.URL
	defer func() { githubRaw, githubAPI = oldRaw, oldAPI }()

	_, c := newServer(t)
	f, _ := faketv.New(faketv.Options{})
	defer f.Close()
	addr, _ := f.Listen("127.0.0.1:0")
	c.do("POST", "/api/connect", map[string]string{"address": addr}, nil)
	c.waitStatus("connected")

	// The default repository does not exist yet: the list is empty.
	var ln launchersResp
	if c.do("GET", "/api/launchers", nil, &ln); len(ln.Catalog) != 0 || ln.Store.Active || ln.Store.Error == "" {
		t.Fatalf("no store yet: %s %+v", ids(ln.Catalog), ln.Store)
	}
	// Point the program at the user's repository.
	var sw struct{ Warning string }
	if code := c.do("POST", "/api/catalog", map[string]string{"action": "store", "repo": "https://github.com/" + store}, &sw); code != 200 || !strings.Contains(sw.Warning, "apps/Broken/info.json") {
		t.Fatalf("switch store: %d %q", code, sw.Warning)
	}
	c.do("GET", "/api/launchers", nil, &ln)
	if ids(ln.Catalog) != "player,launcher-one,broken,media,stream,tvx,media-21,big-game" || !ln.Store.Active || ln.Store.Repo != store || ln.Store.Default {
		t.Fatalf("store catalog: %s %+v", ids(ln.Catalog), ln.Store)
	}
	byID := map[string]Launcher{}
	for _, l := range ln.Catalog {
		byID[l.ID] = l
	}
	if p := byID["player"]; p.Name != "Мой плеер" || p.File != "apps/My Player (1).apk" || p.Source != "store" || p.Kind != "app" {
		t.Errorf("described file: %+v", p)
	}
	if p := byID["launcher-one"]; p.Kind != "launcher" || p.Repo != "example/launcher" || !p.Store || p.Site == "" {
		t.Errorf("upstream entry: %+v", p)
	}
	if p := byID["media-21"]; p.Name != "media 21" || p.File != "apps/media-21.apk" {
		t.Errorf("undescribed file must be listed under its name: %+v", p)
	}
	if p := byID["big-game"]; !strings.HasPrefix(p.Asset, "https://github.com/"+store+"/releases/download/") {
		t.Errorf("release file: %+v", p)
	}

	// A folder is an app: the description file gives the text, info.json the details.
	if p := byID["media"]; p.Name != "Media" || p.Description != "Медиацентр для фильмов, музыки и фото. работает с сетевыми папками" || len(p.Files) != 2 || p.Kind != "app" {
		t.Errorf("folder with description.txt: %+v", p)
	}
	if p := byID["tvx"]; p.Name != "TVX Home" || p.Description != "Простой рабочий стол с виджетами." || p.Kind != "launcher" || !p.Store || p.File != "Лаунчер TVX/tvx.apk" {
		t.Errorf("folder with info.json and README: %+v", p)
	}
	if p := byID["stream"]; p.Repo != "example/stream" || p.File != "" {
		t.Errorf("folder that points to another project: %+v", p)
	}
	if p := byID["broken"]; p.Name != "Broken" || p.File != "apps/Broken/b.apk" || !strings.Contains(ln.Store.Error, "apps/Broken/info.json") {
		t.Errorf("a broken info.json must not hide the app: %+v, %q", p, ln.Store.Error)
	}

	// Install straight from the repository; the build for this TV's processor is chosen.
	var inst struct{ Package string }
	for _, id := range []string{"player", "media-21", "media", "tvx"} {
		if code := c.do("POST", "/api/launcher/install", map[string]string{"id": id}, &inst); code != 200 {
			t.Fatalf("install %s: %d", id, code)
		}
	}
	if pk := c.packages(); pk["com.example.sideloaded"].State != "installed" {
		t.Fatal("the app from the repository did not reach the TV")
	}
	if got := <-picked; got != "media-21-armeabi-v7a.apk" {
		t.Errorf("the TV is 32-bit ARM, but %s was downloaded", got)
	}
	if c.do("GET", "/api/launchers", nil, &ln); !ln.Have["player"] {
		t.Errorf("installed mark: %+v", ln.Have)
	}

	// The repository changed: an app was taken away.
	catalogJSON = `{"apps":[{"name":"Только один","file":"apps/media-21.apk"}]}`
	if c.do("GET", "/api/launchers?refresh=1", nil, &ln); ids(ln.Catalog) != "player,big-game" {
		t.Logf("catalog after change: %s", ids(ln.Catalog))
	}
	if !strings.Contains(ids(ln.Catalog), "big-game") || strings.Contains(ids(ln.Catalog), "launcher-one") || !strings.Contains(ids(ln.Catalog), "media,") {
		t.Fatalf("the list must follow the repository: %s", ids(ln.Catalog))
	}
	// A broken catalog.json is reported, the files are still listed.
	catalogJSON = `{"apps":[`
	if c.do("GET", "/api/launchers?refresh=1", nil, &ln); !strings.Contains(ln.Store.Error, "catalog.json") || len(ln.Catalog) == 0 {
		t.Fatalf("broken catalog: %+v %s", ln.Store, ids(ln.Catalog))
	}
	// Back to the default repository (which is absent here) → an empty list again.
	c.do("POST", "/api/catalog", map[string]string{"action": "store", "repo": ""}, nil)
	if c.do("GET", "/api/launchers", nil, &ln); len(ln.Catalog) != 0 || ln.Store.Repo != DefaultStoreRepo {
		t.Fatalf("default store: %s", ids(ln.Catalog))
	}
}

// A hiccup of GitHub must not freeze an old list, and a list read before the
// program was restarted must be checked again (the case of 14 November 2026:
// an app was replaced in the repository, the program kept showing the old one).
func TestStoreListFollowsRepository(t *testing.T) {
	const store = "me/tv-apps"
	var mu sync.Mutex
	mode, sha, app := "ok", strings.Repeat("1", 40), "Old Home"
	set := func(m, h, a string) { mu.Lock(); mode, sha, app = m, h, a; mu.Unlock() }
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		switch {
		case mode == "down":
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case p == "/repos/"+store+"/commits/HEAD" && mode == "ok":
			io.WriteString(w, sha)
		case p == "/repos/"+store && mode != "gone":
			io.WriteString(w, `{"full_name":"me/tv-apps"}`)
		case p == "/repos/"+store+"/git/trees/"+sha:
			io.WriteString(w, `{"tree":[{"path":"apps/`+app+`/info.json","type":"blob"}]}`)
		case p == "/"+store+"/"+sha+"/apps/"+app+"/info.json":
			// Only the exact commit is served: a read through "HEAD" would miss it.
			io.WriteString(w, `{"kind":"launcher","repo":"example/home"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	oldRaw, oldAPI := githubRaw, githubAPI
	githubRaw, githubAPI = gh.URL, gh.URL
	defer func() { githubRaw, githubAPI = oldRaw, oldAPI }()

	f, _ := faketv.New(faketv.Options{})
	defer f.Close()
	addr, _ := f.Listen("127.0.0.1:0")
	start := func(dir string) (*Server, *client) {
		s, err := New(dir, "1.2.1")
		if err != nil {
			t.Fatal(err)
		}
		h := httptest.NewServer(s.Handler(http.NotFoundHandler()))
		t.Cleanup(h.Close)
		c := &client{t: t, url: h.URL, token: s.Token}
		c.do("POST", "/api/connect", map[string]string{"address": addr}, nil)
		c.waitStatus("connected")
		return s, c
	}
	s, c := start(t.TempDir())
	var ln launchersResp
	list := func(c *client, q string) string {
		t.Helper()
		ln = launchersResp{}
		if code := c.do("GET", "/api/launchers"+q, nil, &ln); code != 200 {
			t.Fatalf("launchers: %d", code)
		}
		return ids(ln.Catalog)
	}

	c.do("POST", "/api/catalog", map[string]string{"action": "store", "repo": store}, nil)
	if got := list(c, ""); got != "old-home" || ln.Store.Stale || ln.Store.Error != "" || ln.Catalog[0].Kind != "launcher" {
		t.Fatalf("first listing: %s %+v %+v", got, ln.Store, ln.Catalog)
	}

	// The app is replaced, and at that moment GitHub says "not found" for the
	// repository: the old list stays, but is marked as old and is retried.
	set("blip", strings.Repeat("2", 40), "New Home")
	if got := list(c, "?refresh=1"); got != "old-home" || !ln.Store.Stale || !strings.Contains(ln.Store.Error, "ещё раз") || ln.Store.Updated == 0 {
		t.Fatalf("after a hiccup: %s %+v", got, ln.Store)
	}

	// The program is restarted a moment later: the list is read again at once
	// instead of being taken from the cache for ten more minutes.
	set("ok", strings.Repeat("2", 40), "New Home")
	_, c2 := start(s.DataDir)
	if got := list(c2, ""); got != "new-home" || ln.Store.Stale || ln.Store.Error != "" {
		t.Fatalf("after a restart: %s %+v", got, ln.Store)
	}

	// No connection: the list is kept; a minute later it is retried by itself.
	set("down", strings.Repeat("2", 40), "New Home")
	if got := list(c2, "?refresh=1"); got != "new-home" || !ln.Store.Stale {
		t.Fatalf("GitHub down: %s %+v", got, ln.Store)
	}
	var cache storeCache
	readJSON(filepath.Join(s.DataDir, "store.json"), &cache)
	cache.Time -= storeRetry + 1
	writeJSON2(filepath.Join(s.DataDir, "store.json"), cache)
	set("ok", strings.Repeat("3", 40), "Newest Home")
	if got := list(c2, ""); got != "newest-home" || ln.Store.Stale {
		t.Fatalf("retry after a minute: %s %+v", got, ln.Store)
	}

	// The repository is really gone: the old list is not shown any more.
	set("gone", strings.Repeat("3", 40), "Newest Home")
	if got := list(c2, "?refresh=1"); got != "" || ln.Store.Stale || !strings.Contains(ln.Store.Error, "не найден") {
		t.Fatalf("repository deleted: %q %+v", got, ln.Store)
	}
}
