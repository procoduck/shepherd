package helm_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// renderFailure runs `helm template` expecting it NOT to succeed, and returns
// helm's output so a test can assert on the message. renderWith (in
// chart_observability_test.go) fails the spec on a non-zero exit, which is the
// right default everywhere except here: two of these values combinations are
// SUPPOSED to be refused, and a guard rail with no test is a comment.
func renderFailure(values string) string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	path := filepath.Join(dir, "values.yaml")
	Expect(os.WriteFile(path, []byte(values), 0o600)).To(Succeed())

	cmd := exec.Command("helm", "template", "shepherd", "shepherd",
		"-f", "shepherd/ci/default-values.yaml", "-f", path)
	out, err := cmd.CombinedOutput()
	Expect(err).To(HaveOccurred(), "expected helm to refuse these values, got:\n%s", out)
	return string(out)
}

func envOf(container map[string]any) map[string]any {
	out := map[string]any{}
	raw, _ := container["env"].([]any) //nolint:errcheck // absent env is an empty map, which is a valid answer
	for _, e := range raw {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string) //nolint:errcheck // same
		out[name] = entry
	}
	return out
}

// Both of these render CRDs this chart does not own, against operators it does
// not install. `helm template` is therefore the only place their shape can be
// checked at all -- nothing else in CI has a CloudNativePG or an External
// Secrets Operator to talk to -- so the assertions here are deliberately about
// the fields that decide whether the result is CORRECT rather than merely
// well-formed: which key the database URL is read from, and whether the
// generated encryption key can ever be regenerated.
var _ = Describe("Helm chart: optional dependencies", func() {
	Describe("CloudNativePG", func() {
		It("renders nothing at all by default", func() {
			objects := renderWith("existingSecret: shepherd-secrets\n")
			for key := range objects {
				Expect(key).NotTo(HavePrefix("Cluster/"), "a database appeared without being asked for")
			}
		})

		It("provisions a Cluster and reads the URL from the operator's own secret", func() {
			objects := renderWith("cnpg:\n  enabled: true\n")

			cluster, ok := objects["Cluster/shepherd-db"]
			Expect(ok).To(BeTrue(), "cnpg.enabled did not render a Cluster")
			Expect(cluster["apiVersion"]).To(Equal("postgresql.cnpg.io/v1"))

			spec, ok := cluster["spec"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(spec).To(HaveKey("instances"))
			initdb, ok := spec["bootstrap"].(map[string]any)["initdb"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(initdb["database"]).To(Equal("shepherd"))

			// The whole point of the integration: nobody writes a connection
			// string. CNPG publishes "<cluster>-app" with a "uri" key, and if
			// either name is wrong the pod starts and fails to reach a
			// database that is running perfectly well.
			for _, workload := range []string{"Deployment/shepherd", "Job/shepherd-migrate"} {
				obj, found := objects[workload]
				Expect(found).To(BeTrue(), "%s missing", workload)
				name := "shepherd"
				if strings.HasPrefix(workload, "Job/") {
					name = "migrate"
				}
				env := envOf(containerOf(obj, name))
				Expect(env).To(HaveKey("SHEPHERD_DATABASE_URL"), "%s never learns where the database is", workload)
				entry, ok := env["SHEPHERD_DATABASE_URL"].(map[string]any)
				Expect(ok).To(BeTrue())
				valueFrom, ok := entry["valueFrom"].(map[string]any)
				Expect(ok).To(BeTrue(), "the URL is a literal, not a reference to the operator's secret")
				ref, ok := valueFrom["secretKeyRef"].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(ref["name"]).To(Equal("shepherd-db-app"))
				Expect(ref["key"]).To(Equal("uri"))
			}
		})

		It("exists before the migration Job, which is the only reason it can work", func() {
			objects := renderWith("cnpg:\n  enabled: true\n")
			meta, ok := objects["Cluster/shepherd-db"]["metadata"].(map[string]any)
			Expect(ok).To(BeTrue())
			ann, ok := meta["annotations"].(map[string]any)
			Expect(ok).To(BeTrue(), "the Cluster is not a hook, so it is created after the Job that needs it")
			Expect(ann["helm.sh/hook"]).To(Equal("pre-install,pre-upgrade"))
			// -20 is ahead of the config/secret at -10 and the Job at -5.
			Expect(ann["helm.sh/hook-weight"]).To(Equal("-20"))
			// A delete policy here would drop the database between releases.
			Expect(ann).NotTo(HaveKey("helm.sh/hook-delete-policy"))
		})

		It("lets the user keep their own database", func() {
			objects := renderWith("existingSecret: my-rds\n")
			env := envOf(containerOf(objects["Deployment/shepherd"], "shepherd"))
			Expect(env).NotTo(HaveKey("SHEPHERD_DATABASE_URL"),
				"the chart overrode a database URL it was never given")
		})
	})

	Describe("External Secrets", func() {
		It("renders nothing at all by default", func() {
			objects := renderWith("existingSecret: shepherd-secrets\n")
			for key := range objects {
				Expect(key).NotTo(HavePrefix("ExternalSecret/"))
				Expect(key).NotTo(HavePrefix("Password/"))
			}
		})

		It("generates the bootstrap secret into the name the workloads already read", func() {
			objects := renderWith("externalSecrets:\n  enabled: true\n")

			es, ok := objects["ExternalSecret/shepherd-bootstrap"]
			Expect(ok).To(BeTrue())
			Expect(es["apiVersion"]).To(Equal("external-secrets.io/v1"))

			spec, ok := es["spec"].(map[string]any)
			Expect(ok).To(BeTrue())
			target, ok := spec["target"].(map[string]any)
			Expect(ok).To(BeTrue())
			// If this name is wrong the operator cheerfully creates a Secret
			// nothing mounts, and Shepherd fails to start for want of a key
			// that exists.
			Expect(target["name"]).To(Equal("shepherd-secrets"))

			data, ok := target["template"].(map[string]any)["data"].(map[string]any)
			Expect(ok).To(BeTrue())
			// Shepherd wants base64 of 32 bytes; the generator emits 32
			// characters, so the b64enc has to be here or the key is rejected.
			Expect(data["SHEPHERD_SECURITY_ENCRYPTION_KEY"]).To(ContainSubstring("b64enc"))
			Expect(data).To(HaveKey("SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD"))
		})

		It("never regenerates, and survives deletion", func() {
			objects := renderWith("externalSecrets:\n  enabled: true\n")
			spec, ok := objects["ExternalSecret/shepherd-bootstrap"]["spec"].(map[string]any)
			Expect(ok).To(BeTrue())

			// Rotating the encryption key silently orphans every git
			// credential and OIDC client secret already in the database.
			// There is no error at the moment it happens, which is exactly
			// why this is asserted rather than left to a comment.
			Expect(spec["refreshInterval"]).To(Equal("0"))
			target, ok := spec["target"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(target["deletionPolicy"]).To(Equal("Retain"))
		})

		It("refuses to render alongside a hand-written secret rather than fighting over it", func() {
			out := renderFailure("externalSecrets:\n  enabled: true\nsecrets:\n  SHEPHERD_DATABASE_URL: postgres://x\n")
			Expect(out).To(ContainSubstring("Pick one"))

			out = renderFailure("externalSecrets:\n  enabled: true\nexistingSecret: mine\n")
			Expect(out).To(ContainSubstring("Pick one"))
		})
	})

	It("retries the migration Job, which is what makes a provisioned database work", func() {
		objects := renderWith("cnpg:\n  enabled: true\n")
		spec, ok := objects["Job/shepherd-migrate"]["spec"].(map[string]any)
		Expect(ok).To(BeTrue())
		// On a first install Postgres is still coming up while this Job runs.
		// Without retries the release fails on a database that was merely slow.
		Expect(spec["backoffLimit"]).To(BeNumerically(">=", 3))
	})
})
