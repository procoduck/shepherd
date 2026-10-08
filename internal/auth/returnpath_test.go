package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2"

	"shepherd/internal/auth"
	"shepherd/internal/config"
)

// #250: the page a signed-out user asked for is carried through the login
// round trip as ?next=. Whatever comes back out is used as a redirect target,
// so anything that is not a same-origin relative path must be refused — an
// open redirect on the sign-in flow is a phishing primitive.
var _ = Describe("SafeReturnPath", func() {
	DescribeTable("accepts a same-origin relative path",
		func(raw, want string) {
			Expect(auth.SafeReturnPath(raw)).To(Equal(want))
		},
		Entry("a page", "/pipelines", "/pipelines"),
		Entry("a nested page", "/pipelines/0b6f/visual", "/pipelines/0b6f/visual"),
		Entry("a query string", "/audit?actor=alice&page=2", "/audit?actor=alice&page=2"),
		Entry("a fragment", "/collectors/abc#labels", "/collectors/abc#labels"),
		Entry("an encoded space", "/pipelines/a%20b", "/pipelines/a%20b"),
		Entry("the root", "/", "/"),
	)

	DescribeTable("refuses anything that could leave the origin",
		func(raw string) {
			Expect(auth.SafeReturnPath(raw)).To(BeEmpty())
		},
		Entry("empty", ""),
		Entry("an absolute https URL", "https://evil.example/"),
		Entry("an absolute http URL", "http://evil.example"),
		Entry("a javascript: URL", "javascript:alert(1)"),
		Entry("a data: URL", "data:text/html,<script>alert(1)</script>"),
		Entry("a scheme without slashes", "https:evil.example"),
		Entry("a relative path without a leading slash", "pipelines"),
		Entry("a protocol-relative URL", "//evil.example"),
		Entry("a protocol-relative URL with a path", "//evil.example/pipelines"),
		Entry("three slashes", "///evil.example"),
		Entry("a backslash after the slash", `/\evil.example`),
		Entry("two backslashes", `\\evil.example`),
		Entry("a backslash later in the path", `/pipelines\..\\evil.example`),
		Entry("a tab browsers strip", "/\t/evil.example"),
		Entry("a newline browsers strip", "/\n/evil.example"),
		Entry("a carriage return (header injection)", "/pipelines\r\nSet-Cookie: x=y"),
		Entry("a NUL byte", "/pipelines\x00"),
		Entry("an encoded second slash", "/%2Fevil.example"),
		Entry("an encoded second slash, lower case", "/%2fevil.example"),
		Entry("two encoded slashes", "%2F%2Fevil.example"),
		Entry("an encoded backslash", "/%5Cevil.example"),
		Entry("an encoded backslash, lower case", "/%5cevil.example"),
		Entry("a double-encoded second slash", "/%252Fevil.example"),
		Entry("a double-encoded backslash", "/%255Cevil.example"),
		Entry("an encoded tab", "/%09/evil.example"),
		Entry("an encoded newline", "/%0A/evil.example"),
		Entry("malformed percent-encoding", "/pipelines%zz"),
		Entry("userinfo smuggled after the slash", "//user@evil.example"),
		Entry("the login page (a loop)", "/login"),
		Entry("the login page with a query", "/login?next=/pipelines"),
		Entry("a server auth route", "/auth/logout"),
		Entry("an over-long path", "/"+string(make([]byte, 4096))),
	)
})

var _ = Describe("OIDC return path through the login round trip", func() {
	newHandler := func() *auth.Handler {
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		return auth.NewOIDCTestHandler(cfg, &oauth2.Config{
			ClientID:    "client-id",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://issuer.example/authorize"},
			RedirectURL: "https://shepherd.example/auth/callback",
			Scopes:      []string{"openid"},
		})
	}
	login := func(next string) *http.Cookie {
		target := "/auth/login"
		if next != "" {
			target += "?next=" + url.QueryEscape(next)
		}
		rr := httptest.NewRecorder()
		newHandler().LoginHandler(rr, httptest.NewRequest(http.MethodGet, target, nil))
		Expect(rr.Code).To(Equal(http.StatusFound))
		for _, c := range rr.Result().Cookies() {
			if c.Name == auth.OIDCReturnCookie {
				return c
			}
		}
		return nil
	}
	callbackTarget := func(c *http.Cookie) string {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
		if c != nil {
			req.AddCookie(c)
		}
		return auth.OIDCReturnPath(req)
	}

	It("carries a safe ?next= to the callback's redirect", func() {
		c := login("/pipelines/0b6f/visual?tab=graph")
		Expect(c).NotTo(BeNil(), "LoginHandler must remember a safe next path")
		Expect(c.HttpOnly).To(BeTrue())
		Expect(c.MaxAge).To(BeNumerically(">", 0))
		Expect(c.SameSite).To(Equal(http.SameSiteLaxMode))
		Expect(callbackTarget(c)).To(Equal("/pipelines/0b6f/visual?tab=graph"))
	})

	DescribeTable("does not remember an unsafe ?next=, and clears a stale one",
		func(next string) {
			c := login(next)
			if c != nil {
				Expect(c.MaxAge).To(BeNumerically("<", 0), "an unsafe next must clear, not set, the cookie")
			}
			Expect(callbackTarget(nil)).To(Equal("/"))
		},
		Entry("no next at all", ""),
		Entry("an absolute URL", "https://evil.example/"),
		Entry("protocol-relative", "//evil.example"),
		Entry("a backslash", `/\evil.example`),
		Entry("an encoded slash", "/%2Fevil.example"),
	)

	It("re-validates the cookie on the way out rather than trusting it", func() {
		// A cookie is client-controlled: a sibling subdomain can toss one,
		// and an older build may have written a different shape. Whatever
		// the callback reads is checked again.
		for _, raw := range []string{"//evil.example", "https://evil.example", `/\evil.example`, "not-base64!!"} {
			forged := &http.Cookie{Name: auth.OIDCReturnCookie, Value: auth.EncodeReturnCookie(raw)}
			if raw == "not-base64!!" {
				forged.Value = raw
			}
			Expect(callbackTarget(forged)).To(Equal("/"), "forged cookie %q", raw)
		}
	})
})
