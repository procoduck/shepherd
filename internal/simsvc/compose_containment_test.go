package simsvc

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// composeFile is the slice of a compose document this spec cares about.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeService struct {
	Image        string            `yaml:"image"`
	Profiles     []string          `yaml:"profiles"`
	User         string            `yaml:"user"`
	ReadOnly     bool              `yaml:"read_only"`
	CapDrop      []string          `yaml:"cap_drop"`
	SecurityOpt  []string          `yaml:"security_opt"`
	MemLimit     string            `yaml:"mem_limit"`
	MemswapLimit string            `yaml:"memswap_limit"`
	CPUs         float64           `yaml:"cpus"`
	PidsLimit    int               `yaml:"pids_limit"`
	Tmpfs        []string          `yaml:"tmpfs"`
	Ports        []any             `yaml:"ports"`
	Networks     []string          `yaml:"networks"`
	Environment  map[string]string `yaml:"environment"`
	Healthcheck  struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

type composeNetwork struct {
	Name     string `yaml:"name"`
	Internal bool   `yaml:"internal"`
}

func loadCompose(path string) composeFile {
	GinkgoHelper()
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var doc composeFile
	Expect(yaml.Unmarshal(raw, &doc)).To(Succeed())
	return doc
}

// A containment claim nobody checks is how these regress silently: every key
// asserted below is one whose removal restores a capability the sandbox is
// supposed to have lost, and every assertion fails if the key is dropped.
//
// This spec pins the DECLARATION, and only the declaration. When it was
// written, the sentence here claimed the e2e suite asserted the observable
// EFFECT — and no such spec existed, which finding H4 caught: the only
// evidence for egress denial was a hand-run transcript in a proof document.
// It exists now, by name:
//
//	e2e/sandbox_egress_test.go, Label("sandbox-egress"), run by `make e2e-egress`
//	from .github/workflows/e2e.yml's e2e-egress job.
//
// P-control dials a canary on the ordinary network and must succeed; P-deny-ip
// dials the same canary's literal address from the simulator's own network
// namespace and must fail; P-topology reads Internal=true back from the running
// daemon rather than from the YAML this file parses. The two specs are
// complementary and neither substitutes for the other — this one runs on every
// `go test` with no Docker daemon, and it is the only one that would notice a
// key deleted from a compose file nobody brought up.
var _ = DescribeTable("Compose containment for the simulator service",
	func(relPath string) {
		root := repoRoot()
		doc := loadCompose(filepath.Join(root, relPath))

		sim, ok := doc.Services["simulator"]
		Expect(ok).To(BeTrue(), "%s declares no simulator service", relPath)

		// Opt-in: the simulator executes user-authored configuration, so it
		// must never come up as part of the default stack.
		Expect(sim.Profiles).To(ContainElement("sim"))

		// Non-root with no capabilities and no way to acquire any.
		Expect(sim.User).To(Equal("65532:65532"))
		Expect(sim.CapDrop).To(ConsistOf("ALL"))
		Expect(sim.SecurityOpt).To(ContainElement("no-new-privileges:true"))

		// Read-only rootfs; the only writable path is a size-capped tmpfs
		// mounted noexec/nosuid/nodev.
		Expect(sim.ReadOnly).To(BeTrue())
		Expect(sim.Tmpfs).NotTo(BeEmpty())
		var tmp string
		for _, mount := range sim.Tmpfs {
			if strings.HasPrefix(mount, "/tmp:") {
				tmp = mount
			}
		}
		Expect(tmp).NotTo(BeEmpty(), "no tmpfs mounted at /tmp; a read-only rootfs leaves the run nowhere to write")
		Expect(tmp).To(ContainSubstring("noexec"))
		Expect(tmp).To(ContainSubstring("nosuid"))
		Expect(tmp).To(ContainSubstring("nodev"))
		Expect(tmp).To(ContainSubstring("size="))

		// memswap_limit is not redundant: mem_limit alone lets a run swap past
		// its cap.
		Expect(sim.MemLimit).To(Equal("512m"))
		Expect(sim.MemswapLimit).To(Equal("512m"))
		Expect(sim.CPUs).To(Equal(1.0))
		Expect(sim.PidsLimit).To(Equal(256))

		// Nothing published to the host, and the only network it joins is the
		// internal one.
		Expect(sim.Ports).To(BeEmpty(), "the simulator must publish no host ports")
		Expect(sim.Networks).To(ConsistOf("sim-internal"))

		// The distroless image has no shell, so the healthcheck has to be the
		// binary's own subcommand.
		Expect(len(sim.Healthcheck.Test)).To(BeNumerically(">=", 3))
		Expect(sim.Healthcheck.Test[:3]).To(Equal([]string{"CMD", "/usr/local/bin/shepherd-simulator", "healthcheck"}))

		// internal: true is the control that actually denies egress. Without
		// it the network gets a gateway and every config a user runs regains
		// full outbound access.
		simNet, ok := doc.Networks["sim-internal"]
		Expect(ok).To(BeTrue(), "%s declares no sim-internal network", relPath)
		Expect(simNet.Internal).To(BeTrue(), "sim-internal must be internal: true or the sandbox can reach the internet")

		// Shepherd has to be ON that network to drive the simulator; if it is
		// not, the whole feature is unreachable and the containment above is
		// decoration.
		shepherd, ok := doc.Services["shepherd"]
		Expect(ok).To(BeTrue())
		Expect(shepherd.Networks).To(ContainElements("default", "sim-internal"))

		// The feature is off unless the operator turns it on, so the default
		// stack is what exercises the "simulator not configured" path.
		Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SIMULATOR_ENABLED"))
		Expect(shepherd.Environment["SHEPHERD_SIMULATOR_ENABLED"]).To(ContainSubstring(":-false"))
	},
	Entry("dev stack", "dev/docker-compose.dev.yaml"),
	Entry("e2e stack", "e2e/docker-compose.e2e.yaml"),
)

var _ = Describe("Simulator Dockerfile", func() {
	It("pins its base images from ARGs rather than a second copy of the version", func() {
		raw, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "Dockerfile.simulator"))
		Expect(err).NotTo(HaveOccurred())
		content := string(raw)

		// A hardcoded FROM is how a sandbox ends up running a different Alloy
		// than the validation gate, which would make S3 results lie about what
		// the fleet will do. `make check-docker` enforces the same rule.
		Expect(content).To(ContainSubstring("FROM ${ALLOY_IMAGE} AS alloy"))
		// distroless/BASE, not /static: Alloy is dynamically linked and a static
		// base cannot exec it at all.
		Expect(content).To(ContainSubstring("FROM ${DISTROLESS_BASE_IMAGE}"))
		Expect(content).To(ContainSubstring("FROM ${GO_IMAGE} AS builder"))
		Expect(content).To(ContainSubstring("USER nonroot"))
		Expect(content).NotTo(ContainSubstring("COPY --from=web"), "the simulator image must not carry the SPA")
	})

	It("keeps its ARG defaults in step with deploy/versions.env", func() {
		versions, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "versions.env"))
		Expect(err).NotTo(HaveOccurred())
		dockerfile, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "Dockerfile.simulator"))
		Expect(err).NotTo(HaveOccurred())

		for _, key := range []string{"GO_IMAGE", "ALLOY_IMAGE", "DISTROLESS_BASE_IMAGE"} {
			pinned := valueFromEnvFile(string(versions), key)
			Expect(pinned).NotTo(BeEmpty())
			Expect(string(dockerfile)).To(ContainSubstring("ARG " + key + "=" + pinned))
		}
	})
})

// repoRoot walks up from the package directory to the module root, so the
// specs address the compose files by their real repo paths.
func repoRoot() string {
	GinkgoHelper()
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for range 10 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	Fail("could not find the module root from " + dir)
	return ""
}

func valueFromEnvFile(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return value
		}
	}
	return ""
}
