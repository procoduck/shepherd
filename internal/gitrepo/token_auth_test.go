package gitrepo_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gitrepo"
)

var _ = Describe("AdoSPAuth", func() {
	var (
		ctx         = GinkgoT().Context
		hits        atomic.Int32
		accessToken string
		expiresIn   int
		server      *httptest.Server
	)

	newServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"token_type":   "Bearer",
				"access_token": accessToken,
				"expires_in":   expiresIn,
			})
		}))
	}

	BeforeEach(func() {
		hits.Store(0)
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	It("sends the minted token as the HTTP Basic password with an empty username", func() {
		accessToken = "ado-sp-token-xyz"
		expiresIn = 3600
		server = newServer()

		auth := gitrepo.AdoSPAuth{
			CredentialID: "cred-1",
			TenantID:     "tenant-1",
			ClientID:     "client-1",
			ClientSecret: "secret-1",
			TokenURL:     server.URL,
			Cache:        gitrepo.NewTokenCache(),
		}

		username, password, ok, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(username).To(Equal(""))
		Expect(password).To(Equal(accessToken))

		_, ok, err = auth.SSH(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "ado_sp is HTTPS-only")
	})

	It("does not re-exchange on a second call while the cached token is still fresh", func() {
		accessToken = "ado-sp-token-fresh"
		expiresIn = 3600 // well beyond the refresh skew
		server = newServer()

		auth := gitrepo.AdoSPAuth{
			CredentialID: "cred-1",
			ClientID:     "client-1",
			ClientSecret: "secret-1",
			TokenURL:     server.URL,
			Cache:        gitrepo.NewTokenCache(),
		}

		_, _, _, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		_, _, _, err = auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())

		Expect(hits.Load()).To(Equal(int32(1)))
	})

	It("re-exchanges when the cached token is within the refresh window", func() {
		accessToken = "ado-sp-token-near-expiry"
		expiresIn = 10 // well inside the refresh skew
		server = newServer()

		auth := gitrepo.AdoSPAuth{
			CredentialID: "cred-1",
			ClientID:     "client-1",
			ClientSecret: "secret-1",
			TokenURL:     server.URL,
			Cache:        gitrepo.NewTokenCache(),
		}

		_, _, _, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		_, _, _, err = auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())

		Expect(hits.Load()).To(Equal(int32(2)))
	})
})

var _ = Describe("GitHubAppAuth", func() {
	var (
		ctx        = GinkgoT().Context
		privateKey *rsa.PrivateKey
		privatePEM []byte
		hits       atomic.Int32
		expiresIn  time.Duration
		server     *httptest.Server

		capturedAuthHeader string
	)

	BeforeEach(func() {
		var err error
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		der, err := x509.MarshalPKCS8PrivateKey(privateKey)
		Expect(err).NotTo(HaveOccurred())
		privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

		hits.Store(0)
		capturedAuthHeader = ""
	})

	newServer := func(installationID, token string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()

			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.URL.Path).To(Equal("/app/installations/" + installationID + "/access_tokens"))
			capturedAuthHeader = r.Header.Get("Authorization")

			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"token":      token,
				"expires_at": time.Now().Add(expiresIn).UTC().Format(time.RFC3339),
			})
		}))
	}

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	It("signs an RS256 JWT with the configured key and the right iss/lifetime, then uses the installation token as the git password", func() {
		expiresIn = 1 * time.Hour
		server = newServer("42", "installation-token-abc")

		auth := gitrepo.GitHubAppAuth{
			CredentialID:   "cred-gh-1",
			AppID:          "123456",
			InstallationID: "42",
			PrivateKeyPEM:  privatePEM,
			APIBase:        server.URL,
			Cache:          gitrepo.NewTokenCache(),
			HTTPClient:     server.Client(),
		}

		username, password, ok, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(username).To(Equal("x-access-token"))
		Expect(password).To(Equal("installation-token-abc"))

		Expect(capturedAuthHeader).To(HavePrefix("Bearer "))
		jwtToken := strings.TrimPrefix(capturedAuthHeader, "Bearer ")

		header, claims := decodeJWTUnverified(jwtToken)
		Expect(header["alg"]).To(Equal("RS256"))
		Expect(header["typ"]).To(Equal("JWT"))
		Expect(claims["iss"]).To(Equal("123456"))

		iatFloat, ok := claims["iat"].(float64)
		Expect(ok).To(BeTrue(), "expected a numeric iat claim")
		expFloat, ok := claims["exp"].(float64)
		Expect(ok).To(BeTrue(), "expected a numeric exp claim")
		iat, exp := int64(iatFloat), int64(expFloat)
		Expect(exp).To(BeNumerically(">", iat))
		Expect(exp-iat).To(BeNumerically("<=", 600), "GitHub App JWT lifetime must not exceed 10 minutes")

		Expect(verifyJWTSignature(jwtToken, &privateKey.PublicKey)).To(Succeed())

		_, ok, err = auth.SSH(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "github_app is HTTPS-only")
	})

	It("rejects a JWT signed by a different key", func() {
		expiresIn = 1 * time.Hour
		server = newServer("42", "installation-token-abc")

		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())

		auth := gitrepo.GitHubAppAuth{
			CredentialID:   "cred-gh-1",
			AppID:          "123456",
			InstallationID: "42",
			PrivateKeyPEM:  privatePEM,
			APIBase:        server.URL,
			Cache:          gitrepo.NewTokenCache(),
			HTTPClient:     server.Client(),
		}

		_, _, ok, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		jwtToken := strings.TrimPrefix(capturedAuthHeader, "Bearer ")
		// The JWT was signed with privateKey, not otherKey: verifying
		// against otherKey's public half must fail.
		Expect(verifyJWTSignature(jwtToken, &otherKey.PublicKey)).To(HaveOccurred())
	})

	It("does not re-exchange on a second call while the cached token is still fresh", func() {
		expiresIn = 1 * time.Hour
		server = newServer("42", "installation-token-fresh")

		auth := gitrepo.GitHubAppAuth{
			CredentialID:   "cred-gh-2",
			AppID:          "123456",
			InstallationID: "42",
			PrivateKeyPEM:  privatePEM,
			APIBase:        server.URL,
			Cache:          gitrepo.NewTokenCache(),
			HTTPClient:     server.Client(),
		}

		_, _, _, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		_, _, _, err = auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())

		Expect(hits.Load()).To(Equal(int32(1)))
	})

	It("re-exchanges when the cached token is within the refresh window", func() {
		expiresIn = 10 * time.Second // well inside the refresh skew
		server = newServer("42", "installation-token-near-expiry")

		auth := gitrepo.GitHubAppAuth{
			CredentialID:   "cred-gh-3",
			AppID:          "123456",
			InstallationID: "42",
			PrivateKeyPEM:  privatePEM,
			APIBase:        server.URL,
			Cache:          gitrepo.NewTokenCache(),
			HTTPClient:     server.Client(),
		}

		_, _, _, err := auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		_, _, _, err = auth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())

		Expect(hits.Load()).To(Equal(int32(2)))
	})

	It("shares one TokenCache with an unrelated ado_sp credential without cross-contamination", func() {
		expiresIn = 1 * time.Hour
		server = newServer("42", "installation-token-shared")

		adoAccessToken := "ado-token-shared"
		adoHits := atomic.Int32{}
		adoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adoHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test
				"token_type":   "Bearer",
				"access_token": adoAccessToken,
				"expires_in":   3600,
			})
		}))
		defer adoServer.Close()

		shared := gitrepo.NewTokenCache()

		ghAuth := gitrepo.GitHubAppAuth{
			CredentialID:   "cred-shared-gh",
			AppID:          "123456",
			InstallationID: "42",
			PrivateKeyPEM:  privatePEM,
			APIBase:        server.URL,
			Cache:          shared,
			HTTPClient:     server.Client(),
		}
		adoAuth := gitrepo.AdoSPAuth{
			CredentialID: "cred-shared-ado",
			ClientID:     "client-1",
			ClientSecret: "secret-1",
			TokenURL:     adoServer.URL,
			Cache:        shared,
		}

		_, ghPassword, _, err := ghAuth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())
		_, adoPassword, _, err := adoAuth.HTTP(ctx())
		Expect(err).NotTo(HaveOccurred())

		Expect(ghPassword).To(Equal("installation-token-shared"))
		Expect(adoPassword).To(Equal(adoAccessToken))
		Expect(hits.Load()).To(Equal(int32(1)))
		Expect(adoHits.Load()).To(Equal(int32(1)))
	})
})

// decodeJWTUnverified splits and base64url-decodes a JWT's header and
// claims without checking the signature — signature verification is done
// separately by verifyJWTSignature so each test assertion is explicit
// about what it's checking.
func decodeJWTUnverified(token string) (header, claims map[string]any) {
	GinkgoHelper()
	parts := strings.Split(token, ".")
	Expect(parts).To(HaveLen(3), "expected a three-part JWT")

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	Expect(err).NotTo(HaveOccurred())
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	Expect(err).NotTo(HaveOccurred())

	Expect(json.Unmarshal(headerBytes, &header)).To(Succeed())
	Expect(json.Unmarshal(claimsBytes, &claims)).To(Succeed())
	return header, claims
}

// verifyJWTSignature verifies an RS256 JWT's signature against pub,
// independent of any go-git or JWT library — this is the assertion that
// actually proves the token was signed by the configured key.
func verifyJWTSignature(token string, pub *rsa.PublicKey) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a three-part JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig)
}
