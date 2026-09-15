// Specs over scripts/dev-kind.sh and dev/kind/cluster.yaml — the K1 slice of
// docs/plans/2026-09-14-kind-dev-stack.md.
//
// Red run, 2026-09-14, before scripts/dev-kind.sh existed:
//
//	readRepoFile: open scripts/dev-kind.sh: no such file or directory
//
// every assertion below failed the same way (the file did not exist), which
// is recorded once here rather than per-spec.
package repocheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const devKindScriptRel = "scripts/dev-kind.sh"

// devKindScriptPath resolves scripts/dev-kind.sh under the module root.
func devKindScriptPath() string {
	GinkgoHelper()
	return filepath.Join(repoRoot(), devKindScriptRel)
}

// kcHmDefRE matches a one-line `kc() { ... }` or `hm() { ... }` wrapper
// function definition, the only place a bare `kubectl`/`helm` call may live.
var kcHmDefRE = regexp.MustCompile(`(?m)^\s*(kc|hm)\(\)\s*\{.*\}\s*$`)

// bareKubectlHelmRE matches kubectl or helm invoked directly (anything but
// through the kc()/hm() wrappers) — at the start of a line, or after a
// pipe/semicolon/&/subshell-or-substitution paren, so `... | kubectl apply
// -f -` and `svc=$(kubectl get ...)` are caught too, not just a call that
// begins its own line.
var bareKubectlHelmRE = regexp.MustCompile(`(?m)(^|[|;&(])\s*(kubectl|helm)\s`)

var _ = Describe("scripts/dev-kind.sh", func() {
	It("exists, is executable, and passes bash -n", func() {
		info, err := os.Stat(devKindScriptPath())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()&0o111).NotTo(BeZero(), "scripts/dev-kind.sh must be executable")

		out, err := exec.Command("bash", "-n", devKindScriptPath()).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "bash -n scripts/dev-kind.sh: %s", out)
	})

	It("passes shellcheck when it is on PATH (never skipped for a finding)", func() {
		shellcheckPath, err := exec.LookPath("shellcheck")
		if err != nil {
			Skip("shellcheck not on PATH")
		}
		out, err := exec.Command(shellcheckPath, devKindScriptPath()).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "shellcheck scripts/dev-kind.sh:\n%s", out)
	})

	It("never calls kubectl or helm directly — only through the kc()/hm() wrappers", func() {
		content := readRepoFile(devKindScriptRel)

		defs := kcHmDefRE.FindAllString(content, -1)
		Expect(defs).To(HaveLen(2), "expected exactly one kc() and one hm() wrapper definition")
		var sawKC, sawHM bool
		for _, d := range defs {
			if strings.Contains(d, "kc()") {
				sawKC = true
				Expect(d).To(ContainSubstring(`--context "$CTX"`), "kc() must pass --context explicitly: %s", d)
			}
			if strings.Contains(d, "hm()") {
				sawHM = true
				Expect(d).To(ContainSubstring(`--kube-context "$CTX"`), "hm() must pass --kube-context explicitly: %s", d)
			}
		}
		Expect(sawKC).To(BeTrue())
		Expect(sawHM).To(BeTrue())

		withoutDefs := kcHmDefRE.ReplaceAllString(content, "")
		Expect(bareKubectlHelmRE.FindAllString(withoutDefs, -1)).To(BeEmpty(),
			"found a bare kubectl/helm call outside the kc()/hm() wrappers")
	})

	It("carries the pin variable names from deploy/versions.env, never a pin literal", func() {
		content := readRepoFile(devKindScriptRel)

		for _, literal := range []string{
			"kindest/node:",
			"calico/v3",
			"nginx-gateway-fabric --version 2",
			"cloudnative-pg --version 0",
		} {
			Expect(content).NotTo(ContainSubstring(literal), "found a hardcoded pin literal %q", literal)
		}

		for _, name := range []string{
			"KIND_NODE_IMAGE",
			"CALICO_VERSION",
			"NGF_CHART_VERSION",
			"CNPG_CHART_VERSION",
			"GATEWAY_API_VERSION",
			"GATEWAY_API_CHANNEL",
			"ALLOY_IMAGE",
		} {
			Expect(content).To(ContainSubstring(name), "missing pin variable %q", name)
		}
	})

	It("fails loudly if a required versions.env pin is missing", func() {
		content := readRepoFile(devKindScriptRel)
		Expect(content).To(ContainSubstring("deploy/versions.env"))
		Expect(content).To(MatchRegexp(`missing from deploy/versions\.env`))
	})

	It("down deletes the named cluster", func() {
		content := readRepoFile(devKindScriptRel)
		down := verbBody(content, "down")
		Expect(down).To(ContainSubstring(`kind delete cluster --name`))
	})

	It("reload sets a fresh dev-build annotation", func() {
		content := readRepoFile(devKindScriptRel)
		start := strings.Index(content, "cmd_reload()")
		end := strings.Index(content, "cmd_seed()")
		Expect(start).To(BeNumerically(">", 0), "cmd_reload() not found")
		Expect(end).To(BeNumerically(">", start), "cmd_seed() not found after cmd_reload()")
		reloadSection := content[start:end]
		Expect(reloadSection).To(ContainSubstring("install_shepherd"),
			"cmd_reload() should reuse the same helm upgrade --install as up (plan: \"the same helm upgrade --install\")")
		// install_shepherd, which cmd_reload calls, is what actually sets the
		// annotation — confirm it is defined once, above cmd_up, and used by
		// both cmd_up (via apply path) and cmd_reload.
		Expect(content).To(ContainSubstring(`--set-string "podAnnotations.dev-build=$(build_id)"`))
	})

	It("never combines --from-env-file with --from-literal in one kubectl create secret command", func() {
		// kubectl v1.36.4 rejects this combination deterministically:
		// "error: from-env-file cannot be combined with from-file or
		// from-literal" (reproduced offline, client-side dry-run, no
		// cluster). Every `create secret` statement in the script must
		// stick to a single env source.
		content := readRepoFile(devKindScriptRel)
		stmt := createSecretStatement(content)
		Expect(stmt).To(ContainSubstring("--from-env-file"),
			"expected the secret to be built from an env-file source: %s", stmt)
		Expect(stmt).NotTo(ContainSubstring("--from-literal"),
			"kubectl rejects --from-env-file combined with --from-literal in the same "+
				"create secret command — fold the literal into the env-file source instead "+
				"(e.g. a process substitution): %s", stmt)
	})

	It("writes the CoreDNS rewrite line without awk -v mangling its backslash escapes", func() {
		// awk -v processes escape sequences in the value it assigns, so
		// `\.` silently becomes `.` (verified with macOS awk; gawk does
		// the same and warns). That turns the contract line's anchored
		// `\.localtest\.me` into an unintended any-char regex, and
		// doubles as the reason the idempotency guard below must match
		// the escaped rule text, not the dotted literal.
		content := readRepoFile(devKindScriptRel)
		Expect(content).NotTo(ContainSubstring("awk -v line="),
			"awk -v unescapes backslashes in its value — pass the rewrite line through "+
				"the environment (ENVIRON) instead")
		Expect(content).To(ContainSubstring(`(.*)\.localtest\.me`),
			"the CoreDNS rewrite regex must keep its literal backslash escapes per §1")
	})

	It("CoreDNS idempotency guard matches the escaped rewrite rule, not the dotted hostname", func() {
		// The guard must recognize the rule apply_coredns_rewrite actually
		// writes (containing a literal backslash before "localtest"), not
		// merely the substring "localtest.me" — a pattern that also
		// happens to match the *target* hostname text once the backslash
		// bug above is fixed, which would make every re-run of `up`
		// append a duplicate rewrite line.
		content := readRepoFile(devKindScriptRel)
		Expect(content).NotTo(ContainSubstring(`grep -q 'localtest\.me'`),
			"the idempotency guard must match the rewrite rule, not the plain hostname text")
		Expect(content).To(MatchRegexp(`grep -q 'rewrite name regex`),
			"expected the guard to search for the rewrite rule itself")
	})

	It("up references every manifest the plan names and dev/kind/values.yaml", func() {
		content := readRepoFile(devKindScriptRel)
		// cmd_up() delegates to helper functions (ensure_cluster,
		// apply_workload_manifests, install_shepherd, ...) rather than
		// inlining every step, so the manifest references live in the
		// section of the file that precedes cmd_reload/cmd_seed/cmd_status/
		// cmd_down — everything those helpers need, and nothing those other
		// verbs' own bodies pull in incidentally.
		reloadAt := strings.Index(content, "cmd_reload()")
		Expect(reloadAt).To(BeNumerically(">", 0), "cmd_reload() not found")
		upSection := content[:reloadAt]
		for _, manifest := range []string{
			"dev/kind/cluster.yaml",
			"dev/kind/gateway.yaml",
			"dev/kind/gitea.yaml",
			"dev/kind/oidc.yaml",
			"dev/kind/routes.yaml",
			"dev/kind/alloy.yaml",
			"dev/kind/values.yaml",
		} {
			Expect(upSection).To(ContainSubstring(manifest), "up's code path does not reference %s", manifest)
		}
	})

	It("applies the Alloy agents only after the chart is installed and seeded", func() {
		// Alloy's remotecfg block resolves the `shepherd` Service on its
		// initial load and exits when the lookup fails; agents applied before
		// install_shepherd crash-loop until the restart backoff happens to
		// line up with the Service appearing. Seen live on the first cold
		// `make dev-kind` (2026-09-14): five restarts per agent. The up
		// sequence must therefore reach install_shepherd before it applies
		// dev/kind/alloy.yaml.
		//
		// Red run against the pre-fix script (alloy applied inside
		// apply_workload_manifests, before install_shepherd):
		//   Expected <string>: (cmd_up's body: ensure_cluster, install_calico, ...)
		//   to contain substring <string>: "apply_alloy_agents"
		content := readRepoFile(devKindScriptRel)
		up := funcBody(content, "cmd_up")
		Expect(up).To(ContainSubstring("install_shepherd"))
		Expect(up).To(ContainSubstring("apply_alloy_agents"))
		Expect(strings.Index(up, "apply_alloy_agents")).To(BeNumerically(">", strings.Index(up, "install_shepherd")),
			"cmd_up must install the chart before applying the Alloy agents")
		// ...and seed before them too: the seed creates the static agent token
		// the dev/*.alloy configs authenticate with, and an agent that polls
		// before it exists exits on "unauthenticated" (second cold bring-up).
		Expect(up).To(ContainSubstring("cmd_seed"))
		Expect(strings.Index(up, "apply_alloy_agents")).To(BeNumerically(">", strings.Index(up, "cmd_seed")),
			"cmd_up must run the seed before applying the Alloy agents")
		Expect(funcBody(content, "apply_alloy_agents")).To(ContainSubstring("dev/kind/alloy.yaml"))
		Expect(funcBody(content, "apply_workload_manifests")).NotTo(ContainSubstring("alloy.yaml"),
			"the pre-chart workload apply must not include the Alloy agents")
	})

	It("find_ngf_service polls for the Service instead of checking once", func() {
		// The label-selected Service can briefly lag Gateway Programmed=True —
		// e2e/k8s/route_conformance_test.go's waitProvisionedServiceName
		// documents exactly this race and retries every 2s for up to
		// gatewayReadyDeadline rather than checking once. A one-shot check
		// here (the pre-fix shape) makes a cold `up` die non-deterministically
		// right after Calico/NGF/CNPG are already installed.
		//
		// Red run, 2026-09-15, against the pre-fix one-shot find_ngf_service:
		//
		//	find_ngf_service must retry (sleep) instead of checking the
		//	Service count once: ... to match regular expression \bsleep\b
		//
		// (66 Passed | 1 Failed overall).
		content := readRepoFile(devKindScriptRel)
		body := funcBody(content, "find_ngf_service")
		Expect(body).To(MatchRegexp(`\bsleep\b`),
			"find_ngf_service must retry (sleep) instead of checking the Service count once: %s", body)
	})
})

// verbBody returns the body of one of the script's cmd_<verb> functions
// (up/reload/seed/status/down), i.e. everything between its opening and
// closing brace, so specs can assert on one verb without matching text that
// happens to live in another.
func verbBody(content, verb string) string {
	GinkgoHelper()
	re := regexp.MustCompile(`(?s)cmd_` + verb + `\s*\(\)\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(content)
	Expect(m).NotTo(BeNil(), "could not find cmd_%s() in scripts/dev-kind.sh", verb)
	return m[1]
}

// funcBody returns the body of a plain shell function `<name>() { ... }`
// (anything other than one of the cmd_<verb> functions verbBody handles),
// for specs that need to assert on one helper's own text in isolation.
func funcBody(content, name string) string {
	GinkgoHelper()
	re := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(name) + `\s*\(\)\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(content)
	Expect(m).NotTo(BeNil(), "could not find %s() in scripts/dev-kind.sh", name)
	return m[1]
}

// createSecretStatement returns the full `create secret ... shepherd-dev-env`
// statement, joining every backslash-continued line so a spec can inspect
// the whole kubectl invocation (flags included) rather than one physical
// line at a time.
func createSecretStatement(content string) string {
	GinkgoHelper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "create secret generic shepherd-dev-env") {
			start = i
			break
		}
	}
	Expect(start).To(BeNumerically(">=", 0), "could not find the shepherd-dev-env create secret statement")

	var stmt []string
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		stmt = append(stmt, lines[i])
		if !strings.HasSuffix(trimmed, `\`) {
			break
		}
	}
	return strings.Join(stmt, "\n")
}

var _ = Describe("dev/kind/cluster.yaml", func() {
	It("has exactly one node, disables the default CNI, and maps 30080 to host port 80", func() {
		var doc struct {
			Kind       string `yaml:"kind"`
			APIVersion string `yaml:"apiVersion"`
			Networking struct {
				DisableDefaultCNI bool   `yaml:"disableDefaultCNI"`
				PodSubnet         string `yaml:"podSubnet"`
			} `yaml:"networking"`
			Nodes []struct {
				Role              string `yaml:"role"`
				ExtraPortMappings []struct {
					ContainerPort int    `yaml:"containerPort"`
					HostPort      int    `yaml:"hostPort"`
					ListenAddress string `yaml:"listenAddress"`
					Protocol      string `yaml:"protocol"`
				} `yaml:"extraPortMappings"`
			} `yaml:"nodes"`
		}
		loadYAML("dev/kind/cluster.yaml", &doc)

		Expect(doc.Kind).To(Equal("Cluster"))
		Expect(doc.APIVersion).To(Equal("kind.x-k8s.io/v1alpha4"))
		Expect(doc.Networking.DisableDefaultCNI).To(BeTrue())
		Expect(doc.Networking.PodSubnet).To(Equal("10.244.0.0/16"))
		Expect(doc.Nodes).To(HaveLen(1))
		Expect(doc.Nodes[0].Role).To(Equal("control-plane"))

		var foundMapping bool
		for _, m := range doc.Nodes[0].ExtraPortMappings {
			if m.ContainerPort == 30080 && m.HostPort == 80 {
				foundMapping = true
			}
		}
		Expect(foundMapping).To(BeTrue(), "no extraPortMappings entry maps containerPort 30080 to hostPort 80")
	})

	It("also parses cleanly with yaml.v3's generic decoder (no anchors/tags kind cannot read)", func() {
		var generic map[string]any
		loadYAML("dev/kind/cluster.yaml", &generic)
		Expect(generic).NotTo(BeEmpty())
	})
})
