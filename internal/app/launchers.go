package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/faketv"
	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

// Launcher is an app the program can install on the TV: a lightweight home
// screen (Kind "launcher") or any other useful app (Kind "app").
//
// Where the APK comes from, in order of precedence:
//   - File:  a file inside the app-store repository;
//   - Asset: a file attached to the latest release of the app-store repository;
//   - Repo:  the latest release of the developer's own GitHub repository.
//
// Nothing is bundled with the program: the file is downloaded at the moment
// the user presses "Установить".
type Launcher struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Repo        string   `json:"repo,omitempty"`
	File        string   `json:"file,omitempty"`
	Files       []string `json:"files,omitempty"` // several builds of one app, for different processors
	Asset       string   `json:"asset,omitempty"`
	Package     string   `json:"package,omitempty"` // Android package name, if known
	Store       bool     `json:"store,omitempty"`   // also available in Google Play on the TV
	Site        string   `json:"site,omitempty"`
	Source      string   `json:"source"` // builtin | store | custom
}

// DefaultStoreRepo is the GitHub repository with the project's own app set.
const DefaultStoreRepo = "Smrnw/UnbrakeTV-apps"

// githubRaw is a variable so that tests can point it at a local server.
var githubRaw = "https://raw.githubusercontent.com"

// launcherCatalog is what the built-in demo TV offers: stand-ins that show
// how installation works. Real apps come only from the app-store repository.
var launcherCatalog = []Launcher{
	{ID: "demo-home", Kind: "launcher", Name: "Демо: рабочий стол", Description: "Пример рабочего стола. В демо-режиме вместо настоящего приложения устанавливается заглушка.", Package: "com.example.demohome", Store: true},
	{ID: "demo-home-lite", Kind: "launcher", Name: "Демо: лёгкий рабочий стол", Description: "Ещё один пример рабочего стола для демо-режима.", Package: "com.example.demohomelite"},
	{ID: "demo-browser", Kind: "app", Name: "Демо: браузер", Description: "Пример приложения для демо-режима."},
	{ID: "demo-player", Kind: "app", Name: "Демо: видеоплеер", Description: "Пример приложения для демо-режима."},
	{ID: "demo-saver", Kind: "app", Name: "Демо: заставка", Description: "Пример приложения для демо-режима."},
}

var (
	repoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	fileRe = regexp.MustCompile(`^[A-Za-z0-9А-Яа-яЁё_ ./()+-]{1,200}\.apk$`)
	slugRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// parseRepo accepts "owner/name" or a github.com link.
func parseRepo(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "..") || strings.ContainsAny(s, " \\?#") {
		return s, false
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "www."), "github.com/")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	if p := strings.Split(s, "/"); len(p) > 2 {
		s = p[0] + "/" + p[1]
	}
	return s, repoRe.MatchString(s) && !strings.Contains(s, "..")
}

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// localCatalog is the user's own adjustments, kept on this computer.
type localCatalog struct {
	StoreRepo string            `json:"storeRepo"`
	Custom    []Launcher        `json:"custom"`
	Hidden    []string          `json:"hidden"`
	Packages  map[string]string `json:"packages"` // id → package name learned at installation
}

func (s *Server) localCatalog() localCatalog {
	lc := localCatalog{}
	s.fileMu.Lock()
	readJSON(filepath.Join(s.DataDir, "catalog.json"), &lc)
	s.fileMu.Unlock()
	if _, ok := parseRepo(lc.StoreRepo); !ok {
		lc.StoreRepo = DefaultStoreRepo
	}
	if lc.Packages == nil {
		lc.Packages = map[string]string{}
	}
	return lc
}

func (s *Server) saveLocalCatalog(lc localCatalog) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return writeJSON2(filepath.Join(s.DataDir, "catalog.json"), lc)
}

// storeCache is the last listing of the app-store repository.
type storeCache struct {
	Repo  string     `json:"repo"`
	Time  int64      `json:"time"`  // last attempt to read the repository
	OK    int64      `json:"ok"`    // when Items were read successfully
	Ref   string     `json:"ref"`   // the commit Items were read from
	Stale bool       `json:"stale"` // the last attempt failed: Items are older, retry soon
	Items []Launcher `json:"items"`
	Error string     `json:"error"`
}

// storeRetry is how soon a failed look into the repository is repeated.
const storeRetry = 60

func (s *Server) storeCache() storeCache {
	var c storeCache
	s.fileMu.Lock()
	readJSON(filepath.Join(s.DataDir, "store.json"), &c)
	s.fileMu.Unlock()
	return c
}

func (s *Server) getText(ctx context.Context, link string, limit int64, accept ...string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "UnbrakeTV/"+s.Version)
	req.Header.Set("Accept", "application/vnd.github+json")
	if len(accept) > 0 {
		req.Header.Set("Accept", accept[0])
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("нет связи с GitHub — проверьте интернет на компьютере")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, resp.StatusCode, nil
}

func cleanItem(x Launcher) (Launcher, bool) {
	x.Name, x.Description = strings.TrimSpace(x.Name), strings.TrimSpace(x.Description)
	if x.Name == "" || len(x.Name) > 100 || len(x.Description) > 600 {
		return x, false
	}
	if x.Kind != "launcher" {
		x.Kind = "app"
	}
	x.File = strings.TrimPrefix(path.Clean("/"+x.File), "/")
	if x.File == "." {
		x.File = ""
	}
	if x.File != "" && !fileRe.MatchString(x.File) {
		return x, false
	}
	if x.Repo != "" {
		r, ok := parseRepo(x.Repo)
		if !ok {
			return x, false
		}
		x.Repo = r
	}
	for _, f := range x.Files {
		if f != path.Clean(f) || !fileRe.MatchString(f) {
			return x, false
		}
	}
	if x.File == "" && x.Repo == "" && x.Asset == "" && len(x.Files) == 0 {
		return x, false
	}
	if x.Package != "" && !tv.ValidPackage(x.Package) {
		x.Package = ""
	}
	if x.Site != "" && !strings.HasPrefix(x.Site, "https://") {
		x.Site = ""
	}
	// Names may be in any language; the id falls back to the file or repository.
	for _, src := range []string{x.ID, x.Name, titleFromFile(x.File), x.Repo, titleFromFile(x.Asset), "app"} {
		if x.ID = slug(src); x.ID != "" {
			break
		}
	}
	return x, true
}

func titleFromFile(name string) string {
	name = strings.TrimSuffix(path.Base(name), path.Ext(name))
	return strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(name))
}

// rawURL is the address of a file inside a repository at the given commit
// ("HEAD" is the default branch, but GitHub may serve it from a cache for
// several minutes; an exact commit is always served as it is).
func rawURL(repo, ref, file string) string {
	parts := strings.Split(file, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	if ref == "" {
		ref = "HEAD"
	}
	return githubRaw + "/" + repo + "/" + ref + "/" + strings.Join(parts, "/")
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// skipDirs are folders of a repository that never hold apps.
var skipDirs = map[string]bool{"knowledge": true, "docs": true, "internal": true, "cmd": true}

// appFolder is one folder of the app-store repository: one app.
type appFolder struct {
	apks       []string
	info, desc string
	descRank   int
}

var descFiles = map[string]int{"description.txt": 4, "описание.txt": 3, "readme.md": 2, "readme.txt": 1}

// plainText turns a description file into one paragraph. A leading Markdown
// heading is returned separately: it is the app's name.
func plainText(b []byte) (title, text string) {
	var out []string
	for i, l := range strings.Split(strings.ReplaceAll(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), "\r", ""), "\n") {
		l = strings.TrimSpace(l)
		if h := strings.TrimLeft(l, "#"); h != l {
			if h = strings.TrimSpace(h); title == "" && len(out) == 0 && i < 5 {
				title = h
				continue
			}
			l = h
		}
		l = strings.TrimSpace(strings.TrimLeft(l, "*->"))
		l = strings.NewReplacer("**", "", "__", "", "`", "").Replace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	text = strings.Join(out, " ")
	if r := []rune(text); len(r) > 500 {
		text = strings.TrimSpace(string(r[:500])) + "…"
	}
	if r := []rune(title); len(r) > 100 {
		title = string(r[:100])
	}
	return
}

// refreshStore reads the app-store repository. An app is a folder:
//
//	apps/Player/player.apk        the installation file (several builds for
//	                             different processors may lie side by side)
//	apps/Player/description.txt    what the user reads under the name (README.md works too)
//	apps/Player/info.json          optional: name, kind, package, link to another project
//
// Folders may also sit in the repository root. Besides that, single *.apk
// files in the root, in "apps" and in the latest release are listed under
// their file names, and catalog.json in the root may describe apps as a list.
func (s *Server) refreshStore(ctx context.Context) storeCache {
	repo := s.localCatalog().StoreRepo
	c := storeCache{Repo: repo, Time: time.Now().Unix(), Items: []Launcher{}}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	ids, files := map[string]bool{}, map[string]bool{}
	add := func(x Launcher) {
		x, ok := cleanItem(x)
		if !ok || len(c.Items) >= 200 {
			return
		}
		for base, n := x.ID, 2; ids[x.ID]; n++ {
			x.ID = fmt.Sprintf("%s-%d", base, n)
		}
		ids[x.ID] = true
		x.Source = "store"
		for _, f := range append([]string{x.File}, x.Files...) {
			if f != "" {
				files[strings.ToLower(f)] = true
				files[strings.ToLower(path.Base(f))] = true
			}
		}
		c.Items = append(c.Items, x)
	}
	problem := func(msg string) {
		if c.Error == "" {
			c.Error = msg
		}
	}

	// The listing is read from one exact commit: the file list and every
	// description belong to the same moment, and nothing comes from a cache.
	// transient: the answer says nothing about the repository itself (no
	// connection, a limit, a hiccup of GitHub) — keep the last good list.
	transient := false
	ref := ""
	again := " — попробую ещё раз через минуту."
	b, code, err := s.getText(ctx, githubAPI+"/repos/"+repo+"/commits/HEAD", 4<<10, "application/vnd.github.sha")
	switch {
	case err != nil:
		problem(err.Error())
		transient = true
	case code == http.StatusOK && shaRe.MatchString(strings.TrimSpace(string(b))):
		ref = strings.TrimSpace(string(b))
	case code == http.StatusForbidden || code == http.StatusTooManyRequests:
		problem("GitHub временно ограничил запросы с вашего адреса" + again)
		transient = true
	case code == http.StatusConflict:
		problem("Репозиторий " + repo + " пока пуст.")
	case code == http.StatusNotFound || code == http.StatusUnprocessableEntity:
		// Right after a change GitHub sometimes answers "not found" for an
		// existing repository: ask about the repository itself to be sure.
		if _, rc, rerr := s.getText(ctx, githubAPI+"/repos/"+repo, 1<<20); rerr == nil && rc == http.StatusNotFound {
			problem("Репозиторий " + repo + " не найден.")
		} else {
			problem("GitHub не отдал список приложений" + again)
			transient = true
		}
	default:
		problem(fmt.Sprintf("GitHub ответил ошибкой %d", code) + again)
		transient = true
	}

	var tree struct {
		Tree      []struct{ Path, Type string }
		Truncated bool
	}
	if ref != "" {
		b, code, err := s.getText(ctx, githubAPI+"/repos/"+repo+"/git/trees/"+ref+"?recursive=1", 16<<20)
		if err != nil || code != http.StatusOK || json.Unmarshal(b, &tree) != nil {
			problem("GitHub не отдал список файлов репозитория" + again)
			transient = true
		} else if tree.Truncated {
			problem("В репозитории слишком много файлов — показаны не все приложения.")
		}
	}
	// read fetches a file of the listed commit. A missing file is fine; any
	// other failure means the listing would be incomplete.
	read := func(file string, limit int64) ([]byte, bool) {
		b, code, err := s.getText(ctx, rawURL(repo, ref, file), limit)
		if err == nil && code == http.StatusOK {
			return b, true
		}
		if err != nil || code != http.StatusNotFound {
			problem("GitHub не отдал файл " + file + again)
			transient = true
		}
		return nil, false
	}
	var flat []string
	folders := map[string]*appFolder{}
	hasCatalog := false
	for _, f := range tree.Tree {
		if f.Type != "blob" {
			continue
		}
		dir, name := path.Dir(f.Path), strings.ToLower(path.Base(f.Path))
		isAPK := strings.HasSuffix(name, ".apk")
		if dir == "." || dir == "apps" {
			hasCatalog = hasCatalog || f.Path == "catalog.json"
			if isAPK {
				flat = append(flat, f.Path)
			}
			continue
		}
		// An app folder sits in the root or directly inside "apps".
		rest := strings.TrimPrefix(dir, "apps/")
		if strings.Contains(rest, "/") || strings.HasPrefix(rest, ".") || skipDirs[strings.ToLower(rest)] && rest == dir {
			continue
		}
		fo := folders[dir]
		if fo == nil {
			fo = &appFolder{}
			folders[dir] = fo
		}
		switch {
		case isAPK:
			fo.apks = append(fo.apks, f.Path)
		case name == "info.json":
			fo.info = f.Path
		case descFiles[name] > fo.descRank:
			fo.desc, fo.descRank = f.Path, descFiles[name]
		}
	}

	if hasCatalog {
		if b, ok := read("catalog.json", 2<<20); ok {
			var doc struct{ Apps []Launcher }
			if json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &doc) != nil {
				problem("В файле catalog.json ошибка — проверьте запятые и кавычки.")
			}
			for _, x := range doc.Apps {
				x.Asset, x.Files = "", nil // only the program fills these in
				add(x)
			}
		}
	}

	var dirs []string
	for d, fo := range folders {
		if len(fo.apks) > 0 || fo.info != "" {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	if len(dirs) > 100 {
		dirs = dirs[:100]
		problem("В репозитории больше ста приложений — показаны первые сто.")
	}
	for _, d := range dirs {
		fo := folders[d]
		folder := path.Base(d)
		x := Launcher{ID: folder}
		if fo.info != "" {
			if b, ok := read(fo.info, 256<<10); ok {
				var inf Launcher
				if json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &inf) != nil {
					problem("В файле " + fo.info + " ошибка — проверьте запятые и кавычки.")
				} else {
					x = Launcher{ID: inf.ID, Kind: inf.Kind, Name: inf.Name, Description: inf.Description, Repo: inf.Repo, Package: inf.Package, Store: inf.Store, Site: inf.Site}
					if x.ID == "" {
						x.ID = folder
					}
				}
			}
		}
		if fo.desc != "" && (x.Description == "" || x.Name == "") {
			if b, ok := read(fo.desc, 64<<10); ok {
				title, text := plainText(b)
				if x.Name == "" {
					x.Name = title
				}
				if x.Description == "" {
					x.Description = text
				}
			}
		}
		if x.Name == "" {
			x.Name = strings.TrimSpace(strings.ReplaceAll(folder, "_", " "))
		}
		sort.Strings(fo.apks)
		switch len(fo.apks) {
		case 0:
			if x.Repo == "" {
				continue // a description without anything to install
			}
		case 1:
			x.File, x.Repo = fo.apks[0], ""
		default:
			x.Files, x.Repo = fo.apks, ""
		}
		add(x)
	}

	for _, f := range flat {
		if !files[strings.ToLower(f)] {
			add(Launcher{Name: titleFromFile(f), File: f, Description: "Файл " + path.Base(f) + " из репозитория приложений."})
		}
	}
	if ref != "" {
		if rel, err := s.latestRelease(ctx, repo); err == nil {
			for _, a := range rel.Assets {
				low := strings.ToLower(a.Name)
				if strings.HasSuffix(low, ".apk") && !files[low] && strings.HasPrefix(a.URL, "https://github.com/"+repo+"/releases/download/") {
					add(Launcher{Name: titleFromFile(a.Name), Asset: a.URL, Description: "Файл " + a.Name + " из выпуска " + rel.Tag + "."})
				}
			}
		}
	}
	c.Ref, c.Stale = ref, transient
	if !transient {
		c.OK = c.Time
	} else if old := s.storeCache(); old.Repo == repo && old.OK > 0 {
		// Never replace a good list with a broken one. The old list is shown
		// as old (with the time it was read) and is checked again soon.
		c.Items, c.Ref, c.OK = old.Items, old.Ref, old.OK
	}
	s.fileMu.Lock()
	writeJSON2(filepath.Join(s.DataDir, "store.json"), c)
	s.fileMu.Unlock()
	return c
}

// catalog is what the user sees: the app-store repository (the demo TV has
// its own stand-ins), plus own additions, minus hidden entries.
func (s *Server) catalog() []Launcher {
	lc := s.localCatalog()
	var items []Launcher
	if c := s.storeCache(); c.Repo == lc.StoreRepo && len(c.Items) > 0 {
		items = append(items, c.Items...)
	} else if s.isDemo() {
		for _, l := range launcherCatalog {
			l.Source = "builtin"
			items = append(items, l)
		}
	}
	for _, l := range lc.Custom {
		l.Source = "custom"
		items = append(items, l)
	}
	hidden := map[string]bool{}
	for _, h := range lc.Hidden {
		hidden[h] = true
	}
	out := []Launcher{}
	for _, l := range items {
		if hidden[l.ID] {
			continue
		}
		if p := lc.Packages[l.ID]; p != "" && l.Package == "" {
			l.Package = p
		}
		if l.Site == "" && l.Repo != "" {
			l.Site = "https://github.com/" + l.Repo
		}
		out = append(out, l)
	}
	return out
}

func (s *Server) findLauncher(id string) (Launcher, bool) {
	for _, l := range s.catalog() {
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
		low := strings.ToLower(a.Name)
		if !strings.HasSuffix(low, ".apk") {
			continue
		}
		switch assetABI(a.Name) {
		case "":
			// Prefer a release build over a debug one.
			if universal == nil || strings.Contains(strings.ToLower(universal.Name), "debug") && !strings.Contains(low, "debug") {
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

const maxLauncherSize = 300 << 20

// downloadAPK fetches the installation file into memory.
func (s *Server) downloadAPK(ctx context.Context, l Launcher, abi string) ([]byte, string, error) {
	store := s.localCatalog().StoreRepo
	link, version, size := "", "", int64(0)
	if len(l.Files) > 0 {
		// Several builds lie in the app's folder: take the one for this TV.
		var builds []ghAsset
		for _, f := range l.Files {
			builds = append(builds, ghAsset{Name: path.Base(f), URL: f})
		}
		a, err := pickAPK(builds, abi)
		if err != nil {
			return nil, "", errors.New("в папке приложения нет сборки для процессора этого телевизора")
		}
		l.File = a.URL
	}
	switch {
	case l.File != "":
		ref := ""
		if c := s.storeCache(); c.Repo == store {
			ref = c.Ref
		}
		link = rawURL(store, ref, l.File)
	case l.Asset != "":
		// Only files of the app-store repository's own releases.
		if !strings.HasPrefix(l.Asset, "https://github.com/"+store+"/releases/download/") {
			return nil, "", errors.New("GitHub вернул неожиданную ссылку на файл — установка отменена")
		}
		link = l.Asset
	default:
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
		link, version, size = a.URL, rel.Tag, a.Size
	}
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "UnbrakeTV/"+s.Version)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", errors.New("не удалось скачать файл с GitHub — проверьте интернет на компьютере")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", errors.New("файл не найден на GitHub — возможно, его убрали из репозитория")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub ответил ошибкой %d при скачивании", resp.StatusCode)
	}
	if size == 0 {
		size = resp.ContentLength
	}
	var buf bytes.Buffer
	chunk := make([]byte, 256*1024)
	for {
		n, err := resp.Body.Read(chunk)
		buf.Write(chunk[:n])
		if buf.Len() > maxLauncherSize {
			return nil, "", errors.New("файл слишком большой — установка отменена")
		}
		if size > 0 {
			s.setProgress(strings.TrimSpace(fmt.Sprintf("Скачиваю %s %s", l.Name, version)) + fmt.Sprintf("… %d%%", min(100, int64(buf.Len())*100/size)))
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
	return buf.Bytes(), version, nil
}

func (s *Server) isDemo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.demo
}

func (s *Server) handleLaunchers(w http.ResponseWriter, r *http.Request) {
	d, err := s.device()
	if err != nil {
		httpErr(w, 409, err.Error())
		return
	}
	lc := s.localCatalog()
	cache := s.storeCache()
	// Look into the app-store repository once per start of the program, then
	// every ten minutes, and a minute after a failed attempt (never in the demo).
	age := time.Now().Unix() - cache.Time
	s.mu.Lock()
	seen := s.storeSeen == lc.StoreRepo
	s.mu.Unlock()
	if !s.isDemo() && (!seen || cache.Repo != lc.StoreRepo || age > 600 || cache.Stale && age > storeRetry || r.URL.Query().Get("refresh") != "") {
		cache = s.refreshStore(r.Context())
		s.mu.Lock()
		s.storeSeen = lc.StoreRepo
		s.mu.Unlock()
	}
	installed, current := d.Launchers()
	if installed == nil {
		installed = []string{}
	}
	names := map[string]string{}
	for _, p := range installed {
		names[p] = tv.Lookup(p, true).Name
	}
	have := map[string]bool{}
	cat := s.catalog()
	if out, err := d.Conn.Shell("pm list packages --user 0", 30*time.Second); err == nil {
		for _, l := range cat {
			have[l.ID] = l.Package != "" && strings.Contains(strings.ReplaceAll(out, "\r", ""), "package:"+l.Package+"\n")
		}
	}
	storeInfo := map[string]any{"repo": lc.StoreRepo, "default": lc.StoreRepo == DefaultStoreRepo, "active": cache.Repo == lc.StoreRepo && len(cache.Items) > 0}
	if cache.Repo == lc.StoreRepo {
		storeInfo["error"], storeInfo["stale"], storeInfo["updated"] = cache.Error, cache.Stale && cache.OK > 0, cache.OK
	}
	writeJSON(w, map[string]any{
		"catalog": cat, "installed": installed, "current": current, "names": names, "have": have,
		"store": storeInfo, "hidden": len(lc.Hidden),
	})
}

// handleCatalog lets the user shape the list: add an own source, hide or
// delete an entry, restore the full list, switch the app-store repository.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct{ Action, ID, Repo, Name, Kind, Description string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	lc := s.localCatalog()
	switch req.Action {
	case "add":
		repo, ok := parseRepo(req.Repo)
		if !ok {
			httpErr(w, 400, "Укажите репозиторий на GitHub: владелец/название или ссылку вида https://github.com/владелец/название")
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = titleFromFile(repo[strings.Index(repo, "/")+1:])
		}
		item, ok := cleanItem(Launcher{ID: "my-" + repo, Kind: req.Kind, Name: name, Description: req.Description, Repo: repo})
		if !ok {
			httpErr(w, 400, "Проверьте название и описание: они слишком длинные или пустые.")
			return
		}
		for _, l := range s.catalog() {
			if l.ID == item.ID || l.Repo == repo {
				httpErr(w, 409, "Этот репозиторий уже есть в списке: "+l.Name)
				return
			}
		}
		if !s.isDemo() {
			rel, err := s.latestRelease(r.Context(), repo)
			if err != nil {
				httpErr(w, 502, "Не удалось найти выпуски в репозитории "+repo+": "+err.Error())
				return
			}
			hasAPK := false
			for _, a := range rel.Assets {
				hasAPK = hasAPK || strings.HasSuffix(strings.ToLower(a.Name), ".apk")
			}
			if !hasAPK {
				httpErr(w, 400, "В последнем выпуске "+repo+" нет APK-файлов — устанавливать нечего.")
				return
			}
		}
		if item.Description == "" {
			item.Description = "Добавлено вами. Устанавливается последняя версия из выпусков " + repo + "."
		}
		lc.Custom = append(lc.Custom, item)
	case "remove":
		kept := []Launcher{}
		for _, l := range lc.Custom {
			if l.ID != req.ID {
				kept = append(kept, l)
			}
		}
		if len(kept) == len(lc.Custom) {
			if _, ok := s.findLauncher(req.ID); !ok {
				httpErr(w, 404, "Такой записи нет в списке.")
				return
			}
			lc.Hidden = append(lc.Hidden, req.ID)
		}
		lc.Custom = kept
	case "reset":
		lc.Hidden = nil
	case "store":
		repo, ok := parseRepo(req.Repo)
		if req.Repo == "" {
			repo, ok = DefaultStoreRepo, true
		}
		if !ok {
			httpErr(w, 400, "Укажите репозиторий на GitHub: владелец/название")
			return
		}
		lc.StoreRepo, lc.Hidden = repo, nil
	default:
		httpErr(w, 400, "неверный запрос")
		return
	}
	if err := s.saveLocalCatalog(lc); err != nil {
		httpErr(w, 500, "Не удалось сохранить список: "+err.Error())
		return
	}
	resp := map[string]any{"ok": true}
	if req.Action == "store" && !s.isDemo() {
		if c := s.refreshStore(r.Context()); c.Error != "" {
			resp["warning"] = c.Error
		}
	}
	writeJSON(w, resp)
}

func (s *Server) handleLauncherInstall(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	l, ok := s.findLauncher(req.ID)
	if !ok {
		httpErr(w, 400, "Этого приложения нет в списке.")
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
	version := ""
	if demo {
		// The demo TV is offline by design: it gets a stand-in file.
		time.Sleep(700 * time.Millisecond)
		pkg, flag := l.Package, ""
		if pkg == "" {
			pkg = "com.example." + strings.ReplaceAll(l.ID, "-", "")
		}
		if l.Kind == "launcher" {
			flag = ";launcher"
		}
		apk, version = []byte(faketv.FakeAPKPrefix+pkg+flag+"\n"), "демо"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
		defer cancel()
		if apk, version, err = s.downloadAPK(ctx, l, abi); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
	}
	third := func() map[string]bool {
		m := map[string]bool{}
		out, _ := d.Conn.Shell("pm list packages --user 0 -3", 30*time.Second)
		for _, x := range strings.Split(out, "\n") {
			if p, ok := strings.CutPrefix(strings.TrimSpace(x), "package:"); ok {
				m[p] = true
			}
		}
		return m
	}
	before := third()
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
	pkg := ""
	for p := range third() {
		if !before[p] {
			pkg = p
		}
	}
	if pkg == "" && before[l.Package] {
		pkg = l.Package // an update of an app that was already there
	}
	if pkg != "" && pkg != l.Package {
		lc := s.localCatalog()
		lc.Packages[l.ID] = pkg
		s.saveLocalCatalog(lc)
	}
	isHome := false
	if all, _ := d.Launchers(); pkg != "" {
		for _, p := range all {
			isHome = isHome || p == pkg
		}
	}
	s.logHistory("install", []tv.Result{{ID: strings.TrimSpace(l.Name + " " + version), OK: true, Method: "install", Output: "Установлено"}})
	writeJSON(w, map[string]any{"ok": true, "package": pkg, "version": version, "name": l.Name, "launcher": isHome})
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

// handleLauncherStore opens the app's Google Play page on the TV screen,
// for those who prefer to install from the store.
func (s *Server) handleLauncherStore(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	l, ok := s.findLauncher(req.ID)
	if !ok || !l.Store || l.Package == "" {
		httpErr(w, 400, "Этого приложения нет в списке.")
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
