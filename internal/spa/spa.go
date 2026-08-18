// Package spa embeds the compiled React SPA and serves it.
package spa

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var distFS embed.FS

// BuildInfo holds metadata about the embedded SPA build.
type BuildInfo struct {
	GitSha  string `json:"git_sha"`
	BuiltAt string `json:"built_at"`
	Dirty   bool   `json:"dirty"`
}

// ParseBuildInfo reads BUILD_INFO.json from the embedded SPA dist.
func ParseBuildInfo() (BuildInfo, error) {
	data, err := distFS.ReadFile("dist/BUILD_INFO.json")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return BuildInfo{GitSha: "dev"}, nil
		}
		return BuildInfo{GitSha: "dev"}, fmt.Errorf("reading BUILD_INFO.json: %w", err)
	}
	var info BuildInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return BuildInfo{GitSha: "dev"}, fmt.Errorf("parsing BUILD_INFO.json: %w", err)
	}
	return info, nil
}

// Handler returns an http.Handler that serves the SPA.
// All non-asset paths serve index.html (client-side routing).
func Handler() http.Handler {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if len(path) > 0 && path[0] == '/' {
			path = path[1:]
		}
		if path == "" {
			path = "index.html"
		}

		// Vite hashes asset filenames; serve them with long-lived immutable cache.
		// Unknown asset paths return 404 instead of falling through to index.html.
		if strings.HasPrefix(path, "assets/") {
			f, err := dist.Open(path)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, "{\"error\":{\"code\":\"not_found\"}}", http.StatusNotFound)
				return
			}
			_ = f.Close() //nolint:errcheck
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fileServer.ServeHTTP(w, r)
			return
		}

		f, err := dist.Open(path)
		if err != nil {
			// Client-side route: serve index.html with no-cache.
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		_ = f.Close() //nolint:errcheck

		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "max-age=3600")
		}
		fileServer.ServeHTTP(w, r)
	})
}
