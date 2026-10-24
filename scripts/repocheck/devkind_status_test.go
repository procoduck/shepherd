package repocheck_test

import (
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// `make dev-kind-status` read every "failed to ... remote config" line as the
// agent rejecting its served config. The 0.22 -> 0.23 upgrade walkthrough
// showed a refused agent (an unbound token) reported that way: Alloy logs a
// refusal as "failed to fetch remote config" with err="permission_denied: …".
// alloy_log_verdict tells the two apart; these specs run it on real lines.
var _ = Describe("scripts/dev-kind.sh alloy_log_verdict", func() {
	verdict := func(logs string) string {
		GinkgoHelper()
		body := funcBody(readRepoFile(devKindScriptRel), "alloy_log_verdict")
		cmd := exec.Command("bash", "-c", "alloy_log_verdict() {"+body+"\n}\nalloy_log_verdict")
		cmd.Stdin = strings.NewReader(logs)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		return strings.TrimSpace(string(out))
	}

	It("reports a refused agent as refused, not as rejecting its config", func() {
		Expect(verdict(`ts=2026-10-24T00:19:27Z level=error msg="failed to fetch remote config" service=remotecfg err="permission_denied: agent token is not bound to an organisation; bind it with BindAgentToken / ` + "`shepherd token bind`" + `"
ts=2026-10-24T00:19:27Z level=error msg="failed to get configuration from remote server" service=remotecfg id=x err="permission_denied: agent token is not bound to an organisation"
`)).To(Equal("refused"))
	})

	It("reports an unauthenticated agent as refused", func() {
		Expect(verdict(`level=error msg="failed to fetch remote config" service=remotecfg err="unauthenticated"` + "\n")).To(Equal("refused"))
	})

	It("reports a fetch failure that is not a refusal as unreachable", func() {
		Expect(verdict(`level=error msg="failed to fetch remote config" service=remotecfg err="unavailable: server temporarily unavailable"` + "\n")).To(Equal("unreachable"))
	})

	It("reports a config the agent could not load as rejecting", func() {
		Expect(verdict(`level=error msg="failed to load remote config" service=remotecfg err="40:3: Failed to build component"` + "\n")).To(Equal("rejecting"))
	})

	It("reports a quiet agent as ok", func() {
		Expect(verdict(`level=info msg="successfully loaded remote configuration" service=remotecfg` + "\n")).To(Equal("ok"))
		Expect(verdict("")).To(Equal("ok"))
	})
})
