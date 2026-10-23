package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestGitSyncMaxFileBytesCap: gitsync.max_file_bytes may not exceed the
// pipeline contents cap (MaxPipelineContentsBytes), or a repository could
// sync a file the API refuses to save and the tenant check refuses to read.
func TestGitSyncMaxFileBytesCap(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantErr bool
	}{{"", false}, {"1048576", false}, {"1048577", true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
			t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if tc.value != "" {
				t.Setenv("SHEPHERD_GITSYNC_MAX_FILE_BYTES", tc.value)
			}
			_, err := Load("")
			if tc.wantErr != (err != nil) || (err != nil && !strings.Contains(err.Error(), "gitsync.max_file_bytes")) {
				t.Fatalf("Load = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
