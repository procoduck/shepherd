//go:build e2e

package e2e_test

import (
	"io"
	"net/http"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Scenario 9: the embedded SPA actually serves. Every other spec in this suite
// hits /api/*, Connect RPC, or metrics, so without this one an e2e run would
// pass unchanged against an image whose frontend embed is broken or empty.
var _ = Describe("9. Embedded SPA", func() {
	It("serves index.html at / and a referenced /assets/ bundle resolves", func() {
		resp, err := http.Get(shepherdURL + "/")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("text/html"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		assetPath := regexp.MustCompile(`/assets/[^"']+`).FindString(string(body))
		Expect(assetPath).NotTo(BeEmpty(), "index.html must reference at least one /assets/ bundle")

		assetResp, err := http.Get(shepherdURL + assetPath)
		Expect(err).NotTo(HaveOccurred())
		defer assetResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(assetResp.StatusCode).To(Equal(http.StatusOK),
			"asset %s referenced by index.html must resolve", assetPath)
	})
})
