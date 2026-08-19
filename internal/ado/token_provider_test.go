package ado_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/ado"
)

func TestADO(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ADO Token Provider Suite")
}

var _ = Describe("TokenProvider", func() {
	var (
		server         *httptest.Server
		requests       []url.Values
		lastAuthHeader string
		accessToken    string
		expiresIn      int
	)

	BeforeEach(func() {
		requests = nil
		lastAuthHeader = ""
		accessToken = "entra-access-token-abc123"
		expiresIn = 3600

		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.ParseForm()).To(Succeed())
			requests = append(requests, r.PostForm)
			lastAuthHeader = r.Header.Get("Authorization")

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"token_type":   "Bearer",
				"access_token": accessToken,
				"expires_in":   expiresIn,
			})
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	It("exchanges client credentials for an Entra token at the configured token URL", func() {
		provider := ado.NewTokenProviderWithTokenURL(server.URL, "client-id-1", "client-secret-1")

		tok, err := provider.Token(GinkgoT().Context())
		Expect(err).NotTo(HaveOccurred())
		Expect(tok.AccessToken).To(Equal(accessToken))
		Expect(tok.Expiry).To(BeTemporally(">", time.Now().Add(59*time.Minute)))

		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Get("grant_type")).To(Equal("client_credentials"))
		Expect(requests[0].Get("scope")).To(Equal("499b84ac-1321-427f-aa17-267ca6975798/.default"))
		user, pass, ok := (&http.Request{Header: http.Header{"Authorization": []string{lastAuthHeader}}}).BasicAuth()
		Expect(ok).To(BeTrue(), "expected the client id/secret to be sent as HTTP Basic auth")
		Expect(user).To(Equal("client-id-1"))
		Expect(pass).To(Equal("client-secret-1"))
	})

	It("mints a fresh token on every call — TokenProvider itself does not cache", func() {
		provider := ado.NewTokenProviderWithTokenURL(server.URL, "client-id-1", "client-secret-1")

		_, err := provider.Token(GinkgoT().Context())
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.Token(GinkgoT().Context())
		Expect(err).NotTo(HaveOccurred())

		Expect(requests).To(HaveLen(2))
	})

	It("returns a clear error when the token endpoint rejects the credentials", func() {
		server.Close()
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"error": "invalid_client",
			})
		}))

		provider := ado.NewTokenProviderWithTokenURL(server.URL, "client-id-1", "wrong-secret")
		_, err := provider.Token(GinkgoT().Context())
		Expect(err).To(HaveOccurred())
	})
})
