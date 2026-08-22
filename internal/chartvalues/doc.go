// Package chartvalues generates a Helm values LAYER for Grafana's
// k8s-monitoring chart (docs/gateway-tier-plan.md W9): the guided-form
// counterpart to a wizard, except its Commit() target is not a pipeline
// Shepherd serves — it is a deployment artifact meant to run in the
// operator's own cluster, delivered via the existing GitOps machinery (D4)
// or a plain download.
//
// # What this package deliberately does not do
//
// docs/gateway-tier-plan.md §5 draws the boundary explicitly: this package
// "must not attempt full coverage of the chart's values surface — that is
// maintaining a mirror of someone else's chart, unbounded and permanently
// rotting." k8s-monitoring's values.schema.json describes dozens of features
// (cost metrics, Windows event logs, cluster events, auto-instrumentation,
// profiling, ...); Render touches exactly one corner of it — the four
// collectors' remoteConfig blocks that make a spoke cluster's Alloy fleet
// register with THIS Shepherd — and nothing else. The output is meant to sit
// alongside the operator's own `-f` values file, not replace it: Render never
// sets sizing, presets, resources, or any feature toggle. New keys are added
// here only when a user asks for them, never speculatively (§5).
//
// # Why remoteConfig and nothing else
//
// docs/spec.md's own deployment context already documents the shape a real
// k8s-monitoring v4 install expects for this: four named collectors
// (alloy-metrics, alloy-logs, alloy-singleton, alloy-receiver), each with a
// collectors.<name>.remoteConfig block naming Shepherd's URL, a poll
// frequency, HTTP Basic auth, and extraAttributes carrying `cluster` and
// `role` — the exact tuple internal/signals' role policy and
// docs/spec.md §"Resolution" key a logical collector on. Render emits
// precisely that block, per collector role the operator selects, and no
// more. The four collector names and the remoteConfig field names are not
// invented: RenderKeys are checked at test time against the vendored
// values.schema.json (testdata/values.schema.json, fetched verbatim from the
// pinned chart release — see version.go and schema.go) rather than typed
// from memory, per the W9 brief's explicit instruction.
//
// # Credentials are never in the file
//
// Following internal/beacon's already-established convention (the same
// agent-token pair, DefaultTokenIDEnv/DefaultTokenSecretEnv), Render sets
// auth.usernameFrom/auth.passwordFrom to sys.env(...) expressions rather
// than auth.username/auth.password literals. The k8s-monitoring chart
// supports both (see collectors.remoteConfig.auth in the vendored schema);
// this package only ever emits the *From shape, so a rendered values file
// can be committed to a GitOps repo or handed to an operator without ever
// carrying a secret, matching this repo's "secrets are never logged, never
// returned by any API" standard one level up — the *generator* itself never
// holds a value that would need protecting.
package chartvalues
