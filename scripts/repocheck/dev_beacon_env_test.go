package repocheck_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The beacon baseline Shepherd appends to every served config authenticates
// its remote_write with sys.env("SHEPHERD_AGENT_TOKEN_ID") /
// sys.env("SHEPHERD_AGENT_TOKEN_SECRET") (internal/beacon). The 0.22 -> 0.23
// upgrade walkthrough found no dev or e2e Alloy setting them, so on every
// local stack the beacon never authenticated and the Reconciliation tab had
// no observed data. Each stack's agents now take the token from one source:
// on kind the alloy-agent-token Secret (dev/kind/alloy.yaml), which the
// remotecfg block reads too; on compose the same env, set per service.

const (
	beaconTokenIDEnv     = "SHEPHERD_AGENT_TOKEN_ID"
	beaconTokenSecretEnv = "SHEPHERD_AGENT_TOKEN_SECRET"
)

type kindSecretEnvDeployment struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	StringData map[string]string `yaml:"stringData"`
	Spec       struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `yaml:"name"`
					Env  []struct {
						Name      string `yaml:"name"`
						ValueFrom *struct {
							SecretKeyRef *struct {
								Name string `yaml:"name"`
								Key  string `yaml:"key"`
							} `yaml:"secretKeyRef"`
						} `yaml:"valueFrom"`
					} `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type alloyComposeFile struct {
	Services map[string]struct {
		Environment map[string]string `yaml:"environment"`
		Volumes     []string          `yaml:"volumes"`
	} `yaml:"services"`
}

var basicAuthRe = regexp.MustCompile(`username\s*=\s*"([^"]+)"\s*\n\s*password\s*=\s*"([^"]+)"`)

var _ = Describe("dev and e2e Alloy agents carry the beacon's token env", func() {
	It("kind: every Alloy Deployment reads both vars from the alloy-agent-token Secret", func() {
		var secret *kindSecretEnvDeployment
		var deployments []kindSecretEnvDeployment
		for _, doc := range loadYAMLDocs("dev/kind/alloy.yaml") {
			var d kindSecretEnvDeployment
			Expect(doc.Decode(&d)).To(Succeed())
			switch d.Kind {
			case "Secret":
				if d.Metadata.Name == "alloy-agent-token" {
					secret = &d
				}
			case "Deployment":
				deployments = append(deployments, d)
			}
		}
		Expect(secret).NotTo(BeNil(), "dev/kind/alloy.yaml defines no alloy-agent-token Secret")
		Expect(secret.StringData).To(HaveKey(beaconTokenIDEnv))
		Expect(secret.StringData).To(HaveKey(beaconTokenSecretEnv))
		Expect(deployments).To(HaveLen(3))
		for _, d := range deployments {
			refs := map[string]string{}
			for _, c := range d.Spec.Template.Spec.Containers {
				for _, e := range c.Env {
					if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "alloy-agent-token" {
						refs[e.Name] = e.ValueFrom.SecretKeyRef.Key
					}
				}
			}
			Expect(refs).To(HaveKeyWithValue(beaconTokenIDEnv, beaconTokenIDEnv), d.Metadata.Name)
			Expect(refs).To(HaveKeyWithValue(beaconTokenSecretEnv, beaconTokenSecretEnv), d.Metadata.Name)
		}
	})

	It("dev: the remotecfg block reads the same env, so the token has one source per stack", func() {
		entries, err := os.ReadDir(filepath.Join(repoRoot(), "dev"))
		Expect(err).NotTo(HaveOccurred())
		n := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "alloy-") || filepath.Ext(e.Name()) != ".alloy" {
				continue
			}
			n++
			content := readRepoFile(filepath.Join("dev", e.Name()))
			Expect(content).To(ContainSubstring(`username = sys.env("`+beaconTokenIDEnv+`")`), e.Name())
			Expect(content).To(ContainSubstring(`password = sys.env("`+beaconTokenSecretEnv+`")`), e.Name())
		}
		Expect(n).To(BeNumerically(">", 0))
	})

	It("dev compose: every Alloy service sets both vars to the kind Secret's (the dev seed's) token", func() {
		var secret kindSecretEnvDeployment
		for _, doc := range loadYAMLDocs("dev/kind/alloy.yaml") {
			var d kindSecretEnvDeployment
			Expect(doc.Decode(&d)).To(Succeed())
			if d.Kind == "Secret" && d.Metadata.Name == "alloy-agent-token" {
				secret = d
			}
		}
		var compose alloyComposeFile
		loadYAML("dev/docker-compose.dev.yaml", &compose)
		n := 0
		for name, svc := range compose.Services {
			if !strings.HasPrefix(name, "alloy-") {
				continue
			}
			n++
			Expect(svc.Environment).To(HaveKeyWithValue(beaconTokenIDEnv, secret.StringData[beaconTokenIDEnv]), name)
			Expect(svc.Environment).To(HaveKeyWithValue(beaconTokenSecretEnv, secret.StringData[beaconTokenSecretEnv]), name)
		}
		Expect(n).To(Equal(3))
	})

	It("e2e compose: the Alloy service sets both vars to the token alloy/config.alloy authenticates with", func() {
		m := basicAuthRe.FindStringSubmatch(readRepoFile("e2e/alloy/config.alloy"))
		Expect(m).To(HaveLen(3), "e2e/alloy/config.alloy has no literal basic_auth")
		var compose alloyComposeFile
		loadYAML("e2e/docker-compose.e2e.yaml", &compose)
		Expect(compose.Services).To(HaveKey("alloy"))
		Expect(compose.Services["alloy"].Environment).To(HaveKeyWithValue(beaconTokenIDEnv, m[1]))
		Expect(compose.Services["alloy"].Environment).To(HaveKeyWithValue(beaconTokenSecretEnv, m[2]))
	})
})
