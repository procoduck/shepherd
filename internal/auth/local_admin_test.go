package auth_test

import (
	"bytes"
	"log/slog"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
)

var _ = Describe("Local Admin", Label("integration"), func() {
	It("HashPassword output verifies correctly and rejects wrong password", func() {
		hash, err := auth.HashPassword("correct-horse-battery-staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(hash).To(HavePrefix("$argon2id$"))
		ok, err := auth.VerifyPassword(hash, "correct-horse-battery-staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, err = auth.VerifyPassword(hash, "wrong-password")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("LocalAdminConfig LogValue redacts password_hash", func() {
		cfg := config.LocalAdminConfig{Enabled: true, Username: "admin", PasswordHash: "$argon2id$secret-hash-value", SessionTTL: time.Hour}
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logger.Info("config loaded", "local_admin", cfg)
		Expect(buf.String()).NotTo(ContainSubstring("secret-hash-value"))
		Expect(buf.String()).To(ContainSubstring("[REDACTED]"))
	})
})
