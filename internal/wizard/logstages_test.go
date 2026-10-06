package wizard

import (
	"strings"
	"testing"
)

// TestLogParseStages pins the shape of each format's stages. Whether Alloy
// accepts them is proven where it is consumed — the wizard goldens'
// TestGoldensLoadInRealAlloy runs every one through a real `alloy run`.
func TestLogParseStages(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []string // substrings the stages must contain; nil = no stages
	}{
		{"", nil},
		{LogFormatRaw, nil},
		{"logfmt", []string{`stage.logfmt {`, `mapping = { level = "" }`, `stage.labels {`}},
		{"json", []string{`stage.json {`, `expressions = { level = "" }`, `stage.labels {`}},
		{"cri", []string{"stage.cri {}"}},
		{"docker", []string{"stage.docker {}"}},
	} {
		got, err := LogParseStages(tc.format)
		if err != nil {
			t.Fatalf("LogParseStages(%q): %v", tc.format, err)
		}
		if tc.want == nil && got != "" {
			t.Errorf("LogParseStages(%q) = %q, want no stages", tc.format, got)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("LogParseStages(%q) = %q, want it to contain %q", tc.format, got, w)
			}
		}
		// The defect this exists to prevent: a parse stage with nothing in it.
		if strings.Contains(got, "stage.logfmt {}") || strings.Contains(got, "stage.json {}") {
			t.Errorf("LogParseStages(%q) renders an empty parse stage, which a running Alloy refuses: %q", tc.format, got)
		}
	}
	if _, err := LogParseStages("yaml"); err == nil {
		t.Error(`LogParseStages("yaml") succeeded, want an error rather than a stage.yaml block`)
	}
}
