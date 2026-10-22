package agentapi

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A refused collector retries every poll: the first refusal per (instance id,
// reason) per window is logged at Warn, the rest are only counted, and the
// remembered keys stay bounded.
func TestRefusalLogRateLimits(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := time.Unix(0, 0)
	l := newRefusalLog(5*time.Minute, 3)
	l.now = func() time.Time { return now }

	steps := []struct {
		name       string
		advance    time.Duration
		instanceID string
		reason     string
		wantLogged bool
	}{
		{"first refusal logs", 0, "host-1", "claim_denied", true},
		{"repeat inside the window is counted only", time.Minute, "host-1", "claim_denied", false},
		{"another reason for the same id logs", 0, "host-1", "role_denied", true},
		{"another id logs", 0, "host-2", "claim_denied", true},
		{"after the window it logs again", 5 * time.Minute, "host-1", "claim_denied", true},
	}
	for _, s := range steps {
		now = now.Add(s.advance)
		if got := l.record(logger, "GetConfig", s.reason, s.instanceID); got != s.wantLogged {
			t.Fatalf("%s: logged = %v, want %v", s.name, got, s.wantLogged)
		}
	}
	if n := strings.Count(buf.String(), "collector request refused"); n != 4 {
		t.Fatalf("Warn lines = %d, want 4", n)
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("refusals must log at Warn: %s", buf.String())
	}

	for i := range 100 {
		l.record(logger, "GetConfig", "claim_denied", "cycled-"+string(rune('a'+i%26))+time.Duration(i).String())
		if len(l.last) > 3 {
			t.Fatalf("remembered keys = %d, want at most 3", len(l.last))
		}
	}
}
