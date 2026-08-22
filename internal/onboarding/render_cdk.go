package onboarding

import (
	"fmt"
	"strings"
)

// renderCDK renders a TypeScript CDK fragment: the `environment` map for an
// existing `lambda.Function`, and — when spec.IncludeADOTLayer is set — a
// `layers` entry built from a construct prop CDK requires the caller to
// supply (`props.adotLayerArn`), never a literal ARN (D5's corollary).
func renderCDK(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "// Fragment for %q. Route: kind=%s segment=%s\n\n", spec.ServiceName, spec.Route.Kind, spec.Route.RouteSegment)

	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "// D5: no literal ARN — pass it in as a prop/context value looked up for\n")
		_, _ = fmt.Fprintf(&sb, "// your region/runtime/architecture at %s\n", adotLambdaDocsURL)
		_, _ = fmt.Fprintf(&sb, "const adotLayer = lambda.LayerVersion.fromLayerVersionArn(\n")
		_, _ = fmt.Fprintf(&sb, "  this, 'ADOTLayer', props.adotLayerArn,\n")
		_, _ = fmt.Fprintf(&sb, ");\n\n")
	}

	_, _ = fmt.Fprintf(&sb, "// ... existing lambda.Function(this, 'Fn', { ... }) props ...\n")
	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "layers: [adotLayer],\n")
	}
	_, _ = fmt.Fprintf(&sb, "environment: {\n")
	for _, v := range lambdaEnv(base, spec) {
		_, _ = fmt.Fprintf(&sb, "  %s: %q,\n", v.Key, v.Value)
	}
	_, _ = fmt.Fprintf(&sb, "},\n")
	return sb.String()
}
