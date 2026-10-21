// Package webui serves the web UI, embedded in the binary (ADR-0012), under a strict
// Content-Security-Policy (ADR-0039). The bundle is built from web/ by `make web`,
// which copies it into dist/; a binary built without it serves a page saying so.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Prefix is where the UI lives; everything else on the listener is the API's.
const Prefix = "/ui/"

// csp allows the bundle's own scripts, styles, and API calls, and nothing else.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const notBuilt = `<!doctype html><title>Bohux</title><p>This binary was built without the web UI.
Build it with <code>make web</code>, or use the CLI.</p>`

// Handler serves the UI under Prefix: files as they are, and index.html for every
// other path, so the SPA's routes load on refresh.
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		index = []byte(notBuilt)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), strings.TrimSuffix(Prefix, "/"))
		name = strings.TrimPrefix(name, "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(dist, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite names assets by content hash: safe to cache for good.
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + name
				files.ServeHTTP(w, r2)
				return
			}
		}
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// Mount serves the UI under Prefix and sends "/" there.
func Mount(mux *http.ServeMux) {
	mux.Handle(Prefix, Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Prefix, http.StatusFound)
	})
}
