package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testHost = "127.0.0.1:4000"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New("packer.html")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = testHost
	return s
}

func do(s *Server, method, target string, hdr map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = testHost
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := do(s, "GET", "/?t="+s.link, nil, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("exchange: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Error("cookie must be HttpOnly and SameSite=Strict")
			}
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestLinkWorksOnce(t *testing.T) {
	s := newTestServer(t)
	login(t, s)
	if w := do(s, "GET", "/?t="+s.link, nil, nil); w.Code != http.StatusForbidden {
		t.Errorf("reusing the link gave %d, want 403", w.Code)
	}
}

func TestPageNeedsSessionAndHidesLink(t *testing.T) {
	s := newTestServer(t)
	if w := do(s, "GET", "/", nil, nil); w.Code != http.StatusForbidden {
		t.Errorf("no session: %d", w.Code)
	}
	c := login(t, s)
	w := do(s, "GET", "/", nil, c)
	if w.Code != 200 {
		t.Fatalf("page: %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, s.link) || strings.Contains(body, "{{TOKEN}}") || !strings.Contains(body, s.csrf) {
		t.Error("page must carry the header token and never the link token")
	}
	if strings.Contains(body, "<script>") {
		t.Error("inline scripts would break the content security policy")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") ||
		w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("security headers missing")
	}
}

func TestAPINeedsCookieAndHeader(t *testing.T) {
	s := newTestServer(t)
	c := login(t, s)
	hdr := map[string]string{"X-Token": s.csrf}

	cases := []struct {
		name   string
		hdr    map[string]string
		cookie *http.Cookie
		host   string
		want   int
	}{
		{"nothing", nil, nil, testHost, 403},
		{"cookie only", nil, c, testHost, 403},
		{"header only", hdr, nil, testHost, 403},
		{"link token as header", map[string]string{"X-Token": s.link}, c, testHost, 403},
		{"wrong host", hdr, c, "evil.example", 403},
		{"both", hdr, c, testHost, 200},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/api/progress", nil)
		r.Host = tc.host
		for k, v := range tc.hdr {
			r.Header.Set(k, v)
		}
		if tc.cookie != nil {
			r.AddCookie(tc.cookie)
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestStaticIsWhitelisted(t *testing.T) {
	s := newTestServer(t)
	for path, want := range map[string]int{
		"/static/common.js":   200,
		"/static/style.css":   200,
		"/static/packer.js":   200,
		"/static/packer.html": 404,
		"/static/":            404,
	} {
		if w := do(s, "GET", path, nil, nil); w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
	}
}

func TestStaticTraversalNeverServes(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/static/../server.go", "/static/%2e%2e/server.go", "/static//packer.html"} {
		if w := do(s, "GET", path, nil, nil); w.Code == 200 {
			t.Errorf("%s was served", path)
		}
	}
}

func TestQuitRefusedWhileJobRuns(t *testing.T) {
	s := newTestServer(t)
	c := login(t, s)
	s.job.State = "running"
	w := do(s, "GET", "/api/quit", map[string]string{"X-Token": s.csrf}, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("quit during a job: %d, want 400", w.Code)
	}
	select {
	case <-s.quit:
		t.Error("server was told to quit")
	default:
	}
}
