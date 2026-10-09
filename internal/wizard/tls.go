package wizard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// TLS options for a destination's writer (#261). Like the Secret auth of
// #229, Shepherd stores only references to objects on the spoke cluster and
// key names; the collector reads the certificate material at runtime with
// remote.kubernetes.secret / remote.kubernetes.configmap. No PEM ever enters
// Shepherd or the served config. See docs/plans/2026-10-06-destination-tenant-tls.md §3.

// ExtraKeyTLS is the destinations.extra key holding a destination's TLS
// options (maintainer decision Q4: extra, no proto change).
const ExtraKeyTLS = "tls"

// The fixed key names, following the kubernetes.io/tls Secret type (and what
// cert-manager writes). The CA key is the default; a destination may name
// another (TLSCA.Key, maintainer decision Q5).
const (
	TLSKeyCADefault = "ca.crt"
	TLSKeyCert      = "tls.crt"
	TLSKeyKey       = "tls.key"
)

// ObjectKind is the kind of Kubernetes object a TLS reference names.
type ObjectKind string

// The object kinds a TLS reference may name.
const (
	ObjectSecret    ObjectKind = "secret"
	ObjectConfigMap ObjectKind = "configmap"
)

// TLS is a destination's extra.tls. Every part is optional; a TLS with no
// part renders nothing (the system trust store, no client certificate).
// There is deliberately no insecure_skip_verify (maintainer decision Q3): it
// is refused as an unknown key.
type TLS struct {
	// CA is the trusted CA bundle, from a Secret or a ConfigMap.
	CA *TLSCA `json:"ca,omitempty"`
	// ClientCert is a Secret holding tls.crt and tls.key.
	ClientCert *TLSClientCert `json:"client_cert,omitempty"`
	// ServerName overrides the name the server certificate is verified
	// against (tls_config.server_name).
	ServerName string `json:"server_name,omitempty"`
}

// TLSCA names the object holding the CA bundle and, optionally, its key.
type TLSCA struct {
	Kind      ObjectKind `json:"kind"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	// Key is the data key holding the PEM bundle; empty means ca.crt.
	Key string `json:"key,omitempty"`
}

// TLSClientCert names the Secret holding the client certificate and key.
type TLSClientCert struct {
	// Kind may be omitted; when set it must be "secret" (a private key never
	// belongs in a ConfigMap).
	Kind      ObjectKind `json:"kind,omitempty"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
}

// CAKey is the data key the CA bundle is read from.
func (c *TLSCA) CAKey() string {
	if c.Key == "" {
		return TLSKeyCADefault
	}
	return c.Key
}

// Empty reports whether t renders nothing.
func (t *TLS) Empty() bool {
	return t == nil || (t.CA == nil && t.ClientCert == nil && t.ServerName == "")
}

// ParseTLS decodes a destination's extra JSON into its TLS options: nil
// when extra has no tls key (or it is null). Decoding is strict, so a typo or
// an option Shepherd does not offer (insecure_skip_verify) is an error rather
// than silently ignored. The result is validated (Validate).
func ParseTLS(extra []byte) (*TLS, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(extra, &m); err != nil {
		return nil, fmt.Errorf("extra is not a JSON object: %w", err)
	}
	raw, ok := m[ExtraKeyTLS]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var t TLS
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("extra.%s: %w", ExtraKeyTLS, err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// dataKeyRE is a legal ConfigMap/Secret data key (Kubernetes'
// IsConfigMapKey): alphanumerics, '-', '_' and '.'.
var dataKeyRE = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// hostnameRE is an RFC 1123 hostname, the shape server_name takes.
var hostnameRE = regexp.MustCompile(`^(?i)[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`)

// Validate checks every reference is a valid Kubernetes namespace and name,
// the CA key is a legal data key, and server_name is a hostname.
func (t *TLS) Validate() error {
	if t == nil {
		return nil
	}
	if c := t.CA; c != nil {
		switch c.Kind {
		case ObjectSecret, ObjectConfigMap:
		default:
			return fmt.Errorf("extra.tls.ca.kind must be %q or %q (got %q)", ObjectSecret, ObjectConfigMap, c.Kind)
		}
		if err := validateObjectRef("extra.tls.ca", c.Namespace, c.Name); err != nil {
			return err
		}
		if c.Key != "" && (len(c.Key) > 253 || c.Key == "." || c.Key == ".." || !dataKeyRE.MatchString(c.Key)) {
			return fmt.Errorf("extra.tls.ca.key %q is not a legal ConfigMap/Secret key (letters, digits, '-', '_', '.')", c.Key)
		}
	}
	if c := t.ClientCert; c != nil {
		if c.Kind != "" && c.Kind != ObjectSecret {
			return fmt.Errorf("extra.tls.client_cert must be a Secret (got kind %q): a private key never belongs in a ConfigMap", c.Kind)
		}
		if err := validateObjectRef("extra.tls.client_cert", c.Namespace, c.Name); err != nil {
			return err
		}
	}
	if t.ServerName != "" && (len(t.ServerName) > 253 || !hostnameRE.MatchString(t.ServerName)) {
		return fmt.Errorf("extra.tls.server_name %q is not a hostname", t.ServerName)
	}
	return nil
}

func validateObjectRef(field, namespace, name string) error {
	if !k8sNamespaceRE.MatchString(namespace) {
		return fmt.Errorf("%s.namespace must be a Kubernetes namespace name (got %q)", field, namespace)
	}
	if !k8sNameRE.MatchString(name) {
		return fmt.Errorf("%s.name must be a Kubernetes object name (got %q)", field, name)
	}
	return nil
}

// errTLSNeedsHTTPS refuses TLS options on an http:// URL, where they would
// configure nothing.
var errTLSNeedsHTTPS = errors.New("TLS options need an https:// URL")

// ValidateTLSForURL refuses TLS options with a non-https URL.
func ValidateTLSForURL(t *TLS, rawURL string) error {
	if t.Empty() {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return errTLSNeedsHTTPS
	}
	return nil
}

// objectReads collects the remote.kubernetes.* components a writer reads,
// one per distinct object: two references to the same object (auth and
// client certificate in one Secret, or a cert-manager Secret serving as both
// CA and client certificate) share one component and one read.
type objectReads struct {
	sb     strings.Builder
	labels map[string]string
}

func (o *objectReads) add(kind ObjectKind, namespace, name, label string) string {
	if o.labels == nil {
		o.labels = map[string]string{}
	}
	id := string(kind) + "/" + namespace + "/" + name
	if l, ok := o.labels[id]; ok {
		return l
	}
	o.labels[id] = label
	fmt.Fprintf(&o.sb, `remote.kubernetes.%s %s {
  namespace = %s
  name      = %s
}

`, kind, Quote(label), Quote(namespace), Quote(name))
	return label
}

// dataRef is the expression reading key from the component label of kind.
func dataRef(kind ObjectKind, label, key string) string {
	return fmt.Sprintf("remote.kubernetes.%s.%s.data[%s]", kind, label, Quote(key))
}

// renderTLS writes the writer's tls_config block. A Secret's data is
// map(secret) and ca_pem/cert_pem are strings, so those go through
// convert.nonsensitive (as #229 does for username); a ConfigMap's data is
// map(string). key_pem is a secret and is never converted.
func renderTLS(sb *strings.Builder, reads *objectReads, label string, t *TLS) {
	if t.Empty() {
		return
	}
	var lines []string
	if c := t.CA; c != nil {
		l := reads.add(c.Kind, c.Namespace, c.Name, label+"_tls_ca")
		expr := dataRef(c.Kind, l, c.CAKey())
		if c.Kind == ObjectSecret {
			expr = "convert.nonsensitive(" + expr + ")"
		}
		lines = append(lines, "ca_pem      = "+expr)
	}
	if c := t.ClientCert; c != nil {
		l := reads.add(ObjectSecret, c.Namespace, c.Name, label+"_tls_client")
		lines = append(lines,
			"cert_pem    = convert.nonsensitive("+dataRef(ObjectSecret, l, TLSKeyCert)+")",
			"key_pem     = "+dataRef(ObjectSecret, l, TLSKeyKey))
	}
	if t.ServerName != "" {
		lines = append(lines, "server_name = "+Quote(t.ServerName))
	}
	sb.WriteString("\n    tls_config {\n")
	for _, line := range lines {
		sb.WriteString("      " + line + "\n")
	}
	sb.WriteString("    }\n")
}
