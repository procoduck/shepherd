package gateway

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateGatewayRef reports whether name/namespace can identify a Gateway
// API `Gateway` object — the parentRef every rendered HTTPRoute attaches to.
//
// The rule is Kubernetes' own, via k8s.io/apimachinery's validators rather
// than a regexp copied here: a Gateway's name is an ordinary object name, a
// DNS-1123 subdomain (lowercase alphanumerics, '-' and '.', at most 253
// characters), and a namespace is a DNS-1123 label (no '.', at most 63) —
// gateway-api's own Namespace type carries that exact pattern. ParentReference
// .name itself has no pattern in the CRD, so the API server accepts an
// HTTPRoute naming "Bad Name!" and the route then simply never attaches:
// refusing it where it is typed is the only place the mistake is visible.
//
// namespace may be empty ("the HTTPRoute's own namespace"); name may not.
// It is enforced in two places: the management API when a route is created,
// and RouteSpec.validate, so a row stored before this rule existed fails its
// apply with this message instead of waiting out the attachment deadline.
func ValidateGatewayRef(name, namespace string) error {
	if name == "" {
		return errors.New("gateway name is required")
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		return fmt.Errorf("gateway name %q is not a valid Kubernetes object name: use lowercase "+
			"letters, digits, '-' and '.', starting and ending with a letter or digit, at most "+
			"%d characters", name, validation.DNS1123SubdomainMaxLength)
	}
	if namespace != "" && len(validation.IsDNS1123Label(namespace)) > 0 {
		return fmt.Errorf("gateway namespace %q is not a valid Kubernetes namespace: use lowercase "+
			"letters, digits and '-', starting and ending with a letter or digit, at most %d "+
			"characters", namespace, validation.DNS1123LabelMaxLength)
	}
	return nil
}
