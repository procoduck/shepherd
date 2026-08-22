package onboarding

// envVar is one environment variable this package emits. A fixed-order
// slice, not a map: a human reads these top to bottom, and the order
// groups "where telemetry goes" before "what to call it" consistently
// across every renderer.
type envVar struct {
	Key   string
	Value string
}

// awsLambdaExecWrapper is the ADOT Lambda layer's auto-instrumentation
// wrapper value, verified against AWS's own ADOT Lambda docs
// (https://aws-otel.github.io/docs/getting-started/lambda, 2026-08-22):
// "AWS_LAMBDA_EXEC_WRAPPER=/opt/otel-instrument". Only meaningful alongside
// the ADOT layer (ConnectAppSpec.IncludeADOTLayer set) — a Lambda function
// without that layer attached has no /opt/otel-instrument to wrap with.
const awsLambdaExecWrapper = "/opt/otel-instrument"

// coreOTLPEnv returns the OTLP env vars every "connect an app" surface
// sets, regardless of runtime (Lambda, container, Kubernetes, bare SDK
// init).
func coreOTLPEnv(base string, spec ConnectAppSpec) []envVar {
	return []envVar{
		{"OTEL_EXPORTER_OTLP_ENDPOINT", base},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", string(spec.Protocol)},
		{"OTEL_SERVICE_NAME", spec.ServiceName},
	}
}

// lambdaEnv returns coreOTLPEnv plus the ADOT wrapper variable, when
// spec.IncludeADOTLayer says a layer is being attached. Shared by every
// Lambda-facing renderer (render_lambda.go, render_terraform.go,
// render_sam.go, render_cdk.go) so the four artifacts can never disagree
// about which env vars a Lambda deployment needs.
func lambdaEnv(base string, spec ConnectAppSpec) []envVar {
	vars := coreOTLPEnv(base, spec)
	if spec.IncludeADOTLayer {
		vars = append(vars, envVar{"AWS_LAMBDA_EXEC_WRAPPER", awsLambdaExecWrapper})
	}
	return vars
}
