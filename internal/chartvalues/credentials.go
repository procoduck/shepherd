package chartvalues

import (
	"fmt"
	"regexp"
	"strings"

	"shepherd/internal/beacon"
)

// Keys of the Kubernetes Secret RenderCredentialsLayer reads the agent token
// from. CredentialsSecretCommand creates a Secret with exactly these.
const (
	SecretKeyTokenID     = "token-id"
	SecretKeyTokenSecret = "token-secret"
)

// secretNameRE is a Kubernetes object name (RFC 1123 subdomain, the short form).
var secretNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,251}[a-z0-9])?$`)

// RenderCredentialsLayer is the second values file an operator needs next to
// Render's: for each selected collector, alloy.extraEnv entries that load the
// Shepherd agent token from the Secret secretName (as
// beacon.DefaultTokenIDEnv/DefaultTokenSecretEnv, the variables Render's
// remoteConfig auth reads), plus the RequiredOperatorExtraEnvVar the chart's
// own validation insists on. It carries a Secret's NAME, never a value, so it
// is as safe to commit as Render's output. Render stays a remoteConfig-only
// layer (doc.go); this is the "your own values file must add" part of its
// header comment, rendered instead of described.
func RenderCredentialsLayer(roles []string, secretName string) ([]byte, error) {
	if !secretNameRE.MatchString(secretName) {
		return nil, fmt.Errorf("chartvalues: secret name %q is not a valid Kubernetes name", secretName)
	}
	// Reuse Validate's role checks with placeholder values for the rest.
	if err := Validate(Spec{ClusterName: "x", ShepherdURL: "https://x", Roles: roles}); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, r := range roles {
		want[r] = true
	}

	var sb strings.Builder
	sb.WriteString("# Agent-token wiring for the collectors in Shepherd's generated layer. Apply\n")
	sb.WriteString("# both files: helm upgrade ... -f shepherd-layer.yaml -f this-file.yaml\n")
	fmt.Fprintf(&sb, "# The token itself lives in the Secret %q (keys %s, %s).\n", secretName, SecretKeyTokenID, SecretKeyTokenSecret)
	fmt.Fprintf(&sb, "# %s is required by the chart's own validation and read by nothing.\n\n", RequiredOperatorExtraEnvVar)
	sb.WriteString("collectors:\n")
	for _, role := range validRoles() {
		if !want[role] {
			continue
		}
		fmt.Fprintf(&sb, "  %s:\n", collectorName(role))
		sb.WriteString("    alloy:\n")
		sb.WriteString("      extraEnv:\n")
		for _, e := range []struct{ name, key string }{
			{beacon.DefaultTokenIDEnv, SecretKeyTokenID},
			{beacon.DefaultTokenSecretEnv, SecretKeyTokenSecret},
		} {
			fmt.Fprintf(&sb, "        - name: %s\n", e.name)
			sb.WriteString("          valueFrom:\n")
			sb.WriteString("            secretKeyRef:\n")
			fmt.Fprintf(&sb, "              name: %s\n", yamlDoubleQuote(secretName))
			fmt.Fprintf(&sb, "              key: %s\n", e.key)
		}
		fmt.Fprintf(&sb, "        - name: %s\n", RequiredOperatorExtraEnvVar)
		sb.WriteString("          value: \"unused\"\n")
	}
	return []byte(sb.String()), nil
}

// CredentialsSecretCommand is the kubectl command that creates the Secret
// RenderCredentialsLayer reads, with placeholders for the token — the secret
// is shown once, when an app admin creates the token, and never by Shepherd
// again.
func CredentialsSecretCommand(namespace, secretName string) string {
	return fmt.Sprintf("kubectl -n %s create secret generic %s \\\n  --from-literal=%s=<agent token id> \\\n  --from-literal=%s=<agent token secret>",
		namespace, secretName, SecretKeyTokenID, SecretKeyTokenSecret)
}
