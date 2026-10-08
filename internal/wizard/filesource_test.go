package wizard

import (
	"strings"
	"testing"

	"shepherd/internal/validate"
)

// TestRenderFileSourceQuotes pins that the glob and the job label are
// rendered through Quote: a quote or backslash in either must stay inside
// its string literal rather than end it and inject syntax. Ordinary values
// render exactly as before (the wizard goldens are unchanged).
func TestRenderFileSourceQuotes(t *testing.T) {
	got := RenderFileSource("app_logs", `/var/log/a"b\*.log`, `my"job`, "loki.write.logs.receiver")
	for _, want := range []string{
		`{__path__ = "/var/log/a\"b\\*.log", job = "my\"job"},`,
		`targets    = local.file_match.app_logs.targets`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderFileSource output lacks %q:\n%s", want, got)
		}
	}
	if r := validate.Stage1(got); !r.Valid {
		t.Errorf("RenderFileSource output does not parse: %v\n%s", r.Diagnostics, got)
	}

	plain := RenderFileSource("self", "/var/log/alloy/*.log", "alloy-self", "loki.write.logs.receiver")
	if want := `{__path__ = "/var/log/alloy/*.log", job = "alloy-self"},`; !strings.Contains(plain, want) {
		t.Errorf("ordinary values must render unchanged, want %q in:\n%s", want, plain)
	}
}
