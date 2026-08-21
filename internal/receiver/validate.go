package receiver

import (
	"fmt"
	"regexp"
)

// identRE matches a valid Alloy component label: an identifier, the same
// shape river.IsValidIdentifier accepts for a quoted label.
var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// identity tracks one rendered Alloy component (type + label) so Validate can
// refuse a Config that would emit the same component twice — Alloy would
// refuse it too, but with a compile error pointing at generated text instead
// of at the Go input that caused it.
type identity struct {
	component string
	label     string
}

// Validate checks cfg for internal consistency and for the deliberate-choice
// requirements this package exists to enforce (CORS, size/rate limits). It
// does not consult the Alloy schema itself — that is exercised separately by
// the schema-conformance test and by validating rendered output against the
// real pinned alloy binary (see receiver_conformance_test.go and
// receiver_alloy_test.go).
func Validate(cfg Config) error {
	if len(cfg.OTLP) == 0 && len(cfg.Faro) == 0 {
		return fmt.Errorf("receiver: config has no OTLP and no Faro pipelines")
	}

	seen := map[identity]bool{}
	claim := func(component, label string) error {
		id := identity{component, label}
		if seen[id] {
			return fmt.Errorf("receiver: duplicate %s %q — every component of the same type needs a distinct label", component, label)
		}
		seen[id] = true
		return nil
	}

	for i, p := range cfg.OTLP {
		if err := validateOTLPPipeline(p); err != nil {
			return fmt.Errorf("receiver: OTLP[%d] (%q): %w", i, p.Label, err)
		}
		if err := claim("otelcol.receiver.otlp", p.Label); err != nil {
			return err
		}
		if err := claim("otelcol.processor.batch", p.Label); err != nil {
			return err
		}
		for _, exp := range []*OTLPExporter{p.Metrics, p.Logs, p.Traces} {
			if exp == nil {
				continue
			}
			if err := claim(otlpExporterComponent(exp.Protocol), exp.Name); err != nil {
				return err
			}
		}
	}

	for i := range cfg.Faro {
		p := &cfg.Faro[i]
		if err := validateFaroPipeline(p); err != nil {
			return fmt.Errorf("receiver: Faro[%d] (%q): %w", i, p.Label, err)
		}
		if err := claim("faro.receiver", p.Label); err != nil {
			return err
		}
		if p.Logs != nil {
			if err := claim("loki.write", p.Logs.Name); err != nil {
				return err
			}
		}
		if p.Traces != nil {
			if err := claim("otelcol.processor.batch", faroTracesBatchLabel(p.Label)); err != nil {
				return err
			}
			if err := claim(otlpExporterComponent(p.Traces.Protocol), p.Traces.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

func otlpExporterComponent(proto ExporterProtocol) string {
	if proto == ExporterHTTP {
		return "otelcol.exporter.otlphttp"
	}
	return "otelcol.exporter.otlp"
}

func faroTracesBatchLabel(pipelineLabel string) string {
	return "faro_" + pipelineLabel + "_traces"
}

func validateLabel(field, label string) error {
	if !identRE.MatchString(label) {
		return fmt.Errorf("%s %q is not a valid Alloy identifier (want %s)", field, label, identRE.String())
	}
	return nil
}

func validateOTLPPipeline(p OTLPPipeline) error {
	if err := validateLabel("Label", p.Label); err != nil {
		return err
	}
	if p.HTTP == nil && p.GRPC == nil {
		return fmt.Errorf("at least one of HTTP or GRPC must be set")
	}
	if p.HTTP != nil {
		if p.HTTP.ListenAddr == "" {
			return fmt.Errorf("HTTP.ListenAddr must not be empty")
		}
		if p.HTTP.MaxRequestBodySize == "" {
			return fmt.Errorf("HTTP.MaxRequestBodySize must be set explicitly (no implicit limit)")
		}
	}
	if p.GRPC != nil {
		if p.GRPC.ListenAddr == "" {
			return fmt.Errorf("GRPC.ListenAddr must not be empty")
		}
		if p.GRPC.MaxRecvMsgSize == "" {
			return fmt.Errorf("GRPC.MaxRecvMsgSize must be set explicitly (no implicit limit)")
		}
		if p.GRPC.MaxConcurrentStreams <= 0 {
			return fmt.Errorf("GRPC.MaxConcurrentStreams must be > 0 (no implicit cap)")
		}
	}
	if p.Metrics == nil && p.Logs == nil && p.Traces == nil {
		return fmt.Errorf("at least one of Metrics, Logs, Traces must be set")
	}
	for field, exp := range map[string]*OTLPExporter{"Metrics": p.Metrics, "Logs": p.Logs, "Traces": p.Traces} {
		if exp == nil {
			continue
		}
		if err := validateOTLPExporter(*exp); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	return nil
}

func validateOTLPExporter(exp OTLPExporter) error {
	if err := validateLabel("Name", exp.Name); err != nil {
		return err
	}
	switch exp.Protocol {
	case ExporterGRPC, ExporterHTTP:
	default:
		return fmt.Errorf("protocol %q must be %q or %q", exp.Protocol, ExporterGRPC, ExporterHTTP)
	}
	if exp.EndpointExpr == "" {
		return fmt.Errorf("EndpointExpr must not be empty")
	}
	for k := range exp.Headers {
		if exp.SecretHeaderEnv[k] != "" {
			return fmt.Errorf("header %q is set in both Headers and SecretHeaderEnv", k)
		}
	}
	return nil
}

func validateFaroPipeline(p *FaroPipeline) error {
	if err := validateLabel("Label", p.Label); err != nil {
		return err
	}
	if p.Server.ListenAddr == "" {
		return fmt.Errorf("Server.ListenAddr must not be empty")
	}
	if p.Server.ListenPort <= 0 {
		return fmt.Errorf("Server.ListenPort must be > 0")
	}
	if p.Server.MaxAllowedPayloadSize == "" {
		return fmt.Errorf("Server.MaxAllowedPayloadSize must be set explicitly (no implicit limit)")
	}
	if err := validateRateLimit(p.Server.RateLimit); err != nil {
		return fmt.Errorf("Server.RateLimit: %w", err)
	}
	if err := validateCORS(p.CORS); err != nil {
		return fmt.Errorf("CORS: %w", err)
	}
	if p.Logs == nil && p.Traces == nil {
		return fmt.Errorf("at least one of Logs, Traces must be set")
	}
	if p.Logs != nil {
		if err := validateLabel("Logs.Name", p.Logs.Name); err != nil {
			return err
		}
		if p.Logs.URLExpr == "" {
			return fmt.Errorf("Logs.URLExpr must not be empty")
		}
	}
	if p.Traces != nil {
		if err := validateOTLPExporter(*p.Traces); err != nil {
			return fmt.Errorf("traces: %w", err)
		}
	}
	return nil
}

func validateRateLimit(rl RateLimit) error {
	if !rl.Enabled {
		// A caller that decided rate limiting should be off for this
		// pipeline has still made the decision explicitly (Enabled is a
		// bool field they had to set), so nothing further to check.
		return nil
	}
	switch rl.Strategy {
	case "global", "per_app":
	default:
		return fmt.Errorf("strategy %q must be %q or %q when Enabled", rl.Strategy, "global", "per_app")
	}
	if rl.Rate <= 0 {
		return fmt.Errorf("rate must be > 0 when Enabled")
	}
	if rl.BurstSize <= 0 {
		return fmt.Errorf("BurstSize must be > 0 when Enabled")
	}
	return nil
}

// validateCORS is the control this package exists to prove: an empty or
// wildcard cors_allowed_origins list must be reached deliberately.
//
// RED-RUN PROOF (see receiver_test.go "red run"): if this function's "*"
// check is deleted, a caller can put the literal string "*" directly into
// CORSPolicy.Origins — exactly like any other origin — and it renders
// unchanged as cors_allowed_origins = ["*"], silently granting every origin
// CORS access. With the check in place that one-line change is refused
// before it ever reaches Render.
func validateCORS(c CORSPolicy) error {
	if c.AllowAll && len(c.Origins) > 0 {
		return fmt.Errorf("AllowAll=true and Origins is non-empty — set exactly one")
	}
	for _, o := range c.Origins {
		if o == "*" {
			return fmt.Errorf("origins contains a literal \"*\" — wildcard must be requested via AllowAll, not as an origin string")
		}
		if o == "" {
			return fmt.Errorf("origins contains an empty string")
		}
	}
	return nil
}
