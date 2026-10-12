// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSitePages(t *testing.T) {
	h := secured(siteHandler(""))
	for target, want := range map[string]struct{ ctype, cache string }{
		"/":                {"text/html; charset=utf-8", "no-cache"},
		"/index.html":      {"text/html; charset=utf-8", "no-cache"},
		"/stats":           {"text/html; charset=utf-8", "no-cache"},
		"/stats.html":      {"text/html; charset=utf-8", "no-cache"},
		"/robots.txt":      {"text/plain; charset=utf-8", "no-cache"},
		"/style.css?v=x":   {"text/css; charset=utf-8", "public, max-age=3600"},
		"/stats.js?v=x":    {"text/javascript; charset=utf-8", "public, max-age=3600"},
		"/favicon.svg?v=x": {"image/svg+xml", "public, max-age=3600"},
	} {
		w := get(t, h, http.MethodGet, target, nil)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("%s: status %d, %d bytes", target, w.Code, w.Body.Len())
			continue
		}
		if got := w.Header().Get("Content-Type"); got != want.ctype {
			t.Errorf("%s: Content-Type %q, want %q", target, got, want.ctype)
		}
		if got := w.Header().Get("Cache-Control"); got != want.cache {
			t.Errorf("%s: Cache-Control %q, want %q", target, got, want.cache)
		}
		etag := w.Header().Get("ETag")
		if etag == "" {
			t.Errorf("%s: no ETag", target)
			continue
		}
		if again := get(t, h, http.MethodGet, target, map[string]string{"If-None-Match": etag}); again.Code != http.StatusNotModified {
			t.Errorf("%s: revalidation answered %d, want 304", target, again.Code)
		}
	}
	if a, b := get(t, h, http.MethodGet, "/", nil).Body.String(), get(t, h, http.MethodGet, "/stats", nil).Body.String(); a == b || !strings.Contains(b, "stats.js") {
		t.Error("/stats does not serve the usage page")
	}
}

func TestSiteRefusals(t *testing.T) {
	h := secured(siteHandler(t.TempDir()))
	for target, want := range map[string]int{
		"/nope":              http.StatusNotFound,
		"/www/index.html":    http.StatusNotFound,
		"/stats/":            http.StatusNotFound,
		"/stats/stats.state": http.StatusNotFound,
		"/stats/nope.json":   http.StatusNotFound,
		"/stats/hours.json":  http.StatusNotFound, // not written yet
	} {
		w := get(t, h, http.MethodGet, target, nil)
		if w.Code != want || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: status %d (%s), want %d with JSON", target, w.Code, w.Header().Get("Content-Type"), want)
		}
	}
	w := get(t, h, http.MethodPost, "/", nil)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /: status %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
	if w := get(t, h, http.MethodHead, "/", nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD /: status %d, %d bytes", w.Code, w.Body.Len())
	}
}

func TestSiteUsage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, statsPublic), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, statsPublic, "minutes.json"), []byte(`{"periods":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, statsPrivate), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := secured(siteHandler(dir))
	w := get(t, h, http.MethodGet, "/stats/minutes.json", nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"periods":[]}` {
		t.Fatalf("minutes.json: status %d, body %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("minutes.json: Cache-Control %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("minutes.json: Content-Type %q", got)
	}
	if w.Header().Get("Last-Modified") == "" {
		t.Error("minutes.json: no Last-Modified")
	}
}

// Every answer carries the security headers, API answers included.
func TestSecuredHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", endpoint([]string{"*"}, handleHealth))
	mux.HandleFunc("/", siteHandler(""))
	h := secured(mux)
	for _, target := range []string{"/", "/stats.js", "/nope", "/health"} {
		w := get(t, h, http.MethodGet, target, map[string]string{"X-Forwarded-For": "198.51.100.77"})
		for k, v := range map[string]string{
			"Strict-Transport-Security": "max-age=31536000",
			"X-Content-Type-Options":    "nosniff",
			"Referrer-Policy":           "no-referrer",
			"Content-Security-Policy":   csp,
		} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("%s: %s %q, want %q", target, k, got, v)
			}
		}
	}
}

func TestHealthCheck(t *testing.T) {
	status := http.StatusOK
	body := `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	for _, c := range []struct {
		status int
		body   string
		want   int
	}{
		{http.StatusOK, `{"ok":true}`, 0},
		{http.StatusTooManyRequests, `{"error":"x"}`, 0},
		{http.StatusOK, `{"ok":false}`, 1},
		{http.StatusOK, `not json`, 1},
		{http.StatusServiceUnavailable, `{"ok":true}`, 1},
	} {
		status, body = c.status, c.body
		if got := healthCheck(port); got != c.want {
			t.Errorf("status %d, body %s: exit %d, want %d", c.status, c.body, got, c.want)
		}
	}
	srv.Close()
	if healthCheck(port) != 1 {
		t.Error("no instance: exit 0")
	}
}
