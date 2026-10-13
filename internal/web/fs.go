package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type dirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mod   int64  `json:"mod"`
}

type dirListing struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent"`
	Home    string     `json:"home"`
	Roots   []string   `json:"roots"`
	Sep     string     `json:"sep"`
	Entries []dirEntry `json:"entries"`
}

// Roots returns the top level locations a user can start browsing from.
func Roots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		p := string(c) + `:\`
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) listDir(r *http.Request) (any, error) {
	home, _ := os.UserHomeDir()
	p := r.URL.Query().Get("path")
	if p == "" {
		p = home
	}
	if p == "" {
		p = Roots()[0]
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, errors.New(FriendlyErr(err))
	}
	out := dirListing{Path: p, Home: home, Roots: Roots(), Sep: string(filepath.Separator), Entries: []dirEntry{}}
	if parent := filepath.Dir(p); parent != p {
		out.Parent = parent
	}
	for _, e := range ents {
		de := dirEntry{Name: e.Name(), IsDir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			de.Size, de.Mod = info.Size(), info.ModTime().Unix()
			// Symlinks to folders are shown as folders so they can be entered.
			if info.Mode()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(p, e.Name())); err == nil && st.IsDir() {
					de.IsDir = true
				}
			}
		}
		out.Entries = append(out.Entries, de)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

func (s *Server) makeDir(r *http.Request) (any, error) {
	var req struct{ Path string }
	if err := Decode(r, &req); err != nil {
		return nil, err
	}
	if req.Path == "" {
		return nil, errors.New("no folder name given")
	}
	if err := os.MkdirAll(req.Path, 0o755); err != nil {
		return nil, errors.New(FriendlyErr(err))
	}
	return map[string]string{"path": req.Path}, nil
}

// FriendlyErr trims the operation and path noise from common file errors.
func FriendlyErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error() + ": " + pe.Path
	}
	return err.Error()
}
