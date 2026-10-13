// Package web serves the local browser interface shared by the packer and
// the unpacker. It only listens on the loopback interface.
//
// Access works in two steps. The address printed at startup carries a one
// time link token. The first request that presents it is exchanged for a
// session cookie and redirected to a clean address, and the link stops
// working. API calls then need both that cookie and a second token sent in a
// header, so another local user who sees the address in a process list, or a
// web page in the same browser, gets nothing.
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
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ouroboros/internal/archive"
)

//go:embed static
var staticFiles embed.FS

// Server is a one shot local web application.
type Server struct {
	link   string // one time token in the printed address
	sid    string // session cookie value, set after the link is used
	csrf   string // header token for API calls
	addr   string
	mux    *http.ServeMux
	page   string
	quit   chan struct{}
	once   sync.Once
	active atomic.Int64 // unix time of the last authenticated request

	authMu   sync.Mutex
	linkUsed bool

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
	tok := func() string {
		b := make([]byte, 16)
		rand.Read(b)
		return hex.EncodeToString(b)
	}
	s := &Server{
		link: tok(), sid: tok(), csrf: tok(),
		mux:  http.NewServeMux(),
		page: page,
		quit: make(chan struct{}),
		job:  jobState{State: "idle"},
	}
	s.active.Store(time.Now().Unix())
	s.mux.HandleFunc("/static/", s.serveStatic)
	s.mux.HandleFunc("/", s.servePage)
	s.API("/api/fs", s.listDir)
	s.API("/api/mkdir", s.makeDir)
	s.API("/api/progress", func(*http.Request) (any, error) {
		s.jobMu.Lock()
		defer s.jobMu.Unlock()
		return s.job, nil
	})
	s.API("/api/quit", func(*http.Request) (any, error) {
		s.jobMu.Lock()
		running := s.job.State == "running"
		s.jobMu.Unlock()
		if running {
			return nil, errors.New("a job is still running, wait for it to finish")
		}
		s.once.Do(func() { close(s.quit) })
		return map[string]bool{"ok": true}, nil
	})
	return s, nil
}

// authed reports whether the request carries the session cookie.
func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.sid)) == 1
}

const cookieName = "ouroboros_session"

// idleLimit is how long the program keeps running with nobody using it.
const idleLimit = 30 * time.Minute

// API registers a JSON endpoint protected by the session cookie and header token.
func (s *Server) API(path string, fn func(r *http.Request) (any, error)) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		s.headers(w)
		if !s.hostOK(r) || !s.authed(r) ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.csrf)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.active.Store(time.Now().Unix())
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

// headers sets the protections every response carries. The pages have no
// inline scripts or styles and load nothing from other sites, so the policy
// can be strict.
func (s *Server) headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
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

// staticAssets are the only files served under /static/. The pages themselves
// are only handed out by servePage.
var staticAssets = map[string]string{
	"common.js":   "text/javascript; charset=utf-8",
	"packer.js":   "text/javascript; charset=utf-8",
	"unpacker.js": "text/javascript; charset=utf-8",
	"style.css":   "text/css; charset=utf-8",
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	s.headers(w)
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	ctype, ok := staticAssets[name]
	if !ok || !s.hostOK(r) {
		http.NotFound(w, r)
		return
	}
	raw, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	s.headers(w)
	if !s.hostOK(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// First visit: trade the one time link for a session and drop the token
	// from the address bar and history.
	if t := r.URL.Query().Get("t"); t != "" {
		s.authMu.Lock()
		ok := !s.linkUsed && subtle.ConstantTimeCompare([]byte(t), []byte(s.link)) == 1
		if ok {
			s.linkUsed = true
		}
		s.authMu.Unlock()
		if !ok {
			http.Error(w, "This link has already been used or is not valid. Restart the program to get a new one.", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: s.sid, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	raw, err := staticFiles.ReadFile("static/" + s.page)
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	s.active.Store(time.Now().Unix())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(strings.ReplaceAll(string(raw), "{{TOKEN}}", s.csrf)))
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
// the user quits from the page, the program has been idle for too long, or
// ctx ends.
func (s *Server) Run(ctx context.Context, noBrowser bool) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go srv.Serve(ln)

	url := fmt.Sprintf("http://%s/?t=%s", s.addr, s.link)
	fmt.Println("Open this address in your browser if it does not open by itself.")
	fmt.Println("It works once, then the page switches to a private session:")
	fmt.Println("  " + url)
	if !noBrowser {
		openBrowser(url)
	}

	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
loop:
	for {
		select {
		case <-s.quit:
			break loop
		case <-ctx.Done():
			break loop
		case <-tick.C:
			s.jobMu.Lock()
			running := s.job.State == "running"
			s.jobMu.Unlock()
			if !running && time.Since(time.Unix(s.active.Load(), 0)) > idleLimit {
				fmt.Println("Closing after 30 minutes without use.")
				break loop
			}
		}
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
