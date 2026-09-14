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

// bareKubectlHelmRE matches a line invoking kubectl or helm directly
// (anything but through the kc()/hm() wrappers).
var bareKubectlHelmRE = regexp.MustCompile(`(?m)^\s*(kubectl|helm)\s`)

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
