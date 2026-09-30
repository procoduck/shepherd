package helm_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Tenant-route apply, PR 3 of docs/plans/2026-09-29-tenant-route-apply.md: the
// chart gives Shepherd Kubernetes API access ONLY with the receiver tier on,
// and only what internal/routeapply uses. Whether the reconciler actually
// works with exactly these grants is the kind suite's to prove (PR 5).

func objectsByKind(objs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, o := range objs {
		if o["kind"] == kind {
			out = append(out, o)
		}
	}
	return out
}

func appDeployment(objs []map[string]any) map[string]any {
	for _, o := range objectsByKind(objs, "Deployment") {
		if dig(o, "metadata", "name") == "shepherd" {
			return o
		}
	}
	Fail("no Deployment/shepherd rendered")
	return nil
}

var _ = Describe("tenant-route apply chart wiring", func() {
	It("grants no RBAC and mounts no token with the receiver off (the default)", func() {
		objs, out, err := helmTemplate("{}")
		Expect(err).NotTo(HaveOccurred(), out)
		for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
			Expect(objectsByKind(objs, kind)).To(BeEmpty(), "%s rendered with the receiver off", kind)
		}
		dep := appDeployment(objs)
		Expect(podSpecOf(dep)["automountServiceAccountToken"]).To(BeFalse())
		Expect(envOf(containerOf(dep, "shepherd"))).NotTo(HaveKey("SHEPHERD_GATEWAY_ROUTES_APPLY_ENABLED"))
	})

	It("grants none of it when the receiver is on but applyTenantRoutes is false", func() {
		objs, out, err := helmTemplate(receiverOn + "  applyTenantRoutes: false\n")
		Expect(err).NotTo(HaveOccurred(), out)
		for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
			Expect(objectsByKind(objs, kind)).To(BeEmpty(), "%s rendered with applyTenantRoutes: false", kind)
		}
		Expect(podSpecOf(appDeployment(objs))["automountServiceAccountToken"]).To(BeFalse())
	})

	Context("with the receiver on", func() {
		var objs []map[string]any

		BeforeEach(func() {
			var out string
			var err error
			objs, out, err = helmTemplate(receiverOn)
			Expect(err).NotTo(HaveOccurred(), out)
		})

		It("grants a namespaced Role on httproutes only, bound to Shepherd's ServiceAccount", func() {
			roles := objectsByKind(objs, "Role")
			Expect(roles).To(HaveLen(1))
			Expect(dig(roles[0], "rules")).To(Equal([]any{map[string]any{
				"apiGroups": []any{"gateway.networking.k8s.io"},
				"resources": []any{"httproutes"},
				"verbs":     []any{"get", "list", "create", "update", "delete"},
			}}))
			bindings := objectsByKind(objs, "RoleBinding")
			Expect(bindings).To(HaveLen(1))
			Expect(dig(bindings[0], "roleRef", "name")).To(Equal(dig(roles[0], "metadata", "name")))
			Expect(dig(bindings[0], "subjects")).To(Equal([]any{map[string]any{
				"kind": "ServiceAccount", "name": "shepherd", "namespace": "default",
			}}))
		})

		It("grants cluster-wide exactly get on the one HTTPRoute CRD", func() {
			roles := objectsByKind(objs, "ClusterRole")
			Expect(roles).To(HaveLen(1))
			Expect(dig(roles[0], "rules")).To(Equal([]any{map[string]any{
				"apiGroups":     []any{"apiextensions.k8s.io"},
				"resources":     []any{"customresourcedefinitions"},
				"resourceNames": []any{"httproutes.gateway.networking.k8s.io"},
				"verbs":         []any{"get"},
			}}))
			bindings := objectsByKind(objs, "ClusterRoleBinding")
			Expect(bindings).To(HaveLen(1))
			Expect(dig(bindings[0], "roleRef", "name")).To(Equal(dig(roles[0], "metadata", "name")))
		})

		It("mounts the token on the app pod only, and never offers it from the ServiceAccount", func() {
			Expect(podSpecOf(appDeployment(objs))["automountServiceAccountToken"]).To(BeTrue())
			for _, sa := range objectsByKind(objs, "ServiceAccount") {
				Expect(sa["automountServiceAccountToken"]).To(BeFalse(), "ServiceAccount %v", dig(sa, "metadata", "name"))
			}
			for _, job := range objectsByKind(objs, "Job") {
				Expect(podSpecOf(job)["automountServiceAccountToken"]).To(BeFalse(), "Job %v", dig(job, "metadata", "name"))
			}
			for _, dep := range objectsByKind(objs, "Deployment") {
				if dig(dep, "metadata", "name") != "shepherd" {
					Expect(podSpecOf(dep)["automountServiceAccountToken"]).To(BeFalse(), "Deployment %v", dig(dep, "metadata", "name"))
				}
			}
		})

		It("configures the reconciler: its own namespace, the receiver Service as backend", func() {
			env := envOf(containerOf(appDeployment(objs), "shepherd"))
			Expect(dig(env["SHEPHERD_GATEWAY_ROUTES_APPLY_ENABLED"], "value")).To(Equal("true"))
			Expect(dig(env["SHEPHERD_GATEWAY_ROUTES_APPLY_NAMESPACE"], "valueFrom", "fieldRef", "fieldPath")).To(Equal("metadata.namespace"))
			Expect(dig(env["SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_SERVICE"], "value")).To(Equal(dig(receiverObject(objs, "Service"), "metadata", "name")))
			Expect(dig(env["SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_PORT"], "value")).To(Equal("4318"))
		})
	})
})

var _ = Describe("receiver.publicBaseURL", func() {
	It("reaches the Shepherd pod as the connect-an-app default URL", func() {
		objs, out, err := helmTemplate(receiverOn + "  publicBaseURL: https://telemetry.example.com\n")
		Expect(err).NotTo(HaveOccurred(), out)
		env := envOf(containerOf(appDeployment(objs), "shepherd"))
		Expect(dig(env["SHEPHERD_GATEWAY_ROUTES_PUBLIC_BASE_URL"], "value")).To(Equal("https://telemetry.example.com"))
	})

	It("is absent when unset", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(envOf(containerOf(appDeployment(objs), "shepherd"))).NotTo(HaveKey("SHEPHERD_GATEWAY_ROUTES_PUBLIC_BASE_URL"))
	})

	It("refuses a plaintext URL", func() {
		_, out, err := helmTemplate(receiverOn + "  publicBaseURL: http://telemetry.example.com\n")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("publicBaseURL"))
	})
})
