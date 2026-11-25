package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func stripLeadingSlash(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	return p
}

// SPAFileServer serves static assets and falls back to /index.html for client-side routes.
func SPAFileServer(dist fs.FS) http.Handler {
	fsHandler := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		reqPath := r.URL.Path
		if reqPath == "" || reqPath == "/" {
			setSPACacheHeaders(w, "index.html", true)
			// Let the file server handle directory index resolution. Avoid rewriting to
			// "/index.html" because http.FileServer redirects "/index.html" -> "./".
			fsHandler.ServeHTTP(w, r)
			return
		}

		clean := path.Clean(reqPath)
		rel := stripLeadingSlash(clean)
		if rel == "." || rel == "" {
			rel = "index.html"
		}

		// If the requested file exists, serve it; otherwise fall back to index.html.
		f, err := dist.Open(rel)
		if err == nil {
			_ = f.Close()
			setSPACacheHeaders(w, rel, rel == "index.html")
			fsHandler.ServeHTTP(w, r)
			return
		}
		// Hashed build assets must fail as missing assets, not fall back to the
		// SPA shell. Otherwise a stale page can load index.html as JS/worker code.
		if strings.HasPrefix(rel, "assets/") || path.Ext(rel) != "" {
			http.NotFound(w, r)
			return
		}

		r2 := r.Clone(r.Context())
		setSPACacheHeaders(w, "index.html", true)
		// Serve the SPA entrypoint via directory index resolution.
		r2.URL.Path = "/"
		fsHandler.ServeHTTP(w, r2)
	})
}

func setSPACacheHeaders(w http.ResponseWriter, rel string, shell bool) {
	if shell || !strings.HasPrefix(rel, "assets/") {
		// The desktop WebView keeps the localhost origin across application
		// upgrades. Never let an old index.html (or another unhashed file) pin an
		// obsolete frontend after a new embedded backend is installed.
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		return
	}
	// Vite assets are content-addressed. They are safe to retain and a changed
	// shell will reference a different filename.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}
