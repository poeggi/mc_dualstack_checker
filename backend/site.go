// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The API host's own pages: the landing page, the usage page and their
// files are built into the binary from www/, so one image holds the whole
// service. The usage numbers come from STATE_DIRECTORY/stats/.

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed www
var wwwFS embed.FS

// The pages load only this host's own files.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// siteTypes are the content types of the files in www/, the same on every
// system.
var siteTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".txt":  "text/plain; charset=utf-8",
}

type siteFile struct {
	data  []byte
	etag  string
	ctype string
}

// siteFiles maps request paths to the files of www/.
var siteFiles = func() map[string]siteFile {
	files := map[string]siteFile{}
	entries, err := wwwFS.ReadDir("www")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(wwwFS, "www/"+e.Name())
		if err != nil {
			panic(err)
		}
		ctype, ok := siteTypes[path.Ext(e.Name())]
		if !ok {
			panic("www/" + e.Name() + ": no content type for its extension")
		}
		sum := sha256.Sum256(data)
		files["/"+e.Name()] = siteFile{
			data:  data,
			etag:  `"` + hex.EncodeToString(sum[:8]) + `"`,
			ctype: ctype,
		}
	}
	files["/"] = files["/index.html"]
	files["/stats"] = files["/stats.html"]
	return files
}()

// secured sets the headers every answer carries.
func secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// siteHandler serves the pages and the usage numbers. Browsers revalidate
// the pages on every load. Scripts, styles and icons are cached for an
// hour: the pages name them with the release as a version stamp, so a new
// release is a new URL. Usage files change once a minute.
func siteHandler(statsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/stats/"); ok {
			serveUsage(w, r, statsDir, name)
			return
		}
		f, ok := siteFiles[r.URL.Path]
		if !ok {
			httpError(w, http.StatusNotFound, "unknown endpoint")
			return
		}
		h := w.Header()
		switch path.Ext(r.URL.Path) {
		case ".css", ".js", ".svg", ".png":
			h.Set("Cache-Control", "public, max-age=3600")
		default:
			h.Set("Cache-Control", "no-cache")
		}
		h.Set("Content-Type", f.ctype)
		h.Set("ETag", f.etag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
	}
}

// serveUsage serves one published usage file, see stats.go.
func serveUsage(w http.ResponseWriter, r *http.Request, statsDir, name string) {
	known := false
	for _, k := range periodKinds {
		known = known || name == k.name+".json"
	}
	if !known || statsDir == "" {
		httpError(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	f, err := os.Open(filepath.Join(statsDir, statsPublic, name))
	if err != nil {
		httpError(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "usage numbers unreadable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Type", "application/json")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
