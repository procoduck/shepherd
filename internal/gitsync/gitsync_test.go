package gitsync_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/ado"
)

func TestGitsync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gitsync Suite")
}

// mockADOServer builds a minimal ADO mock with configurable responses.
type mockADOServer struct {
	commits map[string]string // branch → commitID
	files   map[string]string // path → content
}

func newMockADO() *mockADOServer {
	return &mockADOServer{
		commits: make(map[string]string),
		files:   make(map[string]string),
	}
}

func (m *mockADOServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/proj/_apis/git/repositories/repo/commits", func(w http.ResponseWriter, r *http.Request) {
		branch := r.URL.Query().Get("searchCriteria.itemVersion.version")
		sha := m.commits[branch]
		if sha == "" {
			sha = "abc123"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
			"count": 1, "value": []map[string]any{{"commitId": sha}},
		})
	})
	mux.HandleFunc("/proj/_apis/git/repositories/repo/items", func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("$format")
		path := r.URL.Query().Get("path")
		if format == "text" {
			content, ok := m.files[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(content)) //nolint:errcheck // test
			return
		}
		// List files
		var items []map[string]any
		for p := range m.files {
			items = append(items, map[string]any{"path": p, "isFolder": false, "commitId": "abc123"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"count": len(items), "value": items}) //nolint:errcheck // test
	})
	return mux
}

var _ = Describe("ADO client integration via mock server", func() {
	var server *httptest.Server
	var mock *mockADOServer
	var client *ado.Client

	BeforeEach(func() {
		mock = newMockADO()
		mock.commits["main"] = "commit-abc"
		mock.files["/pipelines/myservice.alloy"] = "// valid alloy"

		server = httptest.NewServer(mock.Handler())
		client = ado.NewWithHTTPClient(server.URL, server.URL, server.Client())
	})

	AfterEach(func() {
		server.Close()
	})

	It("no-change fast path: same commit returns early", func() {
		sha, err := client.GetLatestCommit(context.Background(), "proj", "repo", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("commit-abc"))

		// Second call returns same SHA — caller should skip sync.
		sha2, err := client.GetLatestCommit(context.Background(), "proj", "repo", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha2).To(Equal(sha))
	})

	It("list files returns .alloy files under path", func() {
		files, err := client.ListFiles(context.Background(), "proj", "repo", "main", "/pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(1))
		Expect(files[0].Path).To(Equal("/pipelines/myservice.alloy"))
	})

	It("add file is visible in next list", func() {
		mock.files["/pipelines/newservice.alloy"] = "// new"
		files, err := client.ListFiles(context.Background(), "proj", "repo", "main", "/pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(2))
	})

	It("delete file is absent in next list", func() {
		delete(mock.files, "/pipelines/myservice.alloy")
		files, err := client.ListFiles(context.Background(), "proj", "repo", "main", "/pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(BeEmpty())
	})
})
