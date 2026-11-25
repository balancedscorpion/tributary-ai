package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAFileServerFallsBackOnlyForClientRoutes(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("spa shell")},
		"assets/app.js":  &fstest.MapFile{Data: []byte("console.log('ok')")},
		"favicon.svg":    &fstest.MapFile{Data: []byte("<svg/>")},
		"nested/keep.js": &fstest.MapFile{Data: []byte("nested")},
	}
	h := SPAFileServer(fs.FS(dist))

	t.Run("client route falls back to index", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/threads/abc", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "spa shell" {
			t.Fatalf("body = %q, want index shell", rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate, max-age=0" {
			t.Fatalf("Cache-Control = %q, want no-store shell", got)
		}
	})

	t.Run("root shell is never cached across desktop upgrades", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?desktop_build=next", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate, max-age=0" {
			t.Fatalf("Cache-Control = %q, want no-store shell", got)
		}
	})

	t.Run("content-addressed assets remain immutable", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control = %q, want immutable asset", got)
		}
	})

	t.Run("files route falls back to index", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "spa shell" {
			t.Fatalf("body = %q, want index shell", rec.Body.String())
		}
	})

	t.Run("missing hashed asset does not fall back", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing-worker.mjs", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if rec.Body.String() == "spa shell" {
			t.Fatalf("missing asset should not serve index shell")
		}
	})

	t.Run("missing extension path does not fall back", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon-legacy.svg", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}
