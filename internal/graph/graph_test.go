package graph_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/graph"
)

func TestGraph(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Graph Client Suite")
}

var _ = Describe("TransitiveMemberOf", func() {
	It("follows @odata.nextLink pagination", func() {
		page := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if page == 0 {
				page++
				nextURL := "http://" + r.Host + r.URL.Path + "?$skiptoken=abc"
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
					"value":           []map[string]any{{"id": "group-1"}},
					"@odata.nextLink": nextURL,
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
					"value": []map[string]any{{"id": "group-2"}},
				})
			}
		}))
		defer server.Close()

		ids, err := graph.TransitiveMemberOf(GinkgoT().Context(), server.URL, "test-token")
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ContainElements("group-1", "group-2"))
		Expect(page).To(Equal(1), "should have followed the nextLink exactly once")
	})

	It("error propagation on non-200 response", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := graph.TransitiveMemberOf(GinkgoT().Context(), server.URL, "bad-token")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("401"))
	})

	It("returns all group IDs from overage-sized list", func() {
		// Simulate 200+ groups by returning two pages of 100 each.
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			groups := make([]map[string]any, 100)
			for i := range groups {
				groups[i] = map[string]any{"id": strings.Repeat("a", i%10) + "group"}
			}
			resp := map[string]any{"value": groups}
			if calls == 1 {
				resp["@odata.nextLink"] = "http://" + r.Host + r.URL.Path + "?page=2"
			}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck // test
		}))
		defer server.Close()

		ids, err := graph.TransitiveMemberOf(GinkgoT().Context(), server.URL, "token")
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(HaveLen(200))
		Expect(calls).To(Equal(2))
	})
})
