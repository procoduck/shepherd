// Package simsvc implements shepherd-simulator: the S3 sandbox-run service of
// VB-1 §6.4. It runs the pinned Alloy binary as a child process against a
// transformed graph, serves the capture receivers and synthetic sources that
// graph was re-pointed at, and reports what actually arrived.
//
// The service is deliberately dumb: it never sees a graph, an org or a user —
// only rendered Alloy text plus an opaque component index it echoes back. All
// containment (non-root, read-only rootfs, dropped capabilities, no egress)
// lives at the container/pod level, which is why Alloy is a CHILD PROCESS here
// rather than a sibling container: every control the platform applies to this
// process applies to Alloy automatically, with no Docker socket in the picture.
package simsvc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the simulator's entire configuration. It is env-only on purpose:
// the service has no config file, no database and no org model, so a flag or
// file parser would be surface with no payload.
type Config struct {
	// Listen is the control API address. It is the ONLY listener that is
	// expected to be reachable from outside the container.
	Listen string
	// Token, when non-empty, is required as `Authorization: Bearer <token>`
	// on every /v1 request.
	Token string

	// CaptureListen serves the HTTP capture receivers the transform re-points
	// destinations at. SyntheticListen serves the exporter every stubbed
	// discovery target scrapes. OTLPGRPCListen and SyslogListen serve the two
	// destinations that are not HTTP.
	CaptureListen   string
	SyntheticListen string
	OTLPGRPCListen  string
	SyslogListen    string

	// AlloyBinary is the pinned Alloy the sandbox runs. AlloyHTTPListen is
	// where that child's own HTTP server binds — loopback only, because its
	// /api/v0/web/components endpoint exposes the running config and is read
	// by this process alone.
	AlloyBinary     string
	AlloyHTTPListen string
	StabilityLevel  string

	// QueueDepth is how many runs may wait behind the single running one
	// before the control API answers 429.
	QueueDepth      int
	DefaultDuration time.Duration
	MaxDuration     time.Duration
	// RunTTL bounds a run's whole lifetime from acceptance; ResultTTL is how
	// long a finished run's results stay pollable before the sweeper purges
	// them.
	RunTTL    time.Duration
	ResultTTL time.Duration

	// StorageDir is Alloy's --storage.path parent; RunDir holds one rendered
	// config per run. CaptureDir and LogDir are the two directories the
	// transform names in configs (otelcol.exporter.file writes into the
	// former, the synthetic log emitter into the latter), so they must match
	// SHEPHERD_SIMULATOR_CAPTURE_DIR / _LOG_DIR on the Shepherd side.
	StorageDir string
	RunDir     string
	CaptureDir string
	LogDir     string

	// AllowedHosts is the endpoint allowlist's host set (§6.4 defense in
	// depth). A submitted config naming an http(s) URL or host:port outside it
	// is refused before anything is written to disk.
	AllowedHosts []string

	// SATokenPath is the service-account token whose presence makes the
	// process refuse to start. Overridable only so the guard itself is
	// testable; production never sets it.
	SATokenPath string

	// MaxConfigBytes caps a submitted config; over it the API answers 413.
	MaxConfigBytes int64
}

// Defaults mirror the Shepherd-side simulator defaults in
// internal/config/config.go. Where the two must agree — capture host/port,
// synthetic exporter port, OTLP gRPC port, syslog port, capture dir, log dir —
// they are the same literals, and TestComposeContainment pins the compose
// files that carry them.
const (
	DefaultListen          = ":8099"
	DefaultCaptureListen   = ":9110"
	DefaultSyntheticListen = ":9111"
	DefaultOTLPGRPCListen  = ":4317"
	DefaultSyslogListen    = ":5514"
	DefaultAlloyBinary     = "/usr/local/bin/alloy"
	DefaultAlloyHTTPListen = "127.0.0.1:12345"
	DefaultStabilityLevel  = "generally-available"
	DefaultStorageDir      = "/tmp/shepherd-sim/storage"
	DefaultRunDir          = "/tmp/shepherd-sim/run"
	DefaultCaptureDir      = "/tmp/shepherd-sim/capture"
	DefaultLogDir          = "/tmp/shepherd-sim/logs"
	DefaultSATokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // a path to refuse on, not a credential
	DefaultMaxConfigBytes  = 1 << 20
)

// LoadConfig reads the SIM_* environment. It returns an error rather than
// falling back on a bad value, because a simulator that quietly runs with a
// 0-second duration or an unwritable run directory produces empty captures
// that read as pipeline faults.
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{
		Listen:          str(getenv, "SIM_LISTEN", DefaultListen),
		Token:           getenv("SIM_TOKEN"),
		CaptureListen:   str(getenv, "SIM_CAPTURE_LISTEN", DefaultCaptureListen),
		SyntheticListen: str(getenv, "SIM_SYNTHETIC_LISTEN", DefaultSyntheticListen),
		OTLPGRPCListen:  str(getenv, "SIM_OTLP_GRPC_LISTEN", DefaultOTLPGRPCListen),
		SyslogListen:    str(getenv, "SIM_SYSLOG_LISTEN", DefaultSyslogListen),
		AlloyBinary:     str(getenv, "SIM_ALLOY_BINARY", DefaultAlloyBinary),
		AlloyHTTPListen: str(getenv, "SIM_ALLOY_HTTP_LISTEN", DefaultAlloyHTTPListen),
		StabilityLevel:  str(getenv, "SIM_ALLOY_STABILITY_LEVEL", DefaultStabilityLevel),
		StorageDir:      str(getenv, "SIM_STORAGE_DIR", DefaultStorageDir),
		RunDir:          str(getenv, "SIM_RUN_DIR", DefaultRunDir),
		CaptureDir:      str(getenv, "SIM_CAPTURE_DIR", DefaultCaptureDir),
		LogDir:          str(getenv, "SIM_LOG_DIR", DefaultLogDir),
		SATokenPath:     str(getenv, "SIM_SA_TOKEN_PATH", DefaultSATokenPath),
		MaxConfigBytes:  DefaultMaxConfigBytes,
	}

	var err error
	if c.QueueDepth, err = intVal(getenv, "SIM_QUEUE_DEPTH", 4); err != nil {
		return Config{}, err
	}
	if c.DefaultDuration, err = durVal(getenv, "SIM_DEFAULT_DURATION", 30*time.Second); err != nil {
		return Config{}, err
	}
	if c.MaxDuration, err = durVal(getenv, "SIM_MAX_DURATION", 120*time.Second); err != nil {
		return Config{}, err
	}
	if c.RunTTL, err = durVal(getenv, "SIM_RUN_TTL", 5*time.Minute); err != nil {
		return Config{}, err
	}
	if c.ResultTTL, err = durVal(getenv, "SIM_RESULT_TTL", time.Hour); err != nil {
		return Config{}, err
	}

	c.AllowedHosts = allowedHosts(getenv)

	if c.DefaultDuration <= 0 || c.MaxDuration < c.DefaultDuration {
		return Config{}, fmt.Errorf("simsvc: SIM_DEFAULT_DURATION (%s) must be positive and no greater than SIM_MAX_DURATION (%s)", c.DefaultDuration, c.MaxDuration)
	}
	if c.QueueDepth < 0 {
		return Config{}, fmt.Errorf("simsvc: SIM_QUEUE_DEPTH must not be negative, got %d", c.QueueDepth)
	}
	return c, nil
}

// allowedHosts builds the endpoint allowlist. SIM_ALLOWED_HOSTS replaces the
// default set outright; the default covers the container's own hostname (which
// is what the compose service name resolves to) plus loopback, because those
// are the only names the transform can legitimately have written.
func allowedHosts(getenv func(string) string) []string {
	if raw := strings.TrimSpace(getenv("SIM_ALLOWED_HOSTS")); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, strings.ToLower(p))
			}
		}
		return out
	}
	hosts := []string{"localhost", "127.0.0.1", "::1", "shepherd-simulator"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, strings.ToLower(h))
	}
	return hosts
}

func str(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func intVal(getenv func(string) string, key string, def int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("simsvc: %s: %w", key, err)
	}
	return n, nil
}

func durVal(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("simsvc: %s: %w", key, err)
	}
	return d, nil
}
