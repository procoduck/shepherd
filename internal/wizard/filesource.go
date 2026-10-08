package wizard

import "fmt"

// RenderFileSource returns a local.file_match + loki.source.file pair, both
// labelled name, that tails every file matching glob with a `job` label and
// forwards the lines to forwardTo (a receiver expression, e.g.
// "loki.write.logs.receiver").
//
// The glob goes to local.file_match, never straight to loki.source.file:
// loki.source.file treats each target's __path__ as one literal file and
// does not expand patterns. Handed `/var/log/app/*.log` it stats that exact
// name, logs "failed to create source, skipping … no such file or
// directory" and tails nothing — while the component reports healthy and the
// collector reports APPLIED, which is how it shipped (2026-10-08
// walkthrough). local.file_match expands the glob, re-scans on its sync
// period (so files created later are picked up) and exports one target per
// matching file, which loki.source.file then tails.
func RenderFileSource(name, glob, job, forwardTo string) string {
	return fmt.Sprintf(`local.file_match "%[1]s" {
  path_targets = [
    {__path__ = "%[2]s", job = "%[3]s"},
  ]
}

loki.source.file "%[1]s" {
  targets    = local.file_match.%[1]s.targets
  forward_to = [%[4]s]
}
`, name, glob, job, forwardTo)
}
