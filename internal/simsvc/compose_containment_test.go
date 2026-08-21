package simsvc

import (
	"fmt"
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
	Networks     composeNetworks   `yaml:"networks"`
	Environment  map[string]string `yaml:"environment"`
	Healthcheck  struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

type composeNetwork struct {
	Name     string `yaml:"name"`
	Internal bool   `yaml:"internal"`
}

// composeNetworkAttachment is the value half of the compose "long form" for a
// service's network attachment (services.<name>.networks.<net>: {ipv4_address: ...}).
// The short form (services.<name>.networks: [default, sim-internal]) carries no
// per-network data at all — composeNetworks.UnmarshalYAML normalizes both into
// the same map so callers don't care which form a given service used.
type composeNetworkAttachment struct {
	Ipv4Address string `yaml:"ipv4_address"`
}

// composeNetworks decodes either YAML shape compose accepts for a service's
// `networks:` key: the short list form (a bare sequence of network names) or
// the long map form (network name -> attachment object, which is how a static
// ipv4_address gets pinned). Both are valid compose YAML; B-CONTAIN-1 needs
// shepherd on the long form so its per-network IP is assertable, but other
// services (e.g. simulator) still use the short form, so the loader has to
// accept both.
type composeNetworks map[string]composeNetworkAttachment

func (n *composeNetworks) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := value.Decode(&names); err != nil {
			return err
		}
		out := make(composeNetworks, len(names))
		for _, name := range names {
			out[name] = composeNetworkAttachment{}
		}
		*n = out
		return nil
	case yaml.MappingNode:
		var attachments map[string]composeNetworkAttachment
		if err := value.Decode(&attachments); err != nil {
			return err
		}
		*n = attachments
		return nil
	default:
		return fmt.Errorf("composeNetworks: unsupported YAML node kind %v for networks", value.Kind)
	}
}

// Names returns the attached network names, order-independent — the shape
// Gomega's ConsistOf/ContainElements matchers expect in place of the raw map.
func (n composeNetworks) Names() []string {
	names := make([]string, 0, len(n))
	for name := range n {
		names = append(names, name)
	}
	return names
}

// IPv4 returns the pinned ipv4_address for the given network, or "" if the
// service isn't attached to it or used the short form (no address pinned).
func (n composeNetworks) IPv4(network string) string {
	return n[network].Ipv4Address
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
		Expect(sim.Networks.Names()).To(ConsistOf("sim-internal"))

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
		Expect(shepherd.Networks.Names()).To(ContainElements("default", "sim-internal"))

		// The feature is off unless the operator turns it on, so the default
		// stack is what exercises the "simulator not configured" path.
		Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SIMULATOR_ENABLED"))
		Expect(shepherd.Environment["SHEPHERD_SIMULATOR_ENABLED"]).To(ContainSubstring(":-false"))

		// B-CONTAIN-1: shepherd must not LISTEN on sim-internal even though it
		// is a member of it — a control that removal of these two env vars (or
		// reverting them to a bare ":port") silently defeats.
		Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SERVER_LISTEN"))
		Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SERVER_METRICS_LISTEN"))
		listenAddr := shepherd.Environment["SHEPHERD_SERVER_LISTEN"]
		metricsAddr := shepherd.Environment["SHEPHERD_SERVER_METRICS_LISTEN"]
		Expect(listenAddr).NotTo(HavePrefix(":"), "a bare :port binds every interface, including sim-internal")
		Expect(metricsAddr).NotTo(HavePrefix(":"), "same — this is the exact leak B-CONTAIN-1 names")
		Expect(listenAddr).NotTo(HavePrefix("0.0.0.0"))
		Expect(metricsAddr).NotTo(HavePrefix("0.0.0.0"))

		// The bound host must equal shepherd's OWN default-network address, and
		// that address must differ from its sim-internal address — otherwise
		// this whole check is checking nothing.
		defaultIP := shepherd.Networks.IPv4("default")
		simIP := shepherd.Networks.IPv4("sim-internal")
		Expect(defaultIP).NotTo(BeEmpty(), "%s: shepherd has no pinned ipv4_address on default", relPath)
		Expect(simIP).NotTo(BeEmpty(), "%s: shepherd has no pinned ipv4_address on sim-internal", relPath)
		Expect(simIP).NotTo(Equal(defaultIP), "shepherd's default and sim-internal addresses must differ")
		Expect(listenAddr).To(HavePrefix(defaultIP+":"), "SHEPHERD_SERVER_LISTEN must bind shepherd's default-network address")
		Expect(metricsAddr).To(HavePrefix(defaultIP+":"), "SHEPHERD_SERVER_METRICS_LISTEN must bind shepherd's default-network address")

		// The healthcheck dials the same literal shepherd binds: with a
		// specific-IP bind there is no loopback listener, so a renumbered pin
		// that misses the healthcheck line would only surface as the container
		// going permanently unhealthy at `up` time with nothing naming the
		// cause. Tie the two here instead.
		Expect(shepherd.Healthcheck.Test).To(ContainElement(listenAddr),
			"shepherd's healthcheck --addr must equal SHEPHERD_SERVER_LISTEN — a specific-IP bind has no loopback listener")
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
