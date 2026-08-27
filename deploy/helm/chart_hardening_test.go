package helm_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// renderNotes returns the NOTES.txt output for a release name and values.
//
// `helm template` does not render NOTES at all, which is exactly why nobody
// noticed that the collector URL it prints was hardcoded to a release named
// "shepherd" on port 8080. --dry-run=client does render them, and takes a real
// release name, so the templating can actually be checked.
func renderNotes(release, values string) string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	path := filepath.Join(dir, "values.yaml")
	Expect(os.WriteFile(path, []byte(values), 0o600)).To(Succeed())

	cmd := exec.Command("helm", "install", release, "shepherd", "--dry-run=client",
		"-f", "shepherd/ci/default-values.yaml", "-f", path)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "helm install --dry-run failed:\n%s", out)

	_, notes, found := strings.Cut(string(out), "NOTES:")
	Expect(found).To(BeTrue(), "no NOTES section in:\n%s", out)
	return notes
}

// annotationsOf returns an object's metadata.annotations, or an empty map.
func annotationsOf(obj map[string]any) map[string]any {
	meta, _ := obj["metadata"].(map[string]any) //nolint:errcheck // absent metadata yields an empty map, which every caller reads as "no annotations"
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		return map[string]any{}
	}
	return ann
}

// podTemplateLabelsOf returns spec.template.metadata.labels.
func podTemplateLabelsOf(obj map[string]any) map[string]any {
	spec, _ := obj["spec"].(map[string]any)          //nolint:errcheck // same
	template, _ := spec["template"].(map[string]any) //nolint:errcheck // same
	meta, _ := template["metadata"].(map[string]any) //nolint:errcheck // same
	labels, _ := meta["labels"].(map[string]any)     //nolint:errcheck // same
	if labels == nil {
		return map[string]any{}
	}
	return labels
}

// Every assertion here replaces a defect that `helm lint` reported nothing
// about and that no render test would have noticed, because each one is a
// MISSING field rather than a wrong one. A chart is a pile of optional keys;
// absence is its characteristic failure, and absence is invisible unless
// something looks for it on purpose.
var _ = Describe("chart hardening", func() {
	Describe("the migration Job", func() {
		// Proven against a real cluster: the old shape (container-level
		// securityContext only) is refused outright by a namespace enforcing
		// the `restricted` Pod Security Standard --
		//
		//   pods "shepherd-migrate-xxxxx" is forbidden: violates PodSecurity
		//   "restricted:latest": runAsNonRoot != true, seccompProfile ...
		//
		// -- and because the Job is a pre-install hook, that is not a failed
		// migration, it is a failed INSTALL. The Deployment it was migrating
		// for would have been admitted perfectly well.
		It("satisfies restricted Pod Security, which its pre-install hook status makes load-bearing", func() {
			job := renderWith("cnpg:\n  enabled: true\n")["Job/shepherd-migrate"]
			Expect(job).NotTo(BeNil())

			pod := podSpecOf(job)
			sc, ok := pod["securityContext"].(map[string]any)
			Expect(ok).To(BeTrue(), "the migration pod has no pod-level securityContext at all")
			Expect(sc["runAsNonRoot"]).To(BeTrue())
			Expect(sc["runAsUser"]).NotTo(BeNil())
			Expect(sc["runAsUser"]).NotTo(Equal(0), "a high UID, so it cannot collide with a host user")
			seccomp, ok := sc["seccompProfile"].(map[string]any)
			Expect(ok).To(BeTrue(), "no seccompProfile: restricted admission refuses the pod")
			Expect(seccomp["type"]).To(Equal("RuntimeDefault"))

			// The pod-level context must not have come at the cost of the
			// container-level one.
			csc, ok := containerOf(job, "migrate")["securityContext"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(csc["readOnlyRootFilesystem"]).To(BeTrue())
			Expect(csc["allowPrivilegeEscalation"]).To(BeFalse())
		})

		// The Deployment honoured image.pullSecrets and the Job did not, so a
		// private-registry install died in ImagePullBackOff at the pre-install
		// hook -- surfacing minutes later as a --wait timeout, before a single
		// tracked resource existed.
		It("pulls with the same credentials as the Deployment", func() {
			objects := renderWith("image:\n  pullSecrets:\n    - name: regcred\n")
			for _, workload := range []string{"Deployment/shepherd", "Job/shepherd-migrate"} {
				pod := podSpecOf(objects[workload])
				secrets, ok := pod["imagePullSecrets"].([]any)
				Expect(ok).To(BeTrue(), "%s ignores image.pullSecrets, so it cannot pull from a private registry", workload)
				Expect(secrets).To(HaveLen(1))
			}
		})

		It("labels its pods so a failed migration can be found", func() {
			job := renderWith("cnpg:\n  enabled: true\n")["Job/shepherd-migrate"]
			labels := podTemplateLabelsOf(job)
			// Without these the pods carry only batch.kubernetes.io/* and are
			// invisible to `kubectl get pods -l app.kubernetes.io/instance=...`
			// exactly when someone is hunting for why an upgrade failed.
			Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "shepherd"))
			Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/component", "migrate"))
		})

		// The Job needs its config, secret and ServiceAccount to exist before
		// it runs, and Helm creates every hook before any normal resource. It
		// used to get that by making the RUNTIME objects hooks, which cost
		// rollback correctness and left credentials behind on uninstall.
		It("uses its own hook-scoped copies, leaving the runtime objects tracked", func() {
			objects := renderWith("secrets:\n  SHEPHERD_DATABASE_URL: postgres://x\n")

			for _, name := range []string{
				"ConfigMap/shepherd-migrate",
				"Secret/shepherd-migrate-secrets",
				"ServiceAccount/shepherd-migrate",
			} {
				obj, found := objects[name]
				Expect(found).To(BeTrue(), "%s missing: the migration Job cannot see its own config", name)
				ann := annotationsOf(obj)
				Expect(ann["helm.sh/hook"]).To(Equal("pre-install,pre-upgrade"))
				// -10 is ahead of the Job at -5.
				Expect(ann["helm.sh/hook-weight"]).To(Equal("-10"))
				// Scratch resources: cleaned up when the migration passes,
				// cleared out of the way when it failed last time.
				Expect(ann["helm.sh/hook-delete-policy"]).To(Equal("before-hook-creation,hook-succeeded"))
			}

			// The runtime objects must be ordinary tracked resources, or
			// `helm rollback` will not revert them and `helm uninstall` will
			// leave them (and the credentials in them) behind.
			for _, name := range []string{
				"ConfigMap/shepherd",
				"Secret/shepherd-secrets",
				"ServiceAccount/shepherd",
			} {
				obj, found := objects[name]
				Expect(found).To(BeTrue(), "%s missing", name)
				Expect(annotationsOf(obj)).NotTo(HaveKey("helm.sh/hook"),
					"%s is a hook again: rollback will not revert it and uninstall will not remove it", name)
			}

			// And the Job must read the hook copy, not the tracked Secret it
			// cannot see yet.
			envFrom, _ := containerOf(objects["Job/shepherd-migrate"], "migrate")["envFrom"].([]any)
			Expect(envFrom).To(HaveLen(1))
			ref, _ := envFrom[0].(map[string]any)["secretRef"].(map[string]any)
			Expect(ref["name"]).To(Equal("shepherd-migrate-secrets"))
		})

		It("reads the operator-generated secret directly when External Secrets owns it", func() {
			// ESO creates <fullname>-secrets from a -20 hook, i.e. before this
			// Job at -5, so no copy is needed -- and copying would mean
			// rendering a Secret whose values the chart does not have.
			objects := renderWith("externalSecrets:\n  enabled: true\ncnpg:\n  enabled: true\n")
			envFrom, _ := containerOf(objects["Job/shepherd-migrate"], "migrate")["envFrom"].([]any)
			ref, _ := envFrom[0].(map[string]any)["secretRef"].(map[string]any)
			Expect(ref["name"]).To(Equal("shepherd-secrets"))
			Expect(objects).NotTo(HaveKey("Secret/shepherd-migrate-secrets"))
		})
	})

	Describe("the Deployment", func() {
		// Rotating a value in `secrets` updated the Secret and restarted
		// nothing, because env vars are read once at container start. The pods
		// kept using the old database URL until an unrelated rollout.
		It("rolls its pods when a secret value changes, not only the config", func() {
			first := renderWith("secrets:\n  SHEPHERD_DATABASE_URL: postgres://one\n")
			second := renderWith("secrets:\n  SHEPHERD_DATABASE_URL: postgres://two\n")

			annOf := func(objects map[string]map[string]any) map[string]any {
				spec, _ := objects["Deployment/shepherd"]["spec"].(map[string]any)
				template, _ := spec["template"].(map[string]any)
				meta, _ := template["metadata"].(map[string]any)
				ann, _ := meta["annotations"].(map[string]any)
				return ann
			}
			a, b := annOf(first), annOf(second)
			Expect(a).To(HaveKey("checksum/secret"))
			Expect(a["checksum/secret"]).NotTo(Equal(b["checksum/secret"]),
				"the same checksum for two different secrets: changing one will not restart the pods")

			// A hash, never the value itself.
			Expect(a["checksum/secret"]).NotTo(ContainSubstring("postgres://"))
		})

		It("runs as a ServiceAccount the operator can annotate for IRSA or Workload Identity", func() {
			objects := renderWith("serviceAccount:\n  annotations:\n    eks.amazonaws.com/role-arn: arn:aws:iam::1:role/shepherd\n")
			sa := objects["ServiceAccount/shepherd"]
			Expect(annotationsOf(sa)).To(HaveKey("eks.amazonaws.com/role-arn"))
			Expect(sa["automountServiceAccountToken"]).To(BeFalse())
		})

		It("can be pointed at a ServiceAccount the chart does not create", func() {
			objects := renderWith("serviceAccount:\n  create: false\n  name: platform-managed\n")
			Expect(objects).NotTo(HaveKey("ServiceAccount/shepherd"))
			Expect(podSpecOf(objects["Deployment/shepherd"])["serviceAccountName"]).To(Equal("platform-managed"))
			Expect(podSpecOf(objects["Job/shepherd-migrate"])["serviceAccountName"]).To(Equal("platform-managed"))
		})
	})

	Describe("availability", func() {
		// The budget used to gate on .Values.replicas, which is not the replica
		// count once an HPA owns it -- the Deployment stops rendering it. With
		// minReplicas 1 that produced a minAvailable: 1 budget that deadlocks
		// every node drain the moment the autoscaler scales to one pod.
		It("does not render a PodDisruptionBudget that can deadlock a drain", func() {
			objects := renderWith("replicas: 2\nresources:\n  requests:\n    cpu: 100m\nautoscaling:\n  enabled: true\n  minReplicas: 1\n")
			Expect(objects).NotTo(HaveKey("PodDisruptionBudget/shepherd"),
				"minAvailable: 1 with a floor of 1 pod means no voluntary eviction can ever proceed")
		})

		It("renders one when the autoscaler's floor leaves room to evict", func() {
			objects := renderWith("replicas: 1\nresources:\n  requests:\n    cpu: 100m\nautoscaling:\n  enabled: true\n  minReplicas: 3\n")
			Expect(objects).To(HaveKey("PodDisruptionBudget/shepherd"),
				"three pods and no budget: a drain can take them all at once")
		})

		// A CPU-utilization HPA is a percentage OF THE REQUEST. resources is {}
		// by default, so enabling autoscaling on the defaults produced an HPA
		// stuck in FailedGetResourceMetric forever -- indistinguishable from an
		// autoscaler that has simply decided not to scale.
		It("refuses an autoscaler that could never scale", func() {
			out := renderFailure("autoscaling:\n  enabled: true\n")
			Expect(out).To(ContainSubstring("resources.requests.cpu"))
		})
	})

	Describe("the values schema", func() {
		// Go templates treat any non-empty string as true, so `enabled: "false"`
		// TURNS A FEATURE ON. For cnpg that means provisioning a database the
		// user just asked not to have. The schema caught this for route and
		// ingress and for nothing else.
		DescribeTable("rejects a quoted boolean, which would otherwise invert the flag",
			func(values string) {
				Expect(renderFailure(values)).To(ContainSubstring("want boolean"))
			},
			Entry("cnpg", "cnpg:\n  enabled: \"false\"\n"),
			Entry("externalSecrets", "externalSecrets:\n  enabled: \"false\"\n"),
			Entry("metrics", "metrics:\n  enabled: \"false\"\n"),
			Entry("migrations", "migrations:\n  job:\n    enabled: \"false\"\n"),
			Entry("simulator", "simulator:\n  enabled: \"false\"\n"),
			Entry("networkPolicy", "networkPolicy:\n  enabled: \"false\"\n"),
		)

		// A misspelt key is silently ignored: you get the default behaviour and
		// no indication that the value you set went nowhere.
		DescribeTable("names a key it does not recognise instead of ignoring it",
			func(values, wanted string) {
				Expect(renderFailure(values)).To(ContainSubstring(wanted))
			},
			Entry("top-level typo", "simulater:\n  enabled: false\n", "additional properties"),
			Entry("a value from another chart", "replicaCount: 7\n", "additional properties"),
			Entry("nested app-config typo", "config:\n  tracing:\n    endpont: \"otel:4317\"\n", "'/config/tracing'"),
		)

		// Each of these is a closed set the app validates at RUNTIME, in a
		// pod, long after `helm install` has reported success.
		DescribeTable("rejects a value outside a closed set",
			func(values string) {
				Expect(renderFailure(values)).To(ContainSubstring("value must be one of"))
			},
			Entry("tracing protocol", "config:\n  tracing:\n    protocol: htp\n"),
			Entry("log format", "config:\n  log:\n    format: logfmt\n"),
			// Passed verbatim to `alloy --stability.level=`, so a typo fails
			// every config validation Shepherd performs, in-cluster, forever.
			Entry("alloy stability level", "config:\n  validate:\n    stability_level: experimenal\n"),
			Entry("service type", "service:\n  type: Cluster\n"),
			Entry("cnpg render mode", "cnpg:\n  enabled: true\n  render: sometimes\n"),
		)

		It("pins the two values that cannot be changed without silent data loss", func() {
			// A different key length produces an instance that cannot start;
			// a non-zero refresh regenerates a key that cannot be rotated,
			// orphaning every credential already encrypted under the old one.
			Expect(renderFailure("externalSecrets:\n  encryptionKey:\n    length: 16\n")).NotTo(BeEmpty())
			Expect(renderFailure("externalSecrets:\n  refreshInterval: \"1h\"\n")).NotTo(BeEmpty())
		})
	})

	Describe("ingress", func() {
		// "*.example.com" is the commonest non-trivial host and starts with the
		// YAML alias character. Unquoted it failed the whole render with a
		// parse error naming a line number rather than the host.
		It("renders a wildcard host", func() {
			objects := renderWith("ingress:\n  enabled: true\n  hosts:\n    - host: \"*.example.com\"\n      paths:\n        - path: /\n")
			ing, found := objects["Ingress/shepherd"]
			Expect(found).To(BeTrue())
			spec, _ := ing["spec"].(map[string]any)
			rules, _ := spec["rules"].([]any)
			rule, _ := rules[0].(map[string]any)
			Expect(rule["host"]).To(Equal("*.example.com"))
		})
	})

	Describe("NOTES", func() {
		// Every one of these printed something that does not work: a spoke URL
		// hardcoded to "http://shepherd:8080" (wrong for any release not named
		// "shepherd", and ignoring service.port), an ingress the URL branch
		// never consulted, and multiple route hostnames concatenated into one
		// unusable string.
		It("prints a spoke URL that resolves for the release and port in use", func() {
			out := renderNotes("release-two", "service:\n  port: 9000\n")
			Expect(out).To(ContainSubstring("http://release-two-shepherd."))
			Expect(out).To(ContainSubstring(":9000"))
			Expect(out).NotTo(ContainSubstring("http://shepherd:8080"))
		})

		It("points collectors at the ingress when there is one", func() {
			out := renderNotes("fleet", "ingress:\n  enabled: true\n  hosts:\n    - host: shep.example.com\n      paths:\n        - path: /\n")
			Expect(out).To(ContainSubstring("https://shep.example.com"))
			Expect(out).NotTo(ContainSubstring("shep.example.comhttps://"))
			Expect(out).NotTo(ContainSubstring("http://fleet-shepherd."),
				"an ingress is configured but collectors are still sent to the in-cluster name")
		})

		It("uses one hostname when several are configured", func() {
			out := renderNotes("fleet", "route:\n  enabled: true\n  hostnames:\n    - a.example.com\n    - b.example.com\n")
			Expect(out).To(ContainSubstring("https://a.example.com"))
			Expect(out).NotTo(ContainSubstring("a.example.comhttps://b.example.com"))
		})

		It("does not tell a NodePort user to port-forward", func() {
			out := renderNotes("fleet", "service:\n  type: NodePort\n")
			Expect(out).To(ContainSubstring("nodePort"))
			Expect(out).NotTo(ContainSubstring("port-forward"))
		})

		It("says how to read the password it just generated", func() {
			out := renderNotes("fleet", "externalSecrets:\n  enabled: true\ncnpg:\n  enabled: true\n")
			Expect(out).To(ContainSubstring("SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD"))
			Expect(out).To(ContainSubstring("base64 -d"))
		})
	})

	Describe("GitOps safety", func() {
		// lookup returns empty with no cluster connection, which is every
		// template-and-apply pipeline: Argo CD, Flux, `helm template | kubectl
		// apply`. The existence guard therefore fails OPEN there and the
		// resource is emitted on every sync -- and Argo CD deletes a PreSync
		// hook before recreating it, taking the database's PVCs with it.
		It("can be told never to emit the resources that own data", func() {
			objects := renderWith("cnpg:\n  enabled: true\n  render: never\nexternalSecrets:\n  enabled: true\n  render: never\n")
			for key := range objects {
				Expect(key).NotTo(HavePrefix("Cluster/"))
				Expect(key).NotTo(HavePrefix("ExternalSecret/"))
				Expect(key).NotTo(HavePrefix("Password/"))
			}
		})

		It("still emits them by default, so a plain install bootstraps itself", func() {
			objects := renderWith("cnpg:\n  enabled: true\n")
			Expect(objects).To(HaveKey("Cluster/shepherd-db"))
		})

		It("keeps the lookup guard for the helm CLI path", func() {
			// The value gate is for pipelines that cannot use lookup; it does
			// not replace the guard that protects a normal `helm upgrade`.
			for _, f := range []string{"shepherd/templates/cnpg-cluster.yaml", "shepherd/templates/externalsecret.yaml"} {
				body, err := os.ReadFile(f)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(body)).To(ContainSubstring("lookup"), "%s lost its existence guard", f)
			}
		})
	})

	// The upgrade from 0.8.x has to adopt three objects that were created as
	// hooks and therefore carry no Helm ownership metadata. Helm's own error
	// names one object, does not say why, and does not say that stamping the
	// metadata is the fix. This guard says all three.
	It("explains the one manual step the 0.8.x upgrade needs", func() {
		body, err := os.ReadFile("shepherd/templates/upgrade-guard.yaml")
		Expect(err).NotTo(HaveOccurred())
		for _, wanted := range []string{"kubectl label", "kubectl annotate", "meta.helm.sh/release-name", "UPGRADING.md"} {
			Expect(string(body)).To(ContainSubstring(wanted))
		}

		upgrading, err := os.ReadFile("shepherd/UPGRADING.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(upgrading)).To(ContainSubstring("0.8.x"))
		Expect(strings.ToLower(string(upgrading))).To(ContainSubstring("render: never"))
	})
})
