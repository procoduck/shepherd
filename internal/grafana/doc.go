// Package grafana implements D7 (docs/gateway-tier-plan.md §2, §5): an
// optional, org-scoped Grafana service-account connection that lets
// Shepherd ask the question its story otherwise stops short of — "did the
// data actually arrive?" — by querying the destination directly
// (POST /api/ds/query), the same "assert the observable consequence"
// standard this repo already applies to `alloy validate` vs `alloy run`
// and to the gateway tier's route-attachment check
// (internal/gateway/apply.go), extended past the collector into
// production.
//
// # Scope, deliberately bounded (plan §5)
//
// This package is verification FIRST:
//
//   - Verify (verify.go) queries a destination and reports one of THREE
//     outcomes — data arrived, data did not arrive, or Shepherd could not
//     determine which. See OutcomeUnknown's doc comment for why collapsing
//     the third into the second is the one mistake this type exists to make
//     impossible.
//   - ExploreURL (explore.go) builds a deep link into Grafana Explore.
//     It needs no token at all — it is a browser-navigation URL, not an API
//     call — and exists regardless of whether a connection is configured.
//   - Client.ListDatasources (client.go) is the one piece of "destination
//     import": Grafana's own /api/datasources response never carries a
//     datasource's secrets (only booleans under secureJsonFields), so this
//     is bounded to endpoints and types, nothing credential-bearing.
//
// What this package explicitly does NOT do, per plan §5's must-not: manage
// dashboards or alert rules. That surface has no natural boundary and
// Grafana already answers it better.
//
// # Grafana absent means no outcome verification, never reduced function
//
// Every exported entry point that depends on a configured connection
// degrades to OutcomeUnknown (or, for ConnectionStore, a typed
// ErrNotConfigured) rather than returning an error a caller might
// propagate into failing an unrelated write. Verify and VerifyForOrg have
// NO error return at all — see verify.go's doc comment — which makes "a
// broken or unconfigured Grafana cannot fail a pipeline write" a property
// of the function signature, not a promise a caller has to remember to
// honor. connection_test.go's "Grafana absent (no configured connection)"
// specs are the red-run proof, run against a real database with no
// grafana_connections row at all.
//
// # Secret storage
//
// See 0011_grafana_connections.up.sql's comment for why this package
// follows git_credentials' encrypted-at-rest pattern (internal/crypto),
// not destinations' name/namespace Secret-reference pattern: Shepherd
// itself is the caller of /api/ds/query, so it must hold the plaintext
// token in memory at call time, the same way internal/gitsync holds a git
// credential's plaintext to call git. connection.go's ConnectionStore has
// no exported function that returns the plaintext token to a caller —
// only Client, which builds an *http.Request header from it and never logs
// or serializes it (see Client.String, which redacts).
package grafana
