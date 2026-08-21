# V4-19 SQL Deduplication Proof

## WSR Annotations Addressed

| Annotation | Source | Finding | Action |
|---|---|---|---|
| Part 0.1 `[?]` | `migrations/` root | Exact duplicate of `internal/migrations/sql/` | DELETED |
| Part 0.1 `[?]` | `shepherd` binary at root | Stray build artifact | Added `/shepherd` to `.gitignore` |
| Part 1.5 | `MarkServeCacheDirty` | "Orphaned" single-collector query | KEPT — used in agentapi tests; KEEP comment added |
| Part 1.5 | `DeleteExpiredSessions` | Was orphaned | RESOLVED by V4-2 (sweeper now calls it) |

## Intent-Duplicate Query Analysis

| Query pair | Verdict |
|---|---|
| `ListAllClusters` + unclaimed handler conditional | Not duplicates — different result sets for different views |
| `UpsertServeCache` / `UpsertServeCacheConditional` | Intentional — different dirty-flag semantics; both have callers *(plain `UpsertServeCache` since deleted as dead code, 2026-08-21)* |

## Raw SQL Inventory

| File | Reason | Status |
|---|---|---|
| `internal/agentapi/sweeper.go` (pg_class) | System catalog, not sqlc-able | RAW-SQL-OK ✓ |
| `internal/agentapi/sweeper.go` (DELETE sessions) | Needs RowsAffected count | RAW-SQL-OK ✓ |
| `internal/mgmtapi/admin.go` (cross-table count) | Two-column count, no sqlc equivalent | RAW-SQL-OK ✓ |
| `internal/mgmtapi/admin.go` (cluster dirty mark) | Cluster-join shape, no sqlc query | RAW-SQL-OK ✓ |
| `internal/mgmtapi/orgs.go` (JSONB containment) | wizard_state @>, no sqlc equivalent | RAW-SQL-OK ✓ |
| `internal/cli/dev.go` | Dev seed — exempt | Dev-exempt ✓ |
| `internal/testutil/postgres.go` | Test infra — exempt | Test-exempt ✓ |

## Before/After

- Root `migrations/` directory: DELETED (8 SQL files, 0 callers)
- `internal/store/queries/serve_cache.sql`: MarkServeCacheDirty annotated with KEEP comment
- All production raw SQL outside `internal/store/`: marked RAW-SQL-OK
- CI guard `check-raw-sql` added to `make lint`
