package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLoggingConfiguration(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	tests := []struct {
		name   string
		file   string
		env    string
		level  string
		format string
	}{
		{name: "default", level: "info", format: "json"},
		{name: "config file", file: "log:\n  level: debug\n  format: text\n", level: "debug", format: "text"},
		{name: "environment", env: "debug", level: "debug", format: "json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
			t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", key)
			if tt.env != "" {
				t.Setenv("SHEPHERD_LOG_LEVEL", tt.env)
			} else {
				if err := os.Unsetenv("SHEPHERD_LOG_LEVEL"); err != nil {
					t.Fatal(err)
				}
			}
			file := ""
			if tt.file != "" {
				file = filepath.Join(t.TempDir(), "shepherd.yaml")
				if err := os.WriteFile(file, []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(file)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Log.Level != tt.level || cfg.Log.Format != tt.format {
				t.Fatalf("logging config = (%q, %q), want (%q, %q)", cfg.Log.Level, cfg.Log.Format, tt.level, tt.format)
			}
		})
	}
}

// TestGitSyncLimitDefaults locks in the defaults from
// docs/git-provider-design.md §3.6, which internal/gitrepo.DefaultLimits
// duplicates as untyped constants (config intentionally has no dependency
// on gitrepo).
func TestGitSyncLimitDefaults(t *testing.T) {
	t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
	t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	const (
		wantMaxRepoBytes = 50 * 1024 * 1024
		wantMaxFileBytes = 1 * 1024 * 1024
		wantMaxFiles     = 500
		wantFetchTimeout = 60 * time.Second
	)
	if cfg.GitSync.MaxRepoBytes != wantMaxRepoBytes {
		t.Errorf("gitsync.max_repo_bytes = %d, want %d", cfg.GitSync.MaxRepoBytes, wantMaxRepoBytes)
	}
	if cfg.GitSync.MaxFileBytes != wantMaxFileBytes {
		t.Errorf("gitsync.max_file_bytes = %d, want %d", cfg.GitSync.MaxFileBytes, wantMaxFileBytes)
	}
	if cfg.GitSync.MaxFiles != wantMaxFiles {
		t.Errorf("gitsync.max_files = %d, want %d", cfg.GitSync.MaxFiles, wantMaxFiles)
	}
	if cfg.GitSync.FetchTimeout != wantFetchTimeout {
		t.Errorf("gitsync.fetch_timeout = %s, want %s", cfg.GitSync.FetchTimeout, wantFetchTimeout)
	}
}

// TestGitSyncLimitOverrides confirms every new gitsync limit key can be
// overridden via SHEPHERD_GITSYNC_* environment variables.
func TestGitSyncLimitOverrides(t *testing.T) {
	t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
	t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("SHEPHERD_GITSYNC_MAX_REPO_BYTES", "1000")
	t.Setenv("SHEPHERD_GITSYNC_MAX_FILE_BYTES", "2000")
	t.Setenv("SHEPHERD_GITSYNC_MAX_FILES", "7")
	t.Setenv("SHEPHERD_GITSYNC_FETCH_TIMEOUT", "5s")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitSync.MaxRepoBytes != 1000 {
		t.Errorf("gitsync.max_repo_bytes = %d, want 1000", cfg.GitSync.MaxRepoBytes)
	}
	if cfg.GitSync.MaxFileBytes != 2000 {
		t.Errorf("gitsync.max_file_bytes = %d, want 2000", cfg.GitSync.MaxFileBytes)
	}
	if cfg.GitSync.MaxFiles != 7 {
		t.Errorf("gitsync.max_files = %d, want 7", cfg.GitSync.MaxFiles)
	}
	if cfg.GitSync.FetchTimeout != 5*time.Second {
		t.Errorf("gitsync.fetch_timeout = %s, want 5s", cfg.GitSync.FetchTimeout)
	}
}

// TestRouteApplyConfig pins the tenant-route reconciler's config: off by
// default, loadable from the env vars the chart sets, and refused when enabled
// without the namespace and receiver backend it cannot guess.
func TestRouteApplyConfig(t *testing.T) {
	base := func(t *testing.T) {
		t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
		t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	}

	t.Run("off by default", func(t *testing.T) {
		base(t)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		a := cfg.Gateway.Routes.Apply
		if a.Enabled {
			t.Error("gateway.routes.apply.enabled defaults to true, want false")
		}
		if a.BackendPort != 4318 || a.Interval != 60*time.Second || a.AttachTimeout != 60*time.Second {
			t.Errorf("defaults = port %d, interval %s, attach_timeout %s; want 4318, 60s, 60s",
				a.BackendPort, a.Interval, a.AttachTimeout)
		}
	})

	t.Run("enabled from env", func(t *testing.T) {
		base(t)
		t.Setenv("SHEPHERD_GATEWAY_ROUTES_APPLY_ENABLED", "true")
		t.Setenv("SHEPHERD_GATEWAY_ROUTES_APPLY_NAMESPACE", "shepherd")
		t.Setenv("SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_SERVICE", "shepherd-receiver")
		t.Setenv("SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_PORT", "4319")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		a := cfg.Gateway.Routes.Apply
		if !a.Enabled || a.Namespace != "shepherd" || a.BackendService != "shepherd-receiver" || a.BackendPort != 4319 {
			t.Errorf("loaded %+v", a)
		}
	})

	t.Run("enabled without namespace or backend is refused", func(t *testing.T) {
		base(t)
		t.Setenv("SHEPHERD_GATEWAY_ROUTES_APPLY_ENABLED", "true")
		_, err := Load("")
		if err == nil {
			t.Fatal("Load succeeded, want a configuration error")
		}
		for _, want := range []string{"gateway.routes.apply.namespace", "gateway.routes.apply.backend_service"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %s", err, want)
			}
		}
	})
}
