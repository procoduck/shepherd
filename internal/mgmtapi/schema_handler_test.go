package mgmtapi_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// #279: the component schema artifact is the visual builder's biggest
// download (~680 KB raw, ~50 KB gzipped). It is immutable per version, so the
// handler serves it compressed per Accept-Encoding, keeps the encoded bytes per
// version instead of re-encoding on every request, and answers a matching
// If-None-Match with 304.
var _ = Describe("Schema endpoint compression and conditional GET (#279)", func() {
	var router http.Handler

	BeforeEach(func() {
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		h := mgmtapi.NewSchemaHandler(reg)
		r := chi.NewRouter()
		r.Get("/schema/current", h.GetCurrent)
		r.Get("/schema/{version}", h.Get)
		router = r
	})

	get := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	pinned := "/schema/" + version.AlloySchemaVersion

	for _, path := range []string{pinned, "/schema/current"} {
		It("serves an uncompressed body with Content-Length when the client does not accept gzip: "+path, func() {
			rec := get(path, nil)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Header().Get("Content-Encoding")).To(BeEmpty())
			Expect(rec.Header().Get("Vary")).To(ContainSubstring("Accept-Encoding"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
			var doc map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &doc)).To(Succeed())
			Expect(doc).To(HaveKey("components"))
		})

		It("serves a gzip body that decodes to the same JSON when the client accepts gzip: "+path, func() {
			plain := get(path, nil)
			rec := get(path, map[string]string{"Accept-Encoding": "br, gzip, deflate"})
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Header().Get("Content-Encoding")).To(Equal("gzip"))
			Expect(rec.Header().Get("Vary")).To(ContainSubstring("Accept-Encoding"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
			Expect(rec.Body.Len()).To(BeNumerically("<", plain.Body.Len()/4), "gzip must actually shrink the artifact")

			zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
			Expect(err).NotTo(HaveOccurred())
			decoded, err := io.ReadAll(zr)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded).To(Equal(plain.Body.Bytes()))
		})

		It("gives each representation its own strong ETag and answers If-None-Match with 304: "+path, func() {
			plain := get(path, nil)
			gz := get(path, map[string]string{"Accept-Encoding": "gzip"})
			plainTag, gzTag := plain.Header().Get("ETag"), gz.Header().Get("ETag")
			Expect(plainTag).To(HavePrefix(`"`), "strong ETag, not W/")
			Expect(gzTag).To(HavePrefix(`"`), "strong ETag, not W/")
			Expect(gzTag).NotTo(Equal(plainTag), "a strong ETag names one byte representation")

			notMod := get(path, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzTag})
			Expect(notMod.Code).To(Equal(http.StatusNotModified))
			Expect(notMod.Body.Len()).To(BeZero())
			Expect(notMod.Header().Get("ETag")).To(Equal(gzTag))

			// A list containing the tag, as browsers and proxies send it.
			listed := get(path, map[string]string{"If-None-Match": `"nope", ` + plainTag})
			Expect(listed.Code).To(Equal(http.StatusNotModified))

			stale := get(path, map[string]string{"If-None-Match": `"stale"`})
			Expect(stale.Code).To(Equal(http.StatusOK))
		})
	}

	It("honours gzip;q=0 as a refusal", func() {
		rec := get(pinned, map[string]string{"Accept-Encoding": "gzip;q=0"})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Encoding")).To(BeEmpty())
	})

	It("keeps the version-pinned route immutable and current revalidating", func() {
		Expect(get(pinned, map[string]string{"Accept-Encoding": "gzip"}).Header().Get("Cache-Control")).
			To(ContainSubstring("immutable"))
		Expect(strings.Contains(get("/schema/current", nil).Header().Get("Cache-Control"), "immutable")).
			To(BeFalse())
	})

	It("serves the same bytes on repeat requests (encoded once, cached per version)", func() {
		a := get(pinned, map[string]string{"Accept-Encoding": "gzip"})
		b := get(pinned, map[string]string{"Accept-Encoding": "gzip"})
		Expect(b.Body.Bytes()).To(Equal(a.Body.Bytes()))
	})
})
