package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// 0.21 walkthrough: packages that log through the package-level slog
// functions (internal/tenancy's "recorded as incomplete" warning,
// internal/validate's version check) wrote plain text into the server's JSON
// log, because serve never installed its logger as the default.
var _ = Describe("shepherd serve's logger", func() {
	It("is the default logger, so package-level slog calls use its format", func() {
		prev := slog.Default()
		DeferCleanup(func() { slog.SetDefault(prev) })

		var buf bytes.Buffer
		l, err := newServeLogger("json", &buf, slog.LevelInfo)
		Expect(err).NotTo(HaveOccurred())
		Expect(slog.Default()).To(BeIdenticalTo(l))

		slog.Warn("strict tenants: recorded as incomplete", "pipeline_id", "p1")
		var line map[string]any
		Expect(json.Unmarshal(buf.Bytes(), &line)).To(Succeed(), "got %q", buf.String())
		Expect(line).To(HaveKeyWithValue("msg", "strict tenants: recorded as incomplete"))
		Expect(line).To(HaveKeyWithValue("pipeline_id", "p1"))
		Expect(line).To(HaveKeyWithValue("level", "WARN"))
	})

	It("refuses an unknown format without replacing the default", func() {
		prev := slog.Default()
		DeferCleanup(func() { slog.SetDefault(prev) })
		_, err := newServeLogger("xml", &bytes.Buffer{}, slog.LevelInfo)
		Expect(err).To(HaveOccurred())
		Expect(slog.Default()).To(BeIdenticalTo(prev))
	})
})
