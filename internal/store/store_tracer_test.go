package store_test

import (
	"context"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"shepherd/internal/config"
	"shepherd/internal/store"
)

var _ = Describe("Store query tracer", Label("integration"), func() {
	It("logs a failed query's SQL and error but never its bound arguments", func() {
		ctx := context.Background()
		logs := gbytes.NewBuffer()
		logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

		st, err := store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 2}, logger)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		const marker = "tracer-arg-must-not-be-logged"
		_, err = st.Pool().Exec(ctx, "SELECT $1::text FROM no_such_table_for_tracer_spec", marker)
		Expect(err).To(HaveOccurred())

		out := string(logs.Contents())
		Expect(out).To(ContainSubstring("level=ERROR"))
		Expect(out).To(ContainSubstring("no_such_table_for_tracer_spec"))
		Expect(out).NotTo(ContainSubstring(marker))
		Expect(out).NotTo(ContainSubstring("args="))
	})
})
