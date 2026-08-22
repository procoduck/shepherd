package beacon

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultTokenIDEnv and DefaultTokenSecretEnv are the environment variable
// names RenderBaselinePipeline reads the collector's own agent-token
// credentials from at runtime (sys.env(...) — the same secret-indirection
// convention internal/receiver/render.go already uses for bearer tokens).
//
// Why env vars and not a literal: recomputeServeCache/GetConfig cache and
// hash config PER COLLECTOR (cluster+role), shared byte-for-byte across
// every instance of that collector — internal/merge never renders anything
// instance-specific — and Shepherd only ever stores an agent token's
// sha256(secret) (internal/agentapi/auth.go), never the plaintext, so there
// is no secret this package could bake into the rendered text even if the
// cache were instance-scoped. The operator's LOCAL remotecfg block (out of
// Shepherd's control — docs/spec.md's "remote pipelines cannot reference
// local components" is about the component graph, not the OS process
// environment, which both local and remote-served config read from
// identically) already holds this same uuid:secret pair for its own Basic
// Auth to GetConfig; setting it under these two names makes the SAME pair
// readable to the baseline pipeline this package renders. Formalizing this
// as a documented operator contract (env var names, chart wiring) is a W9
// onboarding-artifact concern, not this package's — RenderBaselinePipeline
// only needs the names to agree with what it emits.
const (
	DefaultTokenIDEnv     = "SHEPHERD_AGENT_TOKEN_ID"
	DefaultTokenSecretEnv = "SHEPHERD_AGENT_TOKEN_SECRET"
)

// WritePath is where every rendered baseline pipeline's prometheus.remote_write
// points (NewBaselineConfig resolves it against config.ServerConfig.BaseURL),
// and where internal/server mounts internal/agentapi.NewBeaconHandler. Defined
// here rather than in internal/agentapi so that BOTH of Shepherd's config-serve
// paths — internal/agentapi's lazy recompute AND internal/mgmtapi's eager one
// (see AppendBaseline's doc comment) — can build a BaselineConfig without
// importing internal/agentapi just for this one constant.
const WritePath = "/beacon/v1/write"

// BaselineConfig configures RenderBaselinePipeline. Every field is validated
// by Validate — there is no implicit Alloy default this package lets a
// caller silently inherit for a field that exists here (mirroring
// internal/receiver/config.go's documented convention for the same reason:
// a value this pipeline's author did not think about should not be able to
// reach production).
type BaselineConfig struct {
	// Label is the Alloy component label suffix for every component this
	// pipeline renders, e.g. prometheus.exporter.self "<Label>". Required —
	// must be a valid Alloy identifier (checked by Validate).
	Label string
	// RemoteWriteURL is the absolute URL of Shepherd's own beacon write
	// endpoint (internal/agentapi.BeaconWritePath resolved against
	// config.ServerConfig.BaseURL), e.g.
	// "https://shepherd.example.com/beacon/v1/write". Required.
	RemoteWriteURL string
	// ScrapeInterval is the prometheus.scrape block's scrape_interval, e.g.
	// "60s". Required — see doc.go's caps discussion: this interval is also
	// the floor on how fast beacon_inventory rows can go stale, so it is a
	// deliberate choice, not an inherited default.
	ScrapeInterval string
	// TokenIDEnv and TokenSecretEnv name the environment variables the
	// rendered remote_write's basic_auth block reads via sys.env(...). Use
	// DefaultTokenIDEnv/DefaultTokenSecretEnv unless a caller has a specific
	// reason to diverge (both required non-empty either way).
	TokenIDEnv     string
	TokenSecretEnv string
}

// identRe and the checks in Validate mirror internal/merge.SanitizeName's
// target shape (lowercase, [a-z0-9_]) closely enough to guarantee the
// rendered component labels are valid Alloy identifiers, without importing
// internal/merge for it — this package has no other reason to depend on
// internal/merge, and duplicating a five-line character class is cheaper
// than the coupling.
func validLabel(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Validate checks cfg for the required-and-explicit fields BaselineConfig's
// doc comment promises. Called by RenderBaselinePipeline; exported so a
// caller (e.g. a future config-serving test) can check a value before
// spending a render on it.
func Validate(cfg BaselineConfig) error {
	var missing []string
	if cfg.Label == "" {
		missing = append(missing, "Label")
	} else if !validLabel(cfg.Label) {
		return fmt.Errorf("beacon: Label %q is not a valid Alloy identifier ([a-z_][a-z0-9_]*)", cfg.Label)
	}
	if cfg.RemoteWriteURL == "" {
		missing = append(missing, "RemoteWriteURL")
	}
	if cfg.ScrapeInterval == "" {
		missing = append(missing, "ScrapeInterval")
	}
	if cfg.TokenIDEnv == "" {
		missing = append(missing, "TokenIDEnv")
	}
	if cfg.TokenSecretEnv == "" {
		missing = append(missing, "TokenSecretEnv")
	}
	if len(missing) > 0 {
		return fmt.Errorf("beacon: BaselineConfig missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// errRenderInvalid wraps a Validate failure so RenderBaselinePipeline's
// error is unambiguous about which stage produced it, matching
// internal/receiver.Render's convention of validating before rendering a
// single byte.
var errRenderInvalid = errors.New("beacon: invalid BaselineConfig")

// RenderBaselinePipeline renders the D6 baseline pipeline: a self-scrape of
// the reporting Alloy's own component-health metric, filtered down to
// exactly the series Project understands, remote_written back to Shepherd
// under the same agent-token credentials the collector already holds.
//
// The relabel stage's keep rule is the OTHER half of "parse, project,
// discard" (the beacon-side half is Project): everything alloy_build_info,
// evaluation-timing histograms, and every other series
// prometheus.exporter.self's target exposes never leaves the collector at
// all, which is also a real contribution to the body-size cap (limits.go)
// never being the thing standing between a legitimate beacon and Shepherd.
func RenderBaselinePipeline(cfg BaselineConfig) (string, error) {
	if err := Validate(cfg); err != nil {
		return "", fmt.Errorf("%w: %w", errRenderInvalid, err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "prometheus.exporter.self %q { }\n\n", cfg.Label)

	fmt.Fprintf(&sb, "prometheus.scrape %q {\n", cfg.Label)
	fmt.Fprintf(&sb, "  targets         = prometheus.exporter.self.%s.targets\n", cfg.Label)
	fmt.Fprintf(&sb, "  forward_to      = [prometheus.relabel.%s.receiver]\n", cfg.Label)
	fmt.Fprintf(&sb, "  job_name        = \"shepherd-beacon\"\n")
	fmt.Fprintf(&sb, "  scrape_interval = %q\n", cfg.ScrapeInterval)
	sb.WriteString("}\n\n")

	// Keep exactly runningComponentsMetric (project.go): every other series
	// this target exposes is dropped here, before it is ever batched or sent
	// — never merely ignored on read, as leaving broader relabeling and
	// trusting Project to filter would still cost the size cap and the wire.
	fmt.Fprintf(&sb, "prometheus.relabel %q {\n", cfg.Label)
	fmt.Fprintf(&sb, "  forward_to = [prometheus.remote_write.%s.receiver]\n\n", cfg.Label)
	sb.WriteString("  rule {\n")
	fmt.Fprintf(&sb, "    source_labels = [\"__name__\"]\n")
	fmt.Fprintf(&sb, "    regex         = %q\n", runningComponentsMetric)
	sb.WriteString("    action        = \"keep\"\n")
	sb.WriteString("  }\n")
	sb.WriteString("}\n\n")

	fmt.Fprintf(&sb, "prometheus.remote_write %q {\n", cfg.Label)
	sb.WriteString("  endpoint {\n")
	fmt.Fprintf(&sb, "    url = %q\n\n", cfg.RemoteWriteURL)
	sb.WriteString("    basic_auth {\n")
	fmt.Fprintf(&sb, "      username = sys.env(%q)\n", cfg.TokenIDEnv)
	fmt.Fprintf(&sb, "      password = sys.env(%q)\n", cfg.TokenSecretEnv)
	sb.WriteString("    }\n")
	sb.WriteString("  }\n")
	sb.WriteString("}\n")

	return sb.String(), nil
}

// NewBaselineConfig returns the BaselineConfig every collector's baseline
// pipeline uses: fixed label and scrape interval, the default token env var
// names (see DefaultTokenIDEnv/DefaultTokenSecretEnv), and remoteWriteURL as
// given. remoteWriteURL may be empty; see AppendBaseline for what an empty
// URL means to a caller assembling served config.
func NewBaselineConfig(remoteWriteURL string) BaselineConfig {
	return BaselineConfig{
		Label:          "beacon",
		RemoteWriteURL: remoteWriteURL,
		ScrapeInterval: "60s",
		TokenIDEnv:     DefaultTokenIDEnv,
		TokenSecretEnv: DefaultTokenSecretEnv,
	}
}

// AppendBaseline is the single call both of Shepherd's serve paths use to
// add D6's baseline pipeline to an already-merged config —
// internal/agentapi's recomputeServeCache (the lazy path) and
// internal/mgmtapi's recomputeOrgCaches (the eager path). One function two
// callers share, rather than each re-implementing "render and concatenate",
// is what makes "the baseline reaches BOTH" (docs/gateway-tier-plan.md §10's
// W1 lesson, restated for W5) a property of the code and not of two people
// remembering to do the same thing the same way.
//
// cfg.RemoteWriteURL == "" means "beacon disabled for this serve" —
// content is returned unchanged, no error. A caller reaches this from an
// unset config.ServerConfig.BaseURL (e.g. an incomplete dev/test
// deployment); degrading to "no baseline appended" rather than failing the
// whole config-assembly call matches this repo's standard for an optional
// dependency (see D7's Grafana-absent handling: "Grafana absent means no
// outcome verification, never reduced function" — the same shape applies
// here one level down).
func AppendBaseline(content string, cfg BaselineConfig) (string, error) {
	if cfg.RemoteWriteURL == "" {
		return content, nil
	}
	baseline, err := RenderBaselinePipeline(cfg)
	if err != nil {
		return "", fmt.Errorf("beacon: rendering baseline pipeline: %w", err)
	}
	if content == "" {
		return baseline, nil
	}
	return content + "\n" + baseline, nil
}
