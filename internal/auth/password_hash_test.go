package auth_test

import (
	"regexp"

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

	It("refuses an encoded hash whose parallelism does not fit argon2's uint8", func() {
		// "p" is parsed with bitSize 8 (CodeQL go/incorrect-integer-conversion):
		// a stored hash claiming p=300 is malformed and must error, never be
		// silently verified as p=44. Red run: parsing "p" with bitSize 32 and
		// narrowing afterwards makes this return ok=false, err=nil instead.
		hash, err := auth.HashPassword("correct-horse-battery-staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(hash).To(MatchRegexp(`\$argon2id\$v=\d+\$m=\d+,t=\d+,p=\d+\$`))
		bad := regexp.MustCompile(`,p=\d+\$`).ReplaceAllString(hash, ",p=300$")
		Expect(bad).NotTo(Equal(hash))
		_, err = auth.VerifyPassword(bad, "correct-horse-battery-staple")
		Expect(err).To(MatchError(ContainSubstring("parsing argon2id params")))
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
