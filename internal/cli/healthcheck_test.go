package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shepherd healthcheck", func() {
	var server *httptest.Server
	BeforeEach(func() {
		// cobra keeps flag values between Execute calls; reset --path so the
		// default-path spec cannot inherit another spec's value.
		hc, _, err := rootCmd.Find([]string{"healthcheck"})
		Expect(err).NotTo(HaveOccurred())
		Expect(hc.Flags().Set("path", "/healthz")).To(Succeed())
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/-/ready" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
	})
	AfterEach(func() { server.Close() })

	run := func(args ...string) error {
		rootCmd.SetArgs(append([]string{"healthcheck", "--addr", strings.TrimPrefix(server.URL, "http://")}, args...))
		return rootCmd.Execute()
	}

	It("checks /healthz by default", func() {
		Expect(run()).To(Succeed())
	})
	It("checks the path given with --path", func() {
		Expect(run("--path", "/-/ready")).To(Succeed())
		Expect(run("--path", "/not-ready")).To(MatchError(ContainSubstring("status 503")))
	})
})
