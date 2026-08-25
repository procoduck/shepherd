// Package config defines the Shepherd server configuration schema and loader.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level Shepherd configuration.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	OIDC      OIDCConfig      `mapstructure:"oidc"`
	Auth      AuthConfig      `mapstructure:"auth"`
	Graph     GraphConfig     `mapstructure:"graph"`
	Agent     AgentConfig     `mapstructure:"agent"`
	Validate  ValidateConfig  `mapstructure:"validate"`
	Simulator SimulatorConfig `mapstructure:"simulator"`
	GitSync   GitSyncConfig   `mapstructure:"gitsync"`
	ADO       ADOConfig       `mapstructure:"ado"`
	Security  SecurityConfig  `mapstructure:"security"`
	Tracing   TracingConfig   `mapstructure:"tracing"`
	Log       LogConfig       `mapstructure:"log"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Listen        string `mapstructure:"listen"`
	BaseURL       string `mapstructure:"base_url"`
	MetricsListen string `mapstructure:"metrics_listen"`
	// BeaconDisabled turns off D6's beacon entirely: no ingest endpoint is
	// mounted and no baseline pipeline is appended to any served config.
	//
	// D6 settles that the beacon is NOT opt-in — "the collector we know
	// nothing about is precisely the one that would never opt in" — so this
	// defaults to false and the beacon is on. Opting OUT is a different thing
	// from opting in, and an operator needs it: the beacon adds an ingest
	// endpoint and stores component inventory, and R2 (the beacon's data
	// review) is not signed. An operator who cannot answer for that yet must
	// be able to turn it off without downgrading, rather than discovering it
	// running after an upgrade.
	BeaconDisabled bool `mapstructure:"beacon_disabled"`
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	URL      string `mapstructure:"url"`
	MaxConns int32  `mapstructure:"max_conns"`
}

// OIDCConfig holds OIDC settings supplied by the Helm chart / environment.
//
// Issuer is the switch that decides where OIDC configuration comes from at
// all: non-empty means the chart owns it and the UI-managed oidc_settings row
// (migration 0014) is ignored and shown read-only; empty means an app admin
// may configure a provider from the admin UI instead. Chart config winning is
// deliberate — a cluster whose identity provider is declared in git must not
// be silently re-pointed by whoever holds an app-admin session.
//
// The claim/provider fields below default to the Entra-shaped behaviour this
// config had before it could describe any other provider, so an existing
// values.yaml keeps working unchanged.
type OIDCConfig struct {
	Issuer       string   `mapstructure:"issuer"`
	ClientID     string   `mapstructure:"client_id"`
	ClientSecret string   `mapstructure:"client_secret"`
	RedirectURL  string   `mapstructure:"redirect_url"`
	Scopes       []string `mapstructure:"scopes"`

	// Provider is a preset key from internal/auth.Presets ("entra", "okta",
	// "google", ...). It selects nothing at login time on its own — every
	// value it would prefill is spelled out in the fields below — but it does
	// decide the UseGraphGroups default and labels the deployment in the UI.
	Provider string `mapstructure:"provider"`
	// DisplayName is the login button label ("Continue with Okta").
	DisplayName string `mapstructure:"display_name"`

	// Claim names. SubjectClaim defaults to "oid" (Entra's immutable object
	// ID, what this code has always read) rather than the spec's "sub"; a
	// non-Entra deployment must set it to "sub".
	SubjectClaim string `mapstructure:"subject_claim"`
	EmailClaim   string `mapstructure:"email_claim"`
	NameClaim    string `mapstructure:"name_claim"`
	GroupsClaim  string `mapstructure:"groups_claim"`

	// UseGraphGroups resolves group membership through Microsoft Graph's
	// transitiveMemberOf instead of the ID token's groups claim. Defaults to
	// true when Provider is "entra" (preserving the pre-existing behaviour,
	// which had no other option) and false otherwise.
	UseGraphGroups bool `mapstructure:"use_graph_groups"`
}

// LogValue redacts the OIDC client secret in structured logs.
func (c OIDCConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("issuer", c.Issuer), slog.String("client_id", c.ClientID), slog.String("client_secret", "***"), slog.String("redirect_url", c.RedirectURL), slog.String("provider", c.Provider), slog.String("groups_claim", c.GroupsClaim), slog.Bool("use_graph_groups", c.UseGraphGroups))
}

// AuthConfig holds session and RBAC settings.
type AuthConfig struct {
	AppAdminGroupIDs []string         `mapstructure:"app_admin_group_ids"`
	SessionTTL       time.Duration    `mapstructure:"session_ttl"`
	LocalAdmin       LocalAdminConfig `mapstructure:"local_admin"`
	// InsecureCookies disables Secure flag on auth cookies. Only for non-TLS local dev.
	InsecureCookies bool `mapstructure:"insecure_cookies"`
}

// LocalAdminConfig holds break-glass local admin account settings.
type LocalAdminConfig struct {
	Enabled       bool          `mapstructure:"enabled"`
	AllowWithOIDC bool          `mapstructure:"allow_with_oidc"`
	Username      string        `mapstructure:"username"`
	PasswordHash  string        `mapstructure:"password_hash"`
	SessionTTL    time.Duration `mapstructure:"session_ttl"`
}

// LogValue redacts the password hash in structured logs.
func (c LocalAdminConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled),
		slog.Bool("allow_with_oidc", c.AllowWithOIDC),
		slog.String("username", c.Username),
		slog.String("password_hash", "[REDACTED]"),
		slog.Duration("session_ttl", c.SessionTTL),
	)
}

// GraphConfig holds Microsoft Graph API client settings.
type GraphConfig struct {
	TenantID     string `mapstructure:"tenant_id"`
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	BaseURL      string `mapstructure:"base_url"`
}

// LogValue redacts the Graph client secret in structured logs.
func (c GraphConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("tenant_id", c.TenantID), slog.String("client_id", c.ClientID), slog.String("client_secret", "***"), slog.String("base_url", c.BaseURL))
}

// AgentConfig holds collector lifecycle settings.
type AgentConfig struct {
	InactiveAfter time.Duration `mapstructure:"inactive_after"`
	DeleteAfter   time.Duration `mapstructure:"delete_after"`
	// SweepInterval controls how often the lifecycle sweeper runs.
	SweepInterval time.Duration `mapstructure:"sweep_interval"`
}

// ValidateConfig holds pipeline validation settings.
type ValidateConfig struct {
	AlloyBinary    string        `mapstructure:"alloy_binary"`
	StabilityLevel string        `mapstructure:"stability_level"`
	Timeout        time.Duration `mapstructure:"timeout"`
	// Stage3Timeout is the budget for the Stage 3 merge dry-run validation.
	Stage3Timeout time.Duration `mapstructure:"stage3_timeout"`
}

// SimulatorConfig holds the S3 sandbox-run settings (VB-1 §6.4): the addresses
// of the shepherd-simulator capture harness and the run budget. The transform
// takes these as an argument rather than reading a constant, so the compose
// service name lives here and in one compose env block only — a Kubernetes
// deployment overriding them is a config change, not a code change.
type SimulatorConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// CaptureBaseURL is scheme+host+port with no trailing slash; every HTTP
	// capture receiver hangs off it.
	CaptureBaseURL string `mapstructure:"capture_base_url"`
	// OTLPGRPCAddress is a bare host:port. The otelcol gRPC client takes an
	// address, not a URL, which is why one base URL cannot describe the harness.
	OTLPGRPCAddress string `mapstructure:"otlp_grpc_address"`
	SyslogHost      string `mapstructure:"syslog_host"`
	SyslogPort      int    `mapstructure:"syslog_port"`
	// CaptureDir is the tmpfs directory otelcol.exporter.file writes into.
	CaptureDir string `mapstructure:"capture_dir"`
	// TargetAddress is the synthetic metrics exporter every stubbed discovery
	// target points at.
	TargetAddress string `mapstructure:"target_address"`
	// LogDir is the tmpfs directory the synthetic log emitter writes fixture
	// files into, tailed by the loki.source.file stubs.
	LogDir      string        `mapstructure:"log_dir"`
	Duration    time.Duration `mapstructure:"duration"`
	MaxDuration time.Duration `mapstructure:"max_duration"`

	// -- Run API (VB-1 §6.4/§13 M7): internal/simulate.RunWorker settings. --

	// ControlURL is scheme+host+port of the simulator's control API
	// (POST/GET /v1/runs), no trailing slash. Distinct from CaptureBaseURL,
	// which is the sandbox Alloy's destination, not Shepherd's.
	ControlURL string `mapstructure:"control_url"`
	// Token is the shared bearer token the control API expects, when the
	// simulator has one configured (simsvc.Config.Token).
	Token string `mapstructure:"token"`
	// MaxConcurrentRuns bounds how many runs RunWorker claims and manages at
	// once, cluster-wide (enforced via Postgres advisory locks, not a
	// per-replica semaphore — see internal/simulate.RunWorker).
	MaxConcurrentRuns int `mapstructure:"max_concurrent_runs"`
	// MaxNonTerminalPerOrg caps how many queued/running runs one org may
	// have outstanding at once, so a single org cannot starve every other
	// org's queue behind MaxConcurrentRuns.
	MaxNonTerminalPerOrg int `mapstructure:"max_non_terminal_per_org"`
	// RunTTL: a run still queued/running this long after created_at is
	// force-expired by the janitor tick. Also the orphan-reclaim window for
	// a worker that crashes mid-run.
	RunTTL time.Duration `mapstructure:"run_ttl"`
	// RetentionTTL: a run in a terminal state is purged this long after
	// finished_at.
	RetentionTTL time.Duration `mapstructure:"retention_ttl"`
	// PollInterval is how often RunWorker looks for a queued run to claim.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// JanitorInterval is how often RunWorker sweeps for expired/purgeable runs.
	JanitorInterval time.Duration `mapstructure:"janitor_interval"`
}

// LogValue redacts the simulator control API bearer token in structured logs.
func (c SimulatorConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled),
		slog.String("control_url", c.ControlURL),
		slog.String("token", "***"),
		slog.Int("max_concurrent_runs", c.MaxConcurrentRuns),
		slog.Int("max_non_terminal_per_org", c.MaxNonTerminalPerOrg),
	)
}

// GitSyncConfig holds git-sync reconciler settings, including the fetch
// resource limits from docs/git-provider-design.md §3.6 that bound one
// internal/gitrepo fetch (LatestCommit or Files).
type GitSyncConfig struct {
	Tick                time.Duration `mapstructure:"tick"`
	DefaultPollInterval time.Duration `mapstructure:"default_poll_interval"`
	// MaxRepoBytes caps the total bytes transferred from one repo per fetch.
	MaxRepoBytes int64 `mapstructure:"max_repo_bytes"`
	// MaxFileBytes caps the size of any single *.alloy file; an oversized
	// file is skipped rather than failing the whole fetch.
	MaxFileBytes int64 `mapstructure:"max_file_bytes"`
	// MaxFiles caps the number of *.alloy files one fetch may return.
	MaxFiles int `mapstructure:"max_files"`
	// FetchTimeout bounds the wall-clock time of one LatestCommit or Files call.
	FetchTimeout time.Duration `mapstructure:"fetch_timeout"`
}

// ADOConfig holds Azure DevOps base URL override (for testing).
type ADOConfig struct {
	BaseURL string `mapstructure:"base_url"`
}

// SecurityConfig holds encryption settings.
type SecurityConfig struct {
	EncryptionKey string `mapstructure:"encryption_key"`
}

// LogValue redacts the encryption key in structured logs.
func (c SecurityConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("encryption_key", "***"))
}

// TracingConfig holds OpenTelemetry trace export settings.
//
// Endpoint is the switch: empty means tracing is off entirely — no provider,
// no exporter, no cost — which is the default. Shepherd instruments its own
// serving paths unconditionally, so this being genuinely free when unset is
// what makes instrumenting the hot agent path defensible.
type TracingConfig struct {
	// Endpoint is the OTLP collector address, e.g. "otel-collector:4317"
	// (grpc) or "otel-collector:4318" (http). A scheme is tolerated and
	// stripped.
	Endpoint string `mapstructure:"endpoint"`
	// Protocol is "grpc" (default) or "http".
	Protocol string `mapstructure:"protocol"`
	// Insecure disables TLS to the collector. Normal for an in-cluster
	// collector reached over the pod network.
	Insecure bool `mapstructure:"insecure"`
	// SampleRatio is the head-sampling ratio for traces this process roots,
	// 0.0-1.0. A caller's own sampling decision is always honoured
	// (ParentBased), so this only governs traces that start here.
	SampleRatio float64 `mapstructure:"sample_ratio"`
	// ServiceName and ServiceVersion label the resource in the trace backend.
	ServiceName    string `mapstructure:"service_name"`
	ServiceVersion string `mapstructure:"service_version"`
}

// LogConfig holds structured logging settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load reads and validates the Shepherd configuration from file and environment.
func Load(file string) (*Config, error) {
	v := viper.New()
	v.SetDefault("server.listen", ":8080")
	v.SetDefault("server.base_url", "https://shepherd.example.internal")
	v.SetDefault("server.metrics_listen", ":9090")
	v.SetDefault("database.max_conns", 20)
	v.SetDefault("graph.base_url", "https://graph.microsoft.com")
	v.SetDefault("agent.sweep_interval", "5m")
	v.SetDefault("validate.stage3_timeout", "30s")
	// Simulator defaults are the dev/compose service; a Kubernetes deploy
	// overrides them through the SHEPHERD_SIMULATOR_* bindings below.
	v.SetDefault("simulator.capture_base_url", "http://shepherd-simulator:9110")
	v.SetDefault("simulator.otlp_grpc_address", "shepherd-simulator:4317")
	v.SetDefault("simulator.syslog_host", "shepherd-simulator")
	v.SetDefault("simulator.syslog_port", 5514)
	v.SetDefault("simulator.capture_dir", "/tmp/shepherd-sim/capture")
	v.SetDefault("simulator.target_address", "shepherd-simulator:9111")
	v.SetDefault("simulator.log_dir", "/tmp/shepherd-sim/logs")
	v.SetDefault("simulator.duration", "30s")
	v.SetDefault("simulator.max_duration", "120s")
	v.SetDefault("simulator.control_url", "http://shepherd-simulator:8099")
	v.SetDefault("simulator.max_concurrent_runs", 2)
	v.SetDefault("simulator.max_non_terminal_per_org", 3)
	v.SetDefault("simulator.run_ttl", "5m")
	v.SetDefault("simulator.retention_ttl", "1h")
	v.SetDefault("simulator.poll_interval", "2s")
	v.SetDefault("simulator.janitor_interval", "30s")
	// gitsync fetch limits, per docs/git-provider-design.md §3.6.
	v.SetDefault("gitsync.max_repo_bytes", 50*1024*1024)
	v.SetDefault("gitsync.max_file_bytes", 1*1024*1024)
	v.SetDefault("gitsync.max_files", 500)
	v.SetDefault("gitsync.fetch_timeout", "60s")
	v.SetDefault("tracing.protocol", "grpc")
	v.SetDefault("tracing.insecure", true)
	v.SetDefault("tracing.sample_ratio", 0.1)
	v.SetDefault("tracing.service_name", "shepherd")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	// A zero session TTL makes every session expire the instant it is created
	// (createSessionAndSetCookie refuses a non-future expiry), so this needs a
	// value even when nothing configures auth at all — which is now reachable,
	// since OIDC can be turned on from the UI in a deployment whose chart
	// never set an auth block. Matches the chart's own default.
	v.SetDefault("auth.session_ttl", "8h")
	v.SetDefault("auth.local_admin.username", "admin")
	// OIDC claim/provider defaults reproduce the Entra-only behaviour this
	// code had before it could describe any other provider, so an existing
	// chart values.yaml keeps authenticating exactly as it did.
	// Without this default, a deployment configured purely through
	// SHEPHERD_OIDC_* env vars (no chart values.yaml — e2e/docker-compose.e2e.yaml
	// is exactly that shape) would arrive with a nil scope list, and
	// auth.Settings.Normalize would then substitute the provider preset's
	// scopes. For the "entra" default that silently adds GroupMember.Read.All
	// to the authorize request, which needs admin consent in a real tenant and
	// turns a working login into AADSTS65001. Matching the chart's own default
	// keeps what is sent to the IdP a thing the operator chose.
	v.SetDefault("oidc.scopes", []string{"openid", "profile", "email"})
	v.SetDefault("oidc.provider", "entra")
	v.SetDefault("oidc.display_name", "Microsoft")
	v.SetDefault("oidc.subject_claim", "oid")
	v.SetDefault("oidc.email_claim", "email")
	v.SetDefault("oidc.name_claim", "name")
	v.SetDefault("oidc.groups_claim", "groups")
	v.SetDefault("auth.local_admin.session_ttl", "1h")
	v.SetEnvPrefix("SHEPHERD")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	// Viper v1.19+ requires explicit BindEnv for nested keys to be resolved
	// from environment variables when no config file is present.
	// See: https://github.com/spf13/viper/issues/188
	for _, pair := range []struct{ key, env string }{
		{"server.listen", "SHEPHERD_SERVER_LISTEN"},
		{"server.base_url", "SHEPHERD_SERVER_BASE_URL"},
		{"server.metrics_listen", "SHEPHERD_SERVER_METRICS_LISTEN"},
		{"database.url", "SHEPHERD_DATABASE_URL"},
		{"database.max_conns", "SHEPHERD_DATABASE_MAX_CONNS"},
		{"oidc.issuer", "SHEPHERD_OIDC_ISSUER"},
		{"oidc.client_id", "SHEPHERD_OIDC_CLIENT_ID"},
		{"oidc.client_secret", "SHEPHERD_OIDC_CLIENT_SECRET"},
		{"oidc.redirect_url", "SHEPHERD_OIDC_REDIRECT_URL"},
		{"oidc.scopes", "SHEPHERD_OIDC_SCOPES"},
		{"oidc.provider", "SHEPHERD_OIDC_PROVIDER"},
		{"oidc.display_name", "SHEPHERD_OIDC_DISPLAY_NAME"},
		{"oidc.subject_claim", "SHEPHERD_OIDC_SUBJECT_CLAIM"},
		{"oidc.email_claim", "SHEPHERD_OIDC_EMAIL_CLAIM"},
		{"oidc.name_claim", "SHEPHERD_OIDC_NAME_CLAIM"},
		{"oidc.groups_claim", "SHEPHERD_OIDC_GROUPS_CLAIM"},
		{"oidc.use_graph_groups", "SHEPHERD_OIDC_USE_GRAPH_GROUPS"},
		{"auth.app_admin_group_ids", "SHEPHERD_AUTH_APP_ADMIN_GROUP_IDS"},
		{"auth.session_ttl", "SHEPHERD_AUTH_SESSION_TTL"},
		{"auth.insecure_cookies", "SHEPHERD_AUTH_INSECURE_COOKIES"},
		{"auth.local_admin.enabled", "SHEPHERD_AUTH_LOCAL_ADMIN_ENABLED"},
		{"auth.local_admin.allow_with_oidc", "SHEPHERD_AUTH_LOCAL_ADMIN_ALLOW_WITH_OIDC"},
		{"auth.local_admin.username", "SHEPHERD_AUTH_LOCAL_ADMIN_USERNAME"},
		{"auth.local_admin.password_hash", "SHEPHERD_AUTH_LOCAL_ADMIN_PASSWORD_HASH"},
		{"auth.local_admin.session_ttl", "SHEPHERD_AUTH_LOCAL_ADMIN_SESSION_TTL"},
		{"graph.tenant_id", "SHEPHERD_GRAPH_TENANT_ID"},
		{"graph.client_id", "SHEPHERD_GRAPH_CLIENT_ID"},
		{"graph.client_secret", "SHEPHERD_GRAPH_CLIENT_SECRET"},
		{"graph.base_url", "SHEPHERD_GRAPH_BASE_URL"},
		{"agent.inactive_after", "SHEPHERD_AGENT_INACTIVE_AFTER"},
		{"agent.delete_after", "SHEPHERD_AGENT_DELETE_AFTER"},
		{"agent.sweep_interval", "SHEPHERD_AGENT_SWEEP_INTERVAL"},
		{"validate.alloy_binary", "SHEPHERD_VALIDATE_ALLOY_BINARY"},
		{"validate.stability_level", "SHEPHERD_VALIDATE_STABILITY_LEVEL"},
		{"validate.timeout", "SHEPHERD_VALIDATE_TIMEOUT"},
		{"validate.stage3_timeout", "SHEPHERD_VALIDATE_STAGE3_TIMEOUT"},
		{"simulator.enabled", "SHEPHERD_SIMULATOR_ENABLED"},
		{"simulator.capture_base_url", "SHEPHERD_SIMULATOR_CAPTURE_BASE_URL"},
		{"simulator.otlp_grpc_address", "SHEPHERD_SIMULATOR_OTLP_GRPC_ADDRESS"},
		{"simulator.syslog_host", "SHEPHERD_SIMULATOR_SYSLOG_HOST"},
		{"simulator.syslog_port", "SHEPHERD_SIMULATOR_SYSLOG_PORT"},
		{"simulator.capture_dir", "SHEPHERD_SIMULATOR_CAPTURE_DIR"},
		{"simulator.target_address", "SHEPHERD_SIMULATOR_TARGET_ADDRESS"},
		{"simulator.log_dir", "SHEPHERD_SIMULATOR_LOG_DIR"},
		{"simulator.duration", "SHEPHERD_SIMULATOR_DURATION"},
		{"simulator.max_duration", "SHEPHERD_SIMULATOR_MAX_DURATION"},
		{"simulator.control_url", "SHEPHERD_SIMULATOR_CONTROL_URL"},
		{"simulator.token", "SHEPHERD_SIMULATOR_TOKEN"},
		{"simulator.max_concurrent_runs", "SHEPHERD_SIMULATOR_MAX_CONCURRENT_RUNS"},
		{"simulator.max_non_terminal_per_org", "SHEPHERD_SIMULATOR_MAX_NON_TERMINAL_PER_ORG"},
		{"simulator.run_ttl", "SHEPHERD_SIMULATOR_RUN_TTL"},
		{"simulator.retention_ttl", "SHEPHERD_SIMULATOR_RETENTION_TTL"},
		{"simulator.poll_interval", "SHEPHERD_SIMULATOR_POLL_INTERVAL"},
		{"simulator.janitor_interval", "SHEPHERD_SIMULATOR_JANITOR_INTERVAL"},
		{"gitsync.tick", "SHEPHERD_GITSYNC_TICK"},
		{"gitsync.default_poll_interval", "SHEPHERD_GITSYNC_DEFAULT_POLL_INTERVAL"},
		{"gitsync.max_repo_bytes", "SHEPHERD_GITSYNC_MAX_REPO_BYTES"},
		{"gitsync.max_file_bytes", "SHEPHERD_GITSYNC_MAX_FILE_BYTES"},
		{"gitsync.max_files", "SHEPHERD_GITSYNC_MAX_FILES"},
		{"gitsync.fetch_timeout", "SHEPHERD_GITSYNC_FETCH_TIMEOUT"},
		{"ado.base_url", "SHEPHERD_ADO_BASE_URL"},
		{"security.encryption_key", "SHEPHERD_SECURITY_ENCRYPTION_KEY"},
		{"tracing.endpoint", "SHEPHERD_TRACING_ENDPOINT"},
		{"tracing.protocol", "SHEPHERD_TRACING_PROTOCOL"},
		{"tracing.insecure", "SHEPHERD_TRACING_INSECURE"},
		{"tracing.sample_ratio", "SHEPHERD_TRACING_SAMPLE_RATIO"},
		{"tracing.service_name", "SHEPHERD_TRACING_SERVICE_NAME"},
		{"tracing.service_version", "SHEPHERD_TRACING_SERVICE_VERSION"},
		{"log.level", "SHEPHERD_LOG_LEVEL"},
		{"log.format", "SHEPHERD_LOG_FORMAT"},
	} {
		_ = v.BindEnv(pair.key, pair.env) //nolint:errcheck // static key/env pairs; error only if key is empty
	}
	if file != "" {
		v.SetConfigFile(file)
	} else {
		v.SetConfigName("shepherd")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/shepherd")
	}
	if err := v.ReadInConfig(); err != nil {
		if !errors.As(err, &viper.ConfigFileNotFoundError{}) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	if c.Database.URL == "" {
		return nil, fmt.Errorf("configuration errors:\n  - database.url is required")
	}
	if c.Security.EncryptionKey == "" {
		return nil, fmt.Errorf("configuration errors:\n  - security.encryption_key is required (base64 32 bytes)")
	}
	key, err := base64.StdEncoding.DecodeString(c.Security.EncryptionKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("configuration errors:\n  - security.encryption_key must be a base64-encoded 32-byte value")
	}
	// UseGraphGroups has no SetDefault because its default depends on another
	// key: Graph is Entra's directory API, so "on" is right for Entra and
	// meaningless anywhere else. IsSet distinguishes "the operator chose
	// false" from "the operator said nothing".
	if !v.IsSet("oidc.use_graph_groups") {
		c.OIDC.UseGraphGroups = c.OIDC.Provider == "entra"
	}
	if c.Auth.LocalAdmin.Enabled {
		if c.Auth.LocalAdmin.PasswordHash == "" {
			return nil, fmt.Errorf("configuration errors:\n  - auth.local_admin.password_hash is required when local admin is enabled")
		}
		if !strings.HasPrefix(c.Auth.LocalAdmin.PasswordHash, "$argon2id$") {
			return nil, fmt.Errorf("configuration errors:\n  - auth.local_admin.password_hash must be a valid argon2id encoded string")
		}
		if c.OIDC.Issuer != "" && !c.Auth.LocalAdmin.AllowWithOIDC {
			return nil, fmt.Errorf("configuration errors:\n  - local admin is enabled with OIDC configured; set auth.local_admin.allow_with_oidc=true to allow this (break-glass with OIDC active)")
		}
		if c.Auth.LocalAdmin.SessionTTL < 5*time.Minute {
			return nil, fmt.Errorf("configuration errors:\n  - auth.local_admin.session_ttl must be >= 5m")
		}
	}
	return &c, nil
}
