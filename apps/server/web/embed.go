// Package web embeds the built dashboard (web/dist) into the server binary.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FS returns the dashboard files rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The embed directive guarantees dist exists; this is a build-time invariant.
		panic(err)
	}
	return sub
}

// Handler serves dashboard assets. Unknown non-asset paths fall back to index.html so client-side
// routes work. Callers must route /api/ and /sdk/ before reaching this handler.
func Handler() http.Handler {
	files := FS()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if !isFile(files, name) {
			// SPA fallback: client routes resolve to index.html.
			if strings.Contains(path.Base(name), ".") {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		f, err := files.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if rs, ok := f.(io.ReadSeeker); ok {
			http.ServeContent(w, r, name, st.ModTime(), rs)
			return
		}
		w.Header().Set("Content-Type", contentType(name))
		_, _ = io.Copy(w, f)
	})
}

func isFile(fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && !st.IsDir()
}

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}
