package crypto_test

import (
	"encoding/base64"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/crypto"
)

func TestCrypto(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Crypto Suite")
}

var _ = Describe("Encryptor", func() {
	var enc *crypto.Encryptor

	BeforeEach(func() {
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i)
		}
		var err error
		enc, err = crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
		Expect(err).NotTo(HaveOccurred())
	})

	It("round-trips plaintext", func() {
		plain := []byte("my secret value")
		ct, err := enc.Encrypt(plain)
		Expect(err).NotTo(HaveOccurred())
		Expect(ct).NotTo(Equal(plain))

		got, err := enc.Decrypt(ct)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(plain))
	})

	It("nonce uniqueness across calls", func() {
		ct1, err1 := enc.Encrypt([]byte("value"))
		Expect(err1).NotTo(HaveOccurred())
		ct2, err2 := enc.Encrypt([]byte("value"))
		Expect(err2).NotTo(HaveOccurred())
		Expect(ct1).NotTo(Equal(ct2), "each call should produce a unique nonce")
	})

	It("wrong key fails decryption", func() {
		ct, encErr := enc.Encrypt([]byte("value"))
		Expect(encErr).NotTo(HaveOccurred())

		wrongKey := make([]byte, 32)
		for i := range wrongKey {
			wrongKey[i] = 0xFF
		}
		badEnc, badEncErr := crypto.NewEncryptor(base64.StdEncoding.EncodeToString(wrongKey))
		Expect(badEncErr).NotTo(HaveOccurred())
		_, err := badEnc.Decrypt(ct)
		Expect(err).To(HaveOccurred())
	})

	It("refuses to boot without valid encryption key", func() {
		_, err := crypto.NewEncryptor("")
		Expect(err).To(HaveOccurred())

		_, err2 := crypto.NewEncryptor("dG9vc2hvcnQ=") // base64 of "tooshort"
		Expect(err2).To(HaveOccurred())
		Expect(err).To(HaveOccurred())
	})
})
