// mockmsft is a minimal mock server for Microsoft Graph API and Azure DevOps,
// used in the e2e suite. It serves:
//   - GET /v1.0/me/transitiveMemberOf/microsoft.graph.group → user group memberships
//   - GET /v1.0/groups?$filter=...                           → group search
//   - GET /{project}/_apis/git/repositories/{repo}/items    → ADO file listing
//   - GET /{project}/_apis/git/repositories/{repo}/commits  → ADO commit
//   - POST /__fixture                                        → control endpoint for tests
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

type groupsStore struct {
	mu     sync.RWMutex
	byUser map[string][]map[string]string // token → []group
	all    []map[string]string
}

type adoStore struct {
	mu    sync.RWMutex
	files map[string]string // path → content
}

var (
	groups = &groupsStore{byUser: make(map[string][]map[string]string)}
	ado    = &adoStore{files: make(map[string]string)}
)

func main() {
	// Health-check mode: just probe the running server and exit.
	if len(os.Args) > 1 && os.Args[1] == "-health-check" {
		resp, err := http.Get("http://localhost:9090/health")
		if err != nil {
			log.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("unhealthy: %d", resp.StatusCode)
		}
		return
	}

	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":9090"
	}

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // http write after header
	})

	// Control endpoint — tests use this to seed state.
	mux.HandleFunc("POST /__fixture", handleFixture)

	// Graph: transitive member groups
	mux.HandleFunc("GET /v1.0/me/transitiveMemberOf/microsoft.graph.group", handleTransitiveMemberOf)
	// Graph: group search
	mux.HandleFunc("GET /v1.0/groups", handleGroupSearch)

	// ADO: list items
	mux.HandleFunc("GET /{project}/_apis/git/repositories/{repo}/items", handleADOItems)
	// ADO: commits
	mux.HandleFunc("GET /{project}/_apis/git/repositories/{repo}/commits", handleADOCommits)

	log.Printf("mockmsft listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

type fixtureRequest struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

func handleFixture(w http.ResponseWriter, r *http.Request) {
	var req fixtureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Kind {
	case "groups":
		gs := toGroupList(req.Data["groups"])
		groups.mu.Lock()
		groups.all = gs
		if user, ok := req.Data["user"].(string); ok {
			groups.byUser[user] = gs
		}
		groups.mu.Unlock()
	case "ado_file":
		path, _ := req.Data["path"].(string)       //nolint:errcheck // fixture helper accepts missing values
		content, _ := req.Data["content"].(string) //nolint:errcheck // fixture helper accepts missing values
		ado.mu.Lock()
		ado.files[path] = content
		ado.mu.Unlock()
	case "ado_delete":
		path, _ := req.Data["path"].(string) //nolint:errcheck // fixture helper accepts missing values
		ado.mu.Lock()
		delete(ado.files, path)
		ado.mu.Unlock()
	default:
		http.Error(w, "unknown fixture kind", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleTransitiveMemberOf(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")

	groups.mu.RLock()
	gs, ok := groups.byUser[token]
	if !ok {
		gs = groups.all
	}
	groups.mu.RUnlock()

	respondJSON(w, map[string]any{"value": gs, "@odata.count": len(gs)})
}

func handleGroupSearch(w http.ResponseWriter, r *http.Request) {
	groups.mu.RLock()
	gs := groups.all
	groups.mu.RUnlock()

	q := r.URL.Query().Get("$filter")
	var filtered []map[string]string
	for _, g := range gs {
		if q == "" || strings.Contains(strings.ToLower(g["displayName"]), strings.ToLower(q)) {
			filtered = append(filtered, g)
		}
	}
	respondJSON(w, map[string]any{"value": filtered})
}

func handleADOItems(w http.ResponseWriter, r *http.Request) {
	ado.mu.RLock()
	files := make([]map[string]any, 0, len(ado.files))
	for path := range ado.files {
		files = append(files, map[string]any{
			"path":        path,
			"isFolder":    false,
			"commitId":    "abc123",
			"contentType": "text/plain",
		})
	}
	ado.mu.RUnlock()

	// Check if downloading a specific file.
	if p := r.URL.Query().Get("path"); p != "" {
		format := r.URL.Query().Get("$format")
		ado.mu.RLock()
		content, ok := ado.files[p]
		ado.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if format == "text" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(content)) //nolint:errcheck // http write
			return
		}
	}

	respondJSON(w, map[string]any{"count": len(files), "value": files})
}

func handleADOCommits(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, map[string]any{
		"count": 1,
		"value": []map[string]any{{"commitId": "abc123def456", "comment": "e2e test commit"}},
	})
}

func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // http write after header
}

func toGroupList(v any) []map[string]string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			g := make(map[string]string)
			for k, val := range m {
				if s, ok := val.(string); ok {
					g[k] = s
				}
			}
			out = append(out, g)
		}
	}
	return out
}
