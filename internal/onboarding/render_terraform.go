package onboarding

import (
	"fmt"
	"strings"
)

// renderTerraform renders an HCL snippet layering onto an existing
// `aws_lambda_function` resource: its `environment` block and, when
// spec.IncludeADOTLayer is set, a `layers` entry referencing a Terraform
// variable this snippet declares. The ARN itself is never a literal here
// (D5's corollary) — the variable has no default, forcing whoever applies
// this to look the value up for their own region/runtime/architecture
// rather than silently inheriting a guess.
func renderTerraform(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "# Layer this into the aws_lambda_function resource for %q.\n", spec.ServiceName)
	_, _ = fmt.Fprintf(&sb, "# Route: kind=%s segment=%s\n\n", spec.Route.Kind, spec.Route.RouteSegment)

	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "# D5: Shepherd does not hardcode ADOT layer ARNs — they are region/arch\n")
		_, _ = fmt.Fprintf(&sb, "# specific and drift between releases. Look yours up at\n")
		_, _ = fmt.Fprintf(&sb, "# %s and set it below.\n", adotLambdaDocsURL)
		_, _ = fmt.Fprintf(&sb, "variable %q {\n", "adot_layer_arn")
		_, _ = fmt.Fprintf(&sb, "  description = \"ADOT Lambda layer ARN for this function's runtime/arch/region — %s\"\n", adotLambdaDocsURL)
		_, _ = fmt.Fprintf(&sb, "  type        = string\n")
		_, _ = fmt.Fprintf(&sb, "}\n\n")
	}

	_, _ = fmt.Fprintf(&sb, "resource \"aws_lambda_function\" \"this\" {\n")
	_, _ = fmt.Fprintf(&sb, "  # ... function_name, role, handler, runtime, etc. unchanged ...\n\n")
	if spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "  layers = [var.adot_layer_arn]\n\n")
	}
	_, _ = fmt.Fprintf(&sb, "  environment {\n")
	_, _ = fmt.Fprintf(&sb, "    variables = {\n")
	for _, v := range lambdaEnv(base, spec) {
		_, _ = fmt.Fprintf(&sb, "      %s = %q\n", v.Key, v.Value)
	}
	_, _ = fmt.Fprintf(&sb, "    }\n")
	_, _ = fmt.Fprintf(&sb, "  }\n")
	_, _ = fmt.Fprintf(&sb, "}\n")
	return sb.String()
}
