// Package web serves the local browser interface shared by the packer and
// the unpacker. It only listens on the loopback interface and every API call
// has to present a random token that is handed to the browser at startup.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/archive"
)

//go:embed static
var staticFiles embed.FS

// Server is a one shot local web application.
type Server struct {
	token string
	addr  string
	mux   *http.ServeMux
	page  string
	quit  chan struct{}
	once  sync.Once

	jobMu sync.Mutex
	job   jobState
}

type jobState struct {
	State    string           `json:"state"` // idle, running, done, error
	Progress archive.Progress `json:"progress"`
	Message  string           `json:"message"`
	Warnings []string         `json:"warnings"`
}

// New creates a server that shows the named page (for example "packer.html").
func New(page string) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	s := &Server{
		token: hex.EncodeToString(tok),
		mux:   http.NewServeMux(),
		page:  page,
		quit:  make(chan struct{}),
		job:   jobState{State: "idle"},
	}
	sub, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	s.mux.HandleFunc("/", s.servePage)
	s.API("/api/fs", s.listDir)
	s.API("/api/mkdir", s.makeDir)
	s.API("/api/progress", func(*http.Request) (any, error) {
		s.jobMu.Lock()
		defer s.jobMu.Unlock()
		return s.job, nil
	})
	s.API("/api/quit", func(*http.Request) (any, error) {
		s.once.Do(func() { close(s.quit) })
		return map[string]bool{"ok": true}, nil
	})
	return s, nil
}

// API registers a JSON endpoint protected by the session token.
func (s *Server) API(path string, fn func(r *http.Request) (any, error)) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		out, err := fn(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(out)
	})
}

// Decode reads a JSON request body.
func Decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	return nil
}

// hostOK rejects requests whose Host header is not our own address, which
// blocks DNS rebinding from other websites.
func (s *Server) hostOK(r *http.Request) bool { return r.Host == s.addr }

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || !s.hostOK(r) ||
		subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	raw, err := staticFiles.ReadFile("static/" + s.page)
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(strings.ReplaceAll(string(raw), "{{TOKEN}}", s.token)))
}

// StartJob runs fn in the background and exposes its progress. Only one job
// may run at a time.
func (s *Server) StartJob(fn func(update func(archive.Progress)) (string, []string, error)) error {
	s.jobMu.Lock()
	if s.job.State == "running" {
		s.jobMu.Unlock()
		return errors.New("another job is still running")
	}
	s.job = jobState{State: "running"}
	s.jobMu.Unlock()

	go func() {
		var last time.Time
		update := func(p archive.Progress) {
			if time.Since(last) < 80*time.Millisecond && p.Current != "" {
				return
			}
			last = time.Now()
			s.jobMu.Lock()
			s.job.Progress = p
			s.jobMu.Unlock()
		}
		msg, warns, err := fn(update)
		s.jobMu.Lock()
		defer s.jobMu.Unlock()
		s.job.Warnings = warns
		if err != nil {
			s.job.State, s.job.Message = "error", err.Error()
			return
		}
		s.job.State, s.job.Message = "done", msg
	}()
	return nil
}

// Run listens on a random loopback port, opens the browser and blocks until
// the user quits from the page or ctx ends.
func (s *Server) Run(ctx context.Context, noBrowser bool) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)

	url := fmt.Sprintf("http://%s/?t=%s", s.addr, s.token)
	fmt.Println("Open this address in your browser if it does not open by itself:")
	fmt.Println("  " + url)
	if !noBrowser {
		openBrowser(url)
	}

	select {
	case <-s.quit:
	case <-ctx.Done():
	}
	sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(sc)
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
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
