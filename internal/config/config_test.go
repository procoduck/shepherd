package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
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
