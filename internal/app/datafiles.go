package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// devDir is where per-television files live. The demo TV gets a throw-away
// folder so that it never mixes with real devices.
func (s *Server) devDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := s.DataDir
	if s.demo {
		base = s.demoDir
	}
	id := "unknown"
	if s.info != nil {
		id = s.info.Serial
		if id == "" || id == "unknown" {
			id = s.info.Manufacturer + "_" + s.info.Model
		}
	}
	id = strings.Trim(unsafeName.ReplaceAllString(id, "_"), "._")
	if id == "" {
		id = "unknown"
	}
	return filepath.Join(base, "devices", id)
}

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, v)
	}
}

func writeJSON2(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
