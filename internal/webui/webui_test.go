package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	for _, path := range []string{"/ui/", "/ui/p/shop/e/prod", "/ui/nodes"} {
		rec := get(path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Bohux</title>") {
			t.Errorf("%s: %d %.80q; want the SPA's index", path, rec.Code, rec.Body.String())
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") ||
			!strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q", path, csp)
		}
	}
	if rec := get("/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != Prefix {
		t.Errorf("/: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /ui/: %d", rec.Code)
	}
}
