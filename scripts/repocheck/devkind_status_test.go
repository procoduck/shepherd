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

// `make dev-kind-status` reported the agents' remotecfg health only: an agent
// whose beacon never authenticated read "ok" while the Reconciliation tab had
// no observed data (0.22 -> 0.23 upgrade walkthrough). The status report now
// counts beacon_inventory rows per instance, read from the CNPG primary with
// psql through kc. These specs run beacon_status against a stubbed db_psql.
var _ = Describe("scripts/dev-kind.sh beacon_status", func() {
	status := func(stub string) string {
		GinkgoHelper()
		body := funcBody(readRepoFile(devKindScriptRel), "beacon_status")
		cmd := exec.Command("bash", "-c", "DB_CLUSTER=shepherd-db\ndb_psql() {\n"+stub+"\n}\nbeacon_status() {"+body+"\n}\nbeacon_status")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		return strings.TrimSpace(string(out))
	}

	It("is part of the status report and reads beacon_inventory through kc", func() {
		script := readRepoFile(devKindScriptRel)
		Expect(funcBody(script, "cmd_status")).To(ContainSubstring("beacon_status"))
		Expect(funcBody(script, "beacon_status")).To(ContainSubstring("beacon_inventory"))
		psql := funcBody(script, "db_psql")
		Expect(psql).To(ContainSubstring(`kc -n "$NAMESPACE" exec`))
		Expect(psql).To(ContainSubstring("psql"))
		Expect(psql).To(ContainSubstring("cnpg.io/cluster=${DB_CLUSTER}"))
	})

	It("reports rows per instance with their healthy count", func() {
		Expect(status(`printf 'alloy-logs 12 12\nalloy-singleton 7 6\n'`)).To(Equal(
			"beacon_inventory: alloy-logs: 12 components (12 healthy)\n" +
				"beacon_inventory: alloy-singleton: 7 components (6 healthy)"))
	})

	It("says so when no beacon has reported", func() {
		Expect(status(`true`)).To(Equal("beacon_inventory: 0 rows — no beacon has reported yet"))
	})

	It("says so when the database cannot be reached", func() {
		Expect(status(`return 1`)).To(ContainSubstring("database unreachable"))
	})
})
