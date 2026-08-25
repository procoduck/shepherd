package auth_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
)

var _ = Describe("password hashing", func() {
	It("verifies its own output and rejects a wrong password", func() {
		hash, err := auth.HashPassword("correct-horse-battery-staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(hash).To(HavePrefix("$argon2id$"))

		ok, err := auth.VerifyPassword(hash, "correct-horse-battery-staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		ok, err = auth.VerifyPassword(hash, "wrong")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("produces a different hash for the same password each time", func() {
		// Per-hash salt. Two identical passwords hashing to the same string
		// would make the users table a rainbow-table lookup of who shares a
		// password with whom.
		a, err := auth.HashPassword("same-password")
		Expect(err).NotTo(HaveOccurred())
		b, err := auth.HashPassword("same-password")
		Expect(err).NotTo(HaveOccurred())
		Expect(a).NotTo(Equal(b))
	})

	It("enforces a minimum length rather than a complexity ritual", func() {
		Expect(auth.ValidatePassword("short")).To(MatchError(auth.ErrPasswordTooShort))
		Expect(auth.ValidatePassword("longenough")).To(Succeed())
	})
})
