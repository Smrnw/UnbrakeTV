package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	repo         = "Smrnw/UnbrakeTV"
	releasesPage = "https://github.com/" + repo + "/releases/latest"
)

// githubAPI is a variable so that tests can point it at a local server.
var githubAPI = "https://api.github.com"

// UpdateInfo is the result of the last update check.
type UpdateInfo struct {
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Notes     string `json:"notes"`
	Newer     bool   `json:"newer"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error"`
}

type settings struct {
	// UpdateCheck allows one request to GitHub at start-up to learn about a new version.
	UpdateCheck bool `json:"updateCheck"`
}

func (s *Server) settingsPath() string { return filepath.Join(s.DataDir, "settings.json") }

func (s *Server) loadSettings() settings {
	st := settings{UpdateCheck: true}
	if b, err := os.ReadFile(s.settingsPath()); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// parseVersion turns "v1.2.3" into [1 2 3]; ok is false for "dev" and the like.
func parseVersion(v string) (out []int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// newerVersion reports whether latest is a higher version than current.
func newerVersion(latest, current string) bool {
	a, ok1 := parseVersion(latest)
	b, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	Tag    string    `json:"tag_name"`
	Page   string    `json:"html_url"`
	Body   string    `json:"body"`
	Assets []ghAsset `json:"assets"`
}

// latestRelease asks GitHub for the latest release of a repository.
// The request carries nothing but the program name and version.
func (s *Server) latestRelease(ctx context.Context, repo string) (ghRelease, error) {
	var rel ghRelease
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPI+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "UnbrakeTV/"+s.Version)
	resp, err := s.http.Do(req)
	if err != nil {
		return rel, errors.New("нет связи с GitHub — проверьте интернет на компьютере")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return rel, errors.New("GitHub временно ограничил запросы с вашего адреса — попробуйте позже")
	}
	if resp.StatusCode != http.StatusOK {
		return rel, fmt.Errorf("GitHub ответил ошибкой %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil || rel.Tag == "" {
		return rel, errors.New("непонятный ответ GitHub")
	}
	return rel, nil
}

// CheckUpdate looks for a newer release of the program.
func (s *Server) CheckUpdate(ctx context.Context) UpdateInfo {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := UpdateInfo{CheckedAt: time.Now().Format("2006-01-02 15:04"), URL: releasesPage}
	rel, err := s.latestRelease(ctx, repo)
	if err != nil {
		u.Error = err.Error()
	} else {
		u.Latest = strings.TrimPrefix(rel.Tag, "v")
		u.Newer = newerVersion(rel.Tag, s.Version)
		if strings.HasPrefix(rel.Page, "https://github.com/"+repo+"/") {
			u.URL = rel.Page
		}
		u.Notes = rel.Body
		if r := []rune(u.Notes); len(r) > 1500 {
			u.Notes = string(r[:1500]) + "…"
		}
	}
	s.mu.Lock()
	s.update = u
	s.mu.Unlock()
	for _, f := range afterUpdateCheck {
		f(s, ctx)
	}
	return u
}

// KnowledgeInfo describes the downloaded part of the knowledge base.
type KnowledgeInfo struct {
	Version int `json:"version"`
	Rules   int `json:"rules"`
}

// AutoCheckUpdate runs the start-up check if the user has not turned it off.
// Development builds have no version to compare and never check.
func (s *Server) AutoCheckUpdate() {
	if _, ok := parseVersion(s.Version); !ok {
		return
	}
	s.mu.Lock()
	on := s.settings.UpdateCheck
	s.mu.Unlock()
	if on {
		go s.CheckUpdate(context.Background())
	}
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.CheckUpdate(r.Context()))
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UpdateCheck *bool `json:"updateCheck"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "неверный запрос")
		return
	}
	s.mu.Lock()
	if req.UpdateCheck != nil {
		s.settings.UpdateCheck = *req.UpdateCheck
	}
	st := s.settings
	s.mu.Unlock()
	if b, err := json.MarshalIndent(st, "", "  "); err == nil {
		os.WriteFile(s.settingsPath(), b, 0o600)
	}
	writeJSON(w, st)
}
