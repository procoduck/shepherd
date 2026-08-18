package ado_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/ado"
)

func TestADO(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ADO Client Suite")
}

var _ = Describe("ADO Client", func() {
	var server *httptest.Server
	var client *ado.Client

	BeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case "/myproject/_apis/git/repositories/myrepo/commits":
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
					"count": 1,
					"value": []map[string]any{{"commitId": "abc123def456"}},
				})

			case "/myproject/_apis/git/repositories/myrepo/items":
				format := r.URL.Query().Get("$format")
				if format == "text" {
					w.Header().Set("Content-Type", "text/plain")
					w.Write([]byte("// alloy content")) //nolint:errcheck // test
					return
				}
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
					"count": 1,
					"value": []map[string]any{
						{"path": "/pipelines/test.alloy", "isFolder": false, "commitId": "abc123"},
					},
				})

			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		// Use NewWithHTTPClient to bypass client-credentials token fetch.
		client = ado.NewWithHTTPClient(server.URL, server.URL, server.Client())
	})

	AfterEach(func() {
		server.Close()
	})

	It("GetLatestCommit returns commit SHA", func() {
		sha, err := client.GetLatestCommit(GinkgoT().Context(), "myproject", "myrepo", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("abc123def456"))
	})

	It("ListFiles returns .alloy files", func() {
		files, err := client.ListFiles(GinkgoT().Context(), "myproject", "myrepo", "main", "/pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(1))
		Expect(files[0].Path).To(Equal("/pipelines/test.alloy"))
	})

	It("DownloadFile returns file content", func() {
		content, err := client.DownloadFile(GinkgoT().Context(), "myproject", "myrepo", "main", "/pipelines/test.alloy")
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// alloy content"))
	})
})
