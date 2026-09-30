package routeapply

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"shepherd/internal/gateway"
)

var (
	httpRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	crdGVR       = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// httpRouteCRDName is the one CRD the chart's ClusterRole lets Shepherd read.
const httpRouteCRDName = "httproutes.gateway.networking.k8s.io"

// Kube is the production Cluster: client-go's dynamic client, so Shepherd
// needs no typed clientset or controller-runtime for one resource. It
// touches HTTPRoutes and reads one CRD — never a Gateway.
type Kube struct {
	client dynamic.Interface
}

var _ Cluster = (*Kube)(nil)

// NewInCluster builds a Kube from the pod's ServiceAccount.
func NewInCluster() (*Kube, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("routeapply: in-cluster config: %w", err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("routeapply: dynamic client: %w", err)
	}
	return NewKube(client), nil
}

// NewKube wraps an existing dynamic client.
func NewKube(client dynamic.Interface) *Kube {
	return &Kube{client: client}
}

// Apply creates route, or updates the existing object in place.
func (k *Kube) Apply(ctx context.Context, route *gatewayv1.HTTPRoute) error {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(route)
	if err != nil {
		return fmt.Errorf("converting HTTPRoute %s/%s: %w", route.Namespace, route.Name, err)
	}
	u := &unstructured.Unstructured{Object: obj}
	res := k.client.Resource(httpRouteGVR).Namespace(route.Namespace)

	existing, err := res.Get(ctx, route.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = res.Create(ctx, u, metav1.CreateOptions{})
		return err
	case err != nil:
		return fmt.Errorf("checking whether HTTPRoute %s/%s exists: %w", route.Namespace, route.Name, err)
	}
	u.SetResourceVersion(existing.GetResourceVersion())
	_, err = res.Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// Get reads namespace/name, wrapping gateway.ErrNotFound when it is absent.
func (k *Kube) Get(ctx context.Context, namespace, name string) (*gatewayv1.HTTPRoute, error) {
	u, err := k.client.Resource(httpRouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("HTTPRoute %s/%s: %w", namespace, name, gateway.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var route gatewayv1.HTTPRoute
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &route); err != nil {
		return nil, fmt.Errorf("decoding HTTPRoute %s/%s: %w", namespace, name, err)
	}
	return &route, nil
}

// HTTPRouteCRDAnnotations reads the installed HTTPRoute CRD's annotations,
// which carry the Gateway API bundle version and channel (D3).
func (k *Kube) HTTPRouteCRDAnnotations(ctx context.Context) (map[string]string, error) {
	u, err := k.client.Resource(crdGVR).Get(ctx, httpRouteCRDName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading CRD %s: %w", httpRouteCRDName, err)
	}
	return u.GetAnnotations(), nil
}

// Delete removes namespace/name; an object that is already gone is success.
func (k *Kube) Delete(ctx context.Context, namespace, name string) error {
	err := k.client.Resource(httpRouteGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting HTTPRoute %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ListManaged returns the names of the HTTPRoutes in namespace that carry
// RouteIDLabel — the tenant-route objects this package created.
func (k *Kube) ListManaged(ctx context.Context, namespace string) ([]string, error) {
	list, err := k.client.Resource(httpRouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RouteIDLabel + "," + gateway.ManagedByLabel + "=shepherd",
	})
	if err != nil {
		return nil, fmt.Errorf("listing managed HTTPRoutes in %s: %w", namespace, err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	return names, nil
}
