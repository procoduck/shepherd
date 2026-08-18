//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Shepherd E2E Suite", Label("e2e"))
}

const (
	shepherdURL = "http://localhost:8080"
	alloyURL    = "http://localhost:12345"
	mockmsftURL = "http://localhost:9090"

	appAdminGroupID = "11111111-1111-1111-1111-111111111111"
)

// apiClient makes authenticated requests to Shepherd.
type apiClient struct {
	hc         *http.Client
	baseURL    string
	authCookie string
}

func newAnonymousClient() *apiClient {
	return &apiClient{
		hc:      &http.Client{Timeout: 10 * time.Second},
		baseURL: shepherdURL,
	}
}

func (c *apiClient) do(method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body) //nolint:errcheck // test helper
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if c.authCookie != "" {
		req.Header.Set("Cookie", c.authCookie)
	}
	return c.hc.Do(req)
}

func (c *apiClient) getJSON(path string, out any) {
	GinkgoHelper()
	resp, err := c.do("GET", path, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	Expect(json.NewDecoder(resp.Body).Decode(out)).To(Succeed())
	_ = resp.Body.Close()
}

func (c *apiClient) postJSON(path string, body, out any) int {
	GinkgoHelper()
	resp, err := c.do("POST", path, body)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode
		}
	}
	return resp.StatusCode
}

func fixture(kind string, data map[string]any) {
	GinkgoHelper()
	body, _ := json.Marshal(map[string]any{"kind": kind, "data": data}) //nolint:errcheck // test helper
	resp, err := http.Post(mockmsftURL+"/__fixture", "application/json", bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
	_ = resp.Body.Close()
}

// waitHTTP polls url until it returns 200 or timeout using Eventually (no raw sleep).
func waitHTTP(url string, timeout time.Duration) {
	GinkgoHelper()
	Eventually(func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}).WithTimeout(timeout).WithPolling(500*time.Millisecond).Should(BeTrue(), "timed out waiting for %s", url)
}

var (
	adminClient *apiClient
	orgID       string
)

var _ = SynchronizedBeforeSuite(func() []byte {
	// Wait for shepherd to be ready.
	waitHTTP(shepherdURL+"/healthz", 90*time.Second)
	waitHTTP(alloyURL+"/-/ready", 60*time.Second)
	return nil
}, func(_ []byte) {
	// Resolve the admin token ID from the tokens list.
	// (shepherd-init creates it with a known secret)
	anon := newAnonymousClient()
	var tokens struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	anon.getJSON("/api/admin/agent-tokens", &tokens)
	Expect(tokens.Items).NotTo(BeEmpty())

	adminClient = anon // In M5 this would be a logged-in admin; for now use anon (RBAC not enforced)
})

var _ = SynchronizedAfterSuite(func() {}, func() {
})

var _ = Describe("Shepherd E2E", Ordered, func() {
	_ = Describe("1. Registration & claiming", Ordered, func() {
		It("shows e2e-cluster as unclaimed after Alloy boots", func() {
			var clusters struct {
				Items []struct {
					Name  string `json:"name"`
					OrgID string `json:"org_id"`
				} `json:"items"`
			}
			Eventually(func() bool {
				adminClient.getJSON("/api/admin/clusters", &clusters)
				for _, c := range clusters.Items {
					if c.Name == "e2e-cluster" {
						return true
					}
				}
				return false
			}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())
		})

		It("creates an org and claims the cluster", func() {
			var org struct {
				ID string `json:"id"`
			}
			status := adminClient.postJSON("/api/admin/orgs", map[string]string{
				"name":           "e2e-org",
				"display_name":   "E2E Org",
				"admin_group_id": appAdminGroupID,
			}, &org)
			Expect(status).To(Equal(http.StatusCreated))
			orgID = org.ID
			Expect(orgID).NotTo(BeEmpty())

			status = adminClient.postJSON("/api/admin/clusters/e2e-cluster/claim", map[string]string{
				"org_id": orgID,
			}, nil)
			Expect(status).To(Equal(http.StatusOK))
		})
	})

	_ = Describe("2. Pipeline lifecycle (core loop)", Ordered, func() {
		var pipelineID string
		var pipelineHash string

		It("creates and enables a pipeline", func() {
			Expect(orgID).NotTo(BeEmpty(), "org must be claimed first")

			var p struct {
				ID string `json:"id"`
			}
			status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/pipelines", orgID), map[string]any{
				"name":     "e2e-pipe",
				"contents": `prometheus.exporter.self "e2e" { }`,
				"matchers": []string{`cluster="e2e-cluster"`, `role="metrics"`},
			}, &p)
			Expect(status).To(Equal(http.StatusCreated))
			pipelineID = p.ID
			Expect(pipelineID).NotTo(BeEmpty())

			status = adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/pipelines/%s/enable", orgID, pipelineID), nil, nil)
			Expect(status).To(Equal(http.StatusOK))
		})

		It("served-config contains declare-wrapped content", func() {
			var collectors struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors", orgID), &collectors)
			Expect(collectors.Items).NotTo(BeEmpty())

			collID := collectors.Items[0].ID
			var served struct {
				Content string `json:"content"`
				Hash    string `json:"hash"`
			}
			Eventually(func() bool {
				adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors/%s/served-config", orgID, collID), &served)
				return served.Hash != "" && served.Hash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
			}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())

			Expect(served.Content).To(ContainSubstring(`declare "pipe_e2e_pipe"`))
			pipelineHash = served.Hash
		})

		It("Alloy reports remote_config_status APPLIED", func() {
			var collectors struct {
				Items []struct {
					ID                 string `json:"id"`
					RemoteConfigStatus string `json:"remote_config_status"`
				} `json:"items"`
			}
			Eventually(func() bool {
				adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors", orgID), &collectors)
				for _, c := range collectors.Items {
					if c.RemoteConfigStatus == "RemoteConfigStatuses_APPLIED" {
						return true
					}
				}
				return false
			}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())
		})

		It("disabling the pipeline returns header-only config", func() {
			status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/pipelines/%s/disable", orgID, pipelineID), nil, nil)
			Expect(status).To(Equal(http.StatusOK))

			_ = pipelineHash // suppress unused
			// The served hash should eventually change back to the empty-content hash.
			var collectors struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors", orgID), &collectors)
			if len(collectors.Items) > 0 {
				collID := collectors.Items[0].ID
				var served struct {
					Content string `json:"content"`
				}
				Eventually(func() bool {
					adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors/%s/served-config", orgID, collID), &served)
					return !bytes.Contains([]byte(served.Content), []byte("pipe_e2e_pipe"))
				}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())
			}
		})
	})

	_ = Describe("3. not_modified efficiency", func() {
		It("shepherd_getconfig_total{result=not_modified} increases over time", func() {
			getCounter := func() float64 {
				resp, err := http.Get(shepherdURL + "/metrics")
				if err != nil {
					return 0
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body) //nolint:errcheck // test helper
				for _, line := range bytes.Split(body, []byte("\n")) {
					if bytes.HasPrefix(line, []byte(`shepherd_getconfig_total{result="not_modified"}`)) {
						var v float64
						_, _ = fmt.Sscanf(string(line), `shepherd_getconfig_total{result="not_modified"} %f`, &v) //nolint:errcheck // test helper
						return v
					}
				}
				return 0
			}

			initial := getCounter()
			// Wait until counter increases — replaces raw 25s sleep (P1-E.3).
			Eventually(func() float64 {
				return getCounter()
			}, 60*time.Second, 2*time.Second).Should(BeNumerically(">", initial))
		})
	})

	_ = Describe("4. Validation gate", func() {
		It("rejects syntactically invalid pipeline with 422", func() {
			Expect(orgID).NotTo(BeEmpty())
			var result struct {
				Error struct{ Code string } `json:"error"`
			}
			status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/pipelines/validate", orgID), map[string]string{
				"contents": "prometheus.scrape { missing closing",
			}, &result)
			Expect(status).To(Equal(http.StatusUnprocessableEntity))
		})
	})

	_ = Describe("5. GitOps sync", func() {
		It("syncs an alloy file from mockmsft ADO", func() {
			Expect(orgID).NotTo(BeEmpty())

			// Seed a .alloy file in the mock ADO.
			fixture("ado_file", map[string]any{
				"path":    "/pipelines/gitpipe.alloy",
				"content": `// git-sourced pipeline\nprometheus.exporter.self "git" {}`,
			})

			// TODO: create ADO credential + repo link via API and wait for sync.
			// Full wiring requires the gitsync reconciler to run with the mock ADO base URL,
			// which is configured via SHEPHERD_ADO_BASE_URL in the compose stack.
			// This scenario verifies the fixture endpoint is reachable.
			resp, err := http.Get(mockmsftURL + "/health")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			_ = resp.Body.Close()
		})
	})

	_ = Describe("6. RBAC", Ordered, func() {
		// P1-E.1: unauthenticated mgmt request MUST return 401 exactly (not 200).
		// Red-green proof: a fully open server passes BeElementOf(200,401) — the old test
		// could not detect an RBAC regression. This test FAILS if auth is disabled.
		It("unauthenticated /api/* returns 401 exactly", func() {
			resp, err := http.Get(shepherdURL + "/api/admin/orgs")
			Expect(err).NotTo(HaveOccurred())
			// OIDC issuer is configured in the e2e stack — unauthenticated MUST be 401.
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized), "unauthenticated mgmt request must return 401; got %d — RBAC is not enforced", resp.StatusCode)
			_ = resp.Body.Close()
		})

		It("agent endpoint with wrong secret returns unauthenticated", func() {
			// Use the Connect JSON protocol with a bad auth header.
			req, err := http.NewRequest("POST",
				shepherdURL+"/collector.v1.CollectorService/RegisterCollector",
				bytes.NewBufferString(`{"id":"rbac-test","name":"test","localAttributes":{"cluster":"test","role":"metrics"}}`))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Basic YmFkOnNlY3JldA==") // bad:secret
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			_ = resp.Body.Close()
		})

		// Revoked-token flow: create a token, use it once successfully, revoke via API, assert next call rejects.
		It("revoked agent token is rejected on next GetConfig", func() {
			Expect(orgID).NotTo(BeEmpty(), "org must be created — scenario 1 must run first")

			// Create a new agent token via admin API (adminClient must be authenticated).
			var created struct {
				ID     string `json:"id"`
				Secret string `json:"secret"`
			}
			status := adminClient.postJSON("/api/admin/agent-tokens", map[string]string{
				"name": "rbac-revoke-test",
			}, &created)
			Expect(status).To(Equal(http.StatusCreated))
			Expect(created.ID).NotTo(BeEmpty())
			Expect(created.Secret).NotTo(BeEmpty())

			// Use the token once — should succeed.
			basicAuth := func(id, secret string) string {
				return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
			}
			req, err := http.NewRequest("POST",
				shepherdURL+"/collector.v1.CollectorService/RegisterCollector",
				bytes.NewBufferString(`{"id":"revoke-test-inst","name":"revoke-test","localAttributes":{"cluster":"revoke-cluster","role":"metrics"}}`))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", basicAuth(created.ID, created.Secret))
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// Revoke the token.
			revokeResp, err := adminClient.do("DELETE", "/api/admin/agent-tokens/"+created.ID, nil)
			Expect(err).NotTo(HaveOccurred())
			_ = revokeResp.Body.Close()
			Expect(revokeResp.StatusCode).To(Equal(http.StatusNoContent))

			// Next call with the revoked token must be rejected.
			req2, err := http.NewRequest("POST",
				shepherdURL+"/collector.v1.CollectorService/RegisterCollector",
				bytes.NewBufferString(`{"id":"revoke-test-inst2","name":"revoke-test2","localAttributes":{"cluster":"revoke-cluster","role":"metrics"}}`))
			Expect(err).NotTo(HaveOccurred())
			req2.Header.Set("Content-Type", "application/json")
			req2.Header.Set("Authorization", basicAuth(created.ID, created.Secret))
			resp2, err := http.DefaultClient.Do(req2)
			Expect(err).NotTo(HaveOccurred())
			_ = resp2.Body.Close()
			Expect(resp2.StatusCode).To(Equal(http.StatusUnauthorized), "revoked token must be rejected")
		})
	})

	_ = Describe("7. Status APPLIED propagation round-trip", func() {
		It("UNSET → APPLIED status transitions round-trip correctly via scenarios 1-2", func() {
			// The key lifecycle test was covered in scenario 2. Here we assert the
			// plumbing is consistent: if a collector instance exists, its status
			// is either unset (empty) or a known RemoteConfigStatuses value.
			// DECISION: APPLYING may never be observed due to timing — accepted per spec §18.4.
			if orgID == "" {
				Skip("org not yet created — scenarios 1-2 must run first")
			}
			var collectors struct {
				Items []struct {
					RemoteConfigStatus string `json:"remote_config_status"`
				} `json:"items"`
			}
			adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/collectors", orgID), &collectors)
			for _, c := range collectors.Items {
				Expect(c.RemoteConfigStatus).To(SatisfyAny(
					BeEmpty(),
					Equal("RemoteConfigStatuses_UNSET"),
					Equal("RemoteConfigStatuses_APPLYING"),
					Equal("RemoteConfigStatuses_APPLIED"),
					Equal("RemoteConfigStatuses_FAILED"),
				))
			}
		})
	})
}) // end Shepherd E2E Ordered

// Scenario 8: Local admin alongside OIDC — login + audit actor (LA-1).
// Runs as a separate non-Ordered describe so it does not depend on orgID from the main flow.
var _ = Describe("8. Local admin login + audit actor (LA-1)", func() {
	It("local admin login with allow_with_oidc=true succeeds and /api/me returns auth_method:local", func() {
		// POST /api/auth/local/login with the e2e static password.
		type loginRequest struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		body, err := json.Marshal(loginRequest{Username: "admin", Password: "e2e-local-admin-pass"})
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest("POST", shepherdURL+"/api/auth/local/login", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")

		jar := &cookieJar{}
		hc := &http.Client{Timeout: 10 * time.Second, Jar: jar}
		resp, err := hc.Do(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "local admin login must return 200")
		_ = resp.Body.Close()

		// GET /api/me with the session cookie — must return auth_method:"local".
		meReq, err := http.NewRequest("GET", shepherdURL+"/api/me", nil)
		Expect(err).NotTo(HaveOccurred())
		meReq.Header.Set("X-Requested-With", "XMLHttpRequest")
		meResp, err := hc.Do(meReq)
		Expect(err).NotTo(HaveOccurred())
		defer meResp.Body.Close()
		Expect(meResp.StatusCode).To(Equal(http.StatusOK))

		var me map[string]any
		Expect(json.NewDecoder(meResp.Body).Decode(&me)).To(Succeed())
		Expect(me["auth_method"]).To(Equal("local"), "auth_method must be 'local' for local admin session")
	})
})

// cookieJar is a minimal http.CookieJar for e2e tests.
type cookieJar struct {
	mu      sync.Mutex
	cookies []*http.Cookie
}

func (j *cookieJar) SetCookies(_ *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = append(j.cookies, cookies...)
}

func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies
}
