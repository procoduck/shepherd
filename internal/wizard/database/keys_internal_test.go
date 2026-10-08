package database

import (
	"testing"

	"shepherd/internal/wizard"
)

// The redis key constant exists only to keep CodeQL's name-based
// sensitive-data heuristic off a Secret key NAME (see defaultDataKey); it
// must stay the key destination basic auth uses.
func TestDefaultDataKeys(t *testing.T) {
	if keyRedisAuth != wizard.SecretKeyPassword {
		t.Fatalf("keyRedisAuth = %q, want wizard.SecretKeyPassword %q", keyRedisAuth, wizard.SecretKeyPassword)
	}
	for engine, want := range map[string]string{"postgres": "dsn", "mysql": "dsn", "redis": "password"} {
		if got := defaultDataKey(engine); got != want {
			t.Errorf("defaultDataKey(%q) = %q, want %q", engine, got, want)
		}
	}
}
