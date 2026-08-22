package onboarding

import (
	"fmt"
	"strings"
)

// renderSAM renders the Globals/Function fragment an AWS SAM template
// (template.yaml) needs: an Environment.Variables map, and — when
// spec.IncludeADOTLayer is set — a Layers entry plus the Parameter that
// backs it. As with Terraform, no literal ARN (D5's corollary): the
// Parameter has no Default, so `sam deploy` fails loudly (missing parameter)
// rather than silently deploying with a guess at the ARN.
func renderSAM(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "# Fragment of template.yaml for %q. Route: kind=%s segment=%s\n\n", spec.ServiceName, spec.Route.Kind, spec.Route.RouteSegment)

	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "# D5: no literal ARN — look yours up at %s\n", adotLambdaDocsURL)
		_, _ = fmt.Fprintf(&sb, "Parameters:\n")
		_, _ = fmt.Fprintf(&sb, "  ADOTLayerArn:\n")
		_, _ = fmt.Fprintf(&sb, "    Type: String\n")
		_, _ = fmt.Fprintf(&sb, "    Description: >-\n")
		_, _ = fmt.Fprintf(&sb, "      ADOT Lambda layer ARN for this function's runtime/arch/region —\n")
		_, _ = fmt.Fprintf(&sb, "      %s\n\n", adotLambdaDocsURL)
	}

	_, _ = fmt.Fprintf(&sb, "Resources:\n")
	_, _ = fmt.Fprintf(&sb, "  Function:\n")
	_, _ = fmt.Fprintf(&sb, "    Type: AWS::Serverless::Function\n")
	_, _ = fmt.Fprintf(&sb, "    Properties:\n")
	_, _ = fmt.Fprintf(&sb, "      # ... CodeUri, Handler, Runtime, etc. unchanged ...\n")
	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "      Layers:\n")
		_, _ = fmt.Fprintf(&sb, "        - !Ref ADOTLayerArn\n")
	}
	_, _ = fmt.Fprintf(&sb, "      Environment:\n")
	_, _ = fmt.Fprintf(&sb, "        Variables:\n")
	for _, v := range lambdaEnv(base, spec) {
		_, _ = fmt.Fprintf(&sb, "          %s: %q\n", v.Key, v.Value)
	}
	return sb.String()
}
