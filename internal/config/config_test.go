package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
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
