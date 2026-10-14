package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := NewApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.token = "secret-token-0123456789abcdef0123"
	return a
}

func TestServerGuards(t *testing.T) {
	a := testApp(t)
	h := a.routes()
	do := func(method, path, host string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	tok := map[string]string{"X-Frog-Token": a.token}

	if c := do("GET", "/api/status", "evil.com", tok).Code; c != http.StatusForbidden {
		t.Errorf("foreign host: %d", c)
	}
	if c := do("GET", "/api/status", "127.0.0.1:47621", nil).Code; c != http.StatusUnauthorized {
		t.Errorf("no token: %d", c)
	}
	if c := do("GET", "/api/status", "127.0.0.1:47621", map[string]string{"X-Frog-Token": "nope"}).Code; c != http.StatusUnauthorized {
		t.Errorf("bad token: %d", c)
	}
	if c := do("GET", "/api/status", "localhost:47621", tok).Code; c != http.StatusOK {
		t.Errorf("good token: %d", c)
	}
	if c := do("GET", "/api/status", "127.0.0.1:47621", map[string]string{"Cookie": cookieName + "=" + a.token}).Code; c != http.StatusOK {
		t.Errorf("cookie: %d", c)
	}
	cross := map[string]string{"Cookie": cookieName + "=" + a.token, "Origin": "https://evil.com"}
	if c := do("POST", "/api/apply", "127.0.0.1:47621", cross).Code; c != http.StatusForbidden {
		t.Errorf("cross-origin POST: %d", c)
	}

	// Opening /?t=<token> sets the cookie and redirects.
	w := do("GET", "/?t="+a.token, "127.0.0.1:47621", nil)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Set-Cookie"), a.token) {
		t.Errorf("token link: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	w = do("GET", "/?t=wrong", "127.0.0.1:47621", nil)
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("cookie set for wrong token")
	}
	w = do("GET", "/", "127.0.0.1:47621", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Frogtech Mod Loader") {
		t.Errorf("index: %d", w.Code)
	}
}

func TestIconICO(t *testing.T) {
	ico := iconICO()
	if len(ico) < 100 || ico[2] != 1 || ico[4] != 3 {
		t.Fatalf("bad ico header % x", ico[:6])
	}
}

func TestFontsServed(t *testing.T) {
	a := testApp(t)
	h := a.routes()
	for path, want := range map[string]int{"/fonts/Jost-latin.woff2": 200, "/fonts/BigShouldersDisplay-latin.woff2": 404, "/fonts/OFL-Jost.txt": 404, "/fonts/../index.html": 404} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:47621"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want && !(want == 404 && w.Code >= 300) {
			t.Errorf("%s: %d", path, w.Code)
		}
		if want == 200 && (w.Header().Get("Content-Type") != "font/woff2" || w.Body.Len() < 10000) {
			t.Errorf("%s: %s %d bytes", path, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
}
