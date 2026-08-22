package onboarding

// Bundle is every "connect an app" onboarding artifact rendered for one
// ConnectAppSpec.
type Bundle struct {
	// Env is a plain KEY=VALUE .env file: local dev, `docker run
	// --env-file`, or a Lambda console's "paste as text" environment editor.
	Env string
	// Lambda is a JSON environment-variables snippet for
	// `aws lambda update-function-configuration --environment file://env.json`.
	Lambda string
	// Terraform is an HCL fragment for an existing aws_lambda_function
	// resource.
	Terraform string
	// SAM is a template.yaml fragment for an AWS::Serverless::Function.
	SAM string
	// CDK is a TypeScript fragment for an existing lambda.Function.
	CDK string
	// K8s is a Kubernetes pod template container `env` fragment.
	K8s string
	// SDKNotes is a Markdown document explaining the env vars above: why
	// they are usually enough with no code change, the base-vs-per-signal
	// endpoint boundary, and the D10 Faro+OTLP hybrid.
	SDKNotes string
}

// Render validates spec and produces every onboarding artifact for it.
// Every artifact is derived from the SAME BaseEndpoint call — one place
// computes the route's actual path (endpoint.go), every renderer below only
// ever formats that same string into a different file shape. See
// TestAllEmittedEndpointsResolveToTheRenderedRoute for the property test
// this exists to make checkable across every artifact in Bundle at once,
// not just the ones a human happened to review.
func Render(spec ConnectAppSpec) (Bundle, error) {
	if err := spec.validate(); err != nil {
		return Bundle{}, err
	}
	base, err := BaseEndpoint(spec.GatewayBaseURL, spec.Route)
	if err != nil {
		return Bundle{}, err
	}

	sdkNotes, err := renderSDKNotes(base, spec)
	if err != nil {
		return Bundle{}, err
	}

	return Bundle{
		Env:       renderEnv(base, spec),
		Lambda:    renderLambda(base, spec),
		Terraform: renderTerraform(base, spec),
		SAM:       renderSAM(base, spec),
		CDK:       renderCDK(base, spec),
		K8s:       renderK8s(base, spec),
		SDKNotes:  sdkNotes,
	}, nil
}
