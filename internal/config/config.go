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
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	OIDC     OIDCConfig     `mapstructure:"oidc"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Graph    GraphConfig    `mapstructure:"graph"`
	Agent    AgentConfig    `mapstructure:"agent"`
	Validate ValidateConfig `mapstructure:"validate"`
	GitSync  GitSyncConfig  `mapstructure:"gitsync"`
	ADO      ADOConfig      `mapstructure:"ado"`
	Security SecurityConfig `mapstructure:"security"`
	Log      LogConfig      `mapstructure:"log"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Listen        string `mapstructure:"listen"`
	BaseURL       string `mapstructure:"base_url"`
	MetricsListen string `mapstructure:"metrics_listen"`
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	URL      string `mapstructure:"url"`
	MaxConns int32  `mapstructure:"max_conns"`
}

// OIDCConfig holds OIDC / Entra ID settings.
type OIDCConfig struct {
	Issuer       string   `mapstructure:"issuer"`
	ClientID     string   `mapstructure:"client_id"`
	ClientSecret string   `mapstructure:"client_secret"`
	RedirectURL  string   `mapstructure:"redirect_url"`
	Scopes       []string `mapstructure:"scopes"`
}

// LogValue redacts the OIDC client secret in structured logs.
func (c OIDCConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("issuer", c.Issuer), slog.String("client_id", c.ClientID), slog.String("client_secret", "***"), slog.String("redirect_url", c.RedirectURL))
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
	// gitsync fetch limits, per docs/git-provider-design.md §3.6.
	v.SetDefault("gitsync.max_repo_bytes", 50*1024*1024)
	v.SetDefault("gitsync.max_file_bytes", 1*1024*1024)
	v.SetDefault("gitsync.max_files", 500)
	v.SetDefault("gitsync.fetch_timeout", "60s")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("auth.local_admin.username", "admin")
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
		{"gitsync.tick", "SHEPHERD_GITSYNC_TICK"},
		{"gitsync.default_poll_interval", "SHEPHERD_GITSYNC_DEFAULT_POLL_INTERVAL"},
		{"gitsync.max_repo_bytes", "SHEPHERD_GITSYNC_MAX_REPO_BYTES"},
		{"gitsync.max_file_bytes", "SHEPHERD_GITSYNC_MAX_FILE_BYTES"},
		{"gitsync.max_files", "SHEPHERD_GITSYNC_MAX_FILES"},
		{"gitsync.fetch_timeout", "SHEPHERD_GITSYNC_FETCH_TIMEOUT"},
		{"ado.base_url", "SHEPHERD_ADO_BASE_URL"},
		{"security.encryption_key", "SHEPHERD_SECURITY_ENCRYPTION_KEY"},
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
