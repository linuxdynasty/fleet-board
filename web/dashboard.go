package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// contentTypes sets the Content-Type for the extensions Vite emits, so the
// page does not depend on the machine's MIME table. Anything else falls
// back to sniffing in serveBytes.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".webmanifest": "application/manifest+json",
}

// Dist returns the embedded dashboard files, rooted at the build output so
// Handler sees index.html and assets/ at the top.
func Dist() (fs.FS, error) {
	content, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: embedded dashboard: %w", err)
	}
	return content, nil
}

// Handler serves the dashboard tree with the single-page-app fallback the
// client router needs (04-technology-review, "UI build and serve"): an exact
// file when one exists, else index.html. Hashed assets under assets/ are
// immutable; the entry page is never cached.
func Handler(content fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		body, isIndex := lookup(content, name)
		if isIndex {
			name = "index.html"
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		serveBytes(w, name, body)
	})
}

// lookup returns the file for name, or the entry page when name names
// nothing servable. The second result reports the entry page.
func lookup(content fs.FS, name string) ([]byte, bool) {
	if name != "" && name != "index.html" {
		if body, err := fs.ReadFile(content, name); err == nil {
			return body, false
		}
	}
	body, err := fs.ReadFile(content, "index.html")
	if err != nil {
		return []byte("fleet-board dashboard unavailable"), true
	}
	return body, true
}

func serveBytes(w http.ResponseWriter, name string, body []byte) {
	if ctype, ok := contentTypes[path.Ext(name)]; ok {
		w.Header().Set("Content-Type", ctype)
	}
	// A failed client write has no recourse; the status line is already sent.
	_, _ = w.Write(body) //nolint:gosec // G705: the bytes are fleetd's own embedded build output (or the unavailable fallback); the request path only selects among embedded files
}
