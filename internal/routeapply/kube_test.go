package routeapply_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"shepherd/internal/gateway"
	"shepherd/internal/routeapply"
)

var _ = Describe("Kube (client-go dynamic adapter)", func() {
	var (
		ctx  context.Context
		kube *routeapply.Kube
	)

	httpRoutes := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	crds := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

	unmanaged := func() *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("gateway.networking.k8s.io/v1")
		u.SetKind("HTTPRoute")
		u.SetNamespace("shepherd")
		u.SetName("acme-otlp")
		return u
	}
	crd := func() *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("apiextensions.k8s.io/v1")
		u.SetKind("CustomResourceDefinition")
		u.SetName("httproutes.gateway.networking.k8s.io")
		u.SetAnnotations(map[string]string{
			gateway.BundleVersionAnnotation: "v1.4.1",
			gateway.ChannelAnnotation:       "standard",
		})
		return u
	}

	BeforeEach(func() {
		ctx = context.Background()
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{
				httpRoutes: "HTTPRouteList",
				crds:       "CustomResourceDefinitionList",
			}, unmanaged(), crd())
		kube = routeapply.NewKube(client)
	})

	spec := func(segment string) gateway.RouteSpec {
		return gateway.RouteSpec{
			Name: routeapply.ObjectName("r1"), Namespace: "shepherd", TenantID: "acme",
			Kind: gateway.KindOTLP, RouteSegment: segment, GatewayName: "edge", GatewayNamespace: "gateways",
			BackendName: "shepherd-receiver", BackendPort: 4318,
			Labels: map[string]string{routeapply.RouteIDLabel: "r1"},
		}
	}

	It("creates, then converges, an HTTPRoute and reads it back typed", func() {
		first, err := gateway.RenderHTTPRoute(spec("acme-a"))
		Expect(err).NotTo(HaveOccurred())
		Expect(kube.Apply(ctx, first)).To(Succeed())

		second, err := gateway.RenderHTTPRoute(spec("acme-b"))
		Expect(err).NotTo(HaveOccurred())
		Expect(kube.Apply(ctx, second)).To(Succeed(), "applying an existing object updates it")

		got, err := kube.Get(ctx, "shepherd", routeapply.ObjectName("r1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(*got.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/otlp/acme-b"))
		Expect(got.Labels).To(HaveKeyWithValue(routeapply.RouteIDLabel, "r1"))
	})

	It("reports a missing HTTPRoute as gateway.ErrNotFound", func() {
		_, err := kube.Get(ctx, "shepherd", "nope")
		Expect(errors.Is(err, gateway.ErrNotFound)).To(BeTrue(), "got %v", err)
	})

	It("reads the Gateway API version off the HTTPRoute CRD", func() {
		ann, err := kube.HTTPRouteCRDAnnotations(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(gateway.CheckSupport(ann[gateway.BundleVersionAnnotation], ann[gateway.ChannelAnnotation])).To(Succeed())
	})

	It("lists only the routes it manages, and deletes idempotently", func() {
		route, err := gateway.RenderHTTPRoute(spec("acme-a"))
		Expect(err).NotTo(HaveOccurred())
		Expect(kube.Apply(ctx, route)).To(Succeed())

		names, err := kube.ListManaged(ctx, "shepherd")
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(ConsistOf(routeapply.ObjectName("r1")), "the hand-made acme-otlp is not ours")

		Expect(kube.Delete(ctx, "shepherd", routeapply.ObjectName("r1"))).To(Succeed())
		Expect(kube.Delete(ctx, "shepherd", routeapply.ObjectName("r1"))).To(Succeed(), "already gone is success")
		names, err = kube.ListManaged(ctx, "shepherd")
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())

		_, err = kube.Get(ctx, "shepherd", "acme-otlp")
		Expect(err).NotTo(HaveOccurred(), "an unmanaged route is never touched")
	})

	It("drives gateway.ApplyRoute end to end against the fake apiserver", func() {
		// The fake apiserver has no controller, so the route never attaches:
		// ApplyRoute must report that as a failure, not a success.
		_, err := gateway.ApplyRoute(ctx, kube, spec("acme-a"), gateway.ApplyOptions{
			PollInterval: 10 * time.Millisecond, Deadline: 50 * time.Millisecond,
		})
		Expect(err).To(MatchError(ContainSubstring("did not verify as attached")))
		_, getErr := kube.Get(ctx, "shepherd", routeapply.ObjectName("r1"))
		Expect(getErr).NotTo(HaveOccurred(), "the object was still created")
	})
})
