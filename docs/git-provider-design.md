# Git provider design — standard git first, ADO service principals as one auth mode

> Status: **proposed, not implemented** (2026-08-19). Requirement from the product owner:
> GitOps must work against **any standard git server**; Azure DevOps' only special
> requirement is **Entra service-principal authentication**. Testing uses a real **Gitea**
> instance rather than a hand-written mock.
> Tracked in `docs/project-status.md` as F9. Supersedes the ADO-only assumption in
> `docs/spec.md` §10.

## 1. Why this changes

Spec §10 was written ADO-first, and the implementation followed it literally: Shepherd never
speaks git at all. `internal/ado/client.go` calls the **Azure DevOps REST API** —
`GetLatestCommit`, `ListFiles`, `DownloadFile` — authenticated with an Entra
client-credentials token scoped to the ADO resource GUID. That means:

- No other git host works. Not Gitea, GitHub, GitLab, Bitbucket, or a plain git server.
- The test story is a hand-written mock (`e2e/mockmsft`) that reimplements ADO's REST shapes.
  It proves Shepherd talks to *the mock*, not that it talks to *git*.
- Provider concepts leak all the way to the database and the wire contract
  (`ado_credentials`, `ado_org_url`, `entra_tenant_id`, `repo_links.project`).

The requirement inverts this: **standard git is the baseline; ADO is one authentication
strategy on top of it.**

## 2. Current ADO coupling (what has to move)

| Layer | ADO-specific today |
|---|---|
| Transport | `internal/ado/client.go` — ADO Git REST API; no git protocol anywhere. No git library in `go.mod` |
| Auth | Entra client-credentials → token for resource `499b84ac-1321-427f-aa17-267ca6975798` |
| Reconciler | `internal/gitsync/reconciler.go:94` constructs `ado.New(...)` directly and calls the three REST methods |
| Schema | `ado_credentials(ado_org_url, entra_tenant_id, client_id, client_secret_enc)`; `repo_links(project, repository, branch, path)` — `project` is an ADO concept |
| Wire contract | `shepherd.mgmt.v1.AdoCredential` with `ado_org_url` / `entra_tenant_id` / `client_id`; `GitOpsService.{List,Create,Delete}Credential` |
| UI | `GitPage` labelled around ADO credentials |
| Tests | `e2e/mockmsft` mocks ADO REST + a `/__fixture` injection endpoint; e2e scenario 5 drives it |

## 3. Target architecture

### 3.1 Transport: real git, in-process

Introduce `internal/gitrepo` speaking the **git wire protocol over HTTPS** with
[`github.com/go-git/go-git/v5`] — pure Go, so it works in the distroless image with no `git`
binary added (the image currently carries only the Alloy binary).

```go
package gitrepo

// Auth supplies per-request git credentials. Implementations: PAT, basic, ADO service
// principal. Tokens are fetched lazily and cached until shortly before expiry.
type Auth interface {
    Credentials(ctx context.Context) (username, password string, err error)
}

type Repo struct{ URL, Branch, Path string; Auth Auth }

// LatestCommit resolves the branch tip without fetching objects (git ls-remote).
func (r Repo) LatestCommit(ctx context.Context) (string, error)

// Files shallow-clones the branch into memory and returns every *.alloy file under Path.
func (r Repo) Files(ctx context.Context) ([]File, error)
```

- **Change detection** is `ls-remote` on the single branch ref — one cheap round trip per
  poll, replacing `GetLatestCommit`. Unchanged tip ⇒ no fetch, exactly as today.
- **Fetching** is a `depth=1`, single-branch clone into go-git's in-memory filesystem.
  Config repos are small; nothing touches disk, and there is no clone cache to invalidate or
  secure. (If a repo ever outgrows memory, swap the billy backend for a temp dir — the
  interface does not change.)
- Everything downstream of `Files()` — stage-1 validation, pipeline create/update, revision,
  audit, cache dirty — is unchanged. This is a transport swap, not a semantics change.

### 3.2 Auth strategies

`credential.kind` selects the strategy; all secrets stay AES-GCM encrypted at rest (§7.4):

| kind | Fields | Credentials sent | Works with |
|---|---|---|---|
| `pat` | `username` (often a literal like `oauth2`/`git`), `secret` = token | basic auth | Gitea, GitHub, GitLab, Bitbucket, most hosts |
| `basic` | `username`, `secret` = password | basic auth | plain git over HTTPS |
| `ado_sp` | `tenant_id`, `client_id`, `secret` = client secret | Entra client-credentials token for the ADO resource GUID, sent as the basic-auth **password** (username ignored by ADO) | Azure DevOps |
| `none` | — | none | public repos |

**`ado_sp` is the only provider-specific code that survives**, and it shrinks to a token
provider: `internal/ado` keeps the Entra client-credentials exchange and the resource GUID,
loses the three REST methods. SSH deploy keys are deliberately out of scope for now
(see §7).

### 3.3 Data model

Migration `0006_git_providers` (additive-then-backfill; never edit a committed migration):

```sql
-- rename + generalize
ALTER TABLE ado_credentials RENAME TO git_credentials;
ALTER TABLE git_credentials
  ADD COLUMN kind text NOT NULL DEFAULT 'ado_sp'
      CHECK (kind IN ('pat','basic','ado_sp','none')),
  ADD COLUMN username text,
  ALTER COLUMN ado_org_url     DROP NOT NULL,   -- ado_sp only
  ALTER COLUMN entra_tenant_id DROP NOT NULL,   -- ado_sp only
  ALTER COLUMN client_id       DROP NOT NULL;   -- ado_sp only
-- secret column keeps its name/encryption; it now holds a PAT/password/client-secret

ALTER TABLE repo_links ADD COLUMN repo_url text;
UPDATE repo_links l SET repo_url =
  rtrim(c.ado_org_url,'/') || '/' || l.project || '/_git/' || l.repository
  FROM git_credentials c WHERE c.id = l.credential_id;
ALTER TABLE repo_links ALTER COLUMN repo_url SET NOT NULL;
ALTER TABLE repo_links DROP COLUMN project, DROP COLUMN repository;
```

`repo_url` is the ordinary clone URL — `https://gitea.example.com/team/configs.git`,
`https://dev.azure.com/org/project/_git/repo`, anything git understands. `branch` and `path`
are unchanged. Existing ADO rows keep working: the backfill derives their clone URL and
`kind` defaults to `ado_sp`.

### 3.4 Wire contract

`shepherd.mgmt.v1.AdoCredential` → `GitCredential` with `kind`, `username`, `repo-agnostic`
fields, and the ADO trio present only for `kind = ado_sp`. `GitOpsService` methods keep their
names; `CreateRepoLinkRequest` takes `repo_url` instead of `project` + `repository`.

**This breaks the wire contract shipped in `d401cc4`.** It is a clean rename rather than a
deprecation shim because nothing consumes it in production yet — the platform fleet has no
`remotecfg` blocks at all (`docs/platform-monitoring-architecture.md`), so there are no
external integrations to keep compatible. That reasoning should be re-checked before
implementing; if any integration has appeared by then, add `git_credential` alongside and
deprecate rather than rename.

Also folds in the still-missing endpoint from ledger F4:
`POST /git-credentials/{id}/test` becomes a real capability — resolve credentials and run
`ls-remote` against the repo, returning reachable/unreachable with the git error.

### 3.5 UI

`GitPage` gains the CRUD it currently lacks (ledger B2) and becomes provider-neutral: a
credential form that switches fields on `kind`, and a repo-link form taking a clone URL,
branch, path, and target collector. "ADO credential" wording disappears except inside the
`ado_sp` branch.

## 4. Testing with Gitea

Replace the ADO REST mock with a **real git server**, so the tests exercise real git.

- **Compose service** (dev and e2e): `gitea/gitea:1-rootless`, SQLite backend, install lock
  set, an admin user and an access token created at first boot by an init step.
- **Fixtures**: a seed step creates a repo and pushes `.alloy` files — real commits, real
  refs. Mutating a fixture means pushing a commit, which is exactly what production does.
  This replaces `mockmsft`'s `/__fixture` injection.
- **`mockmsft` keeps only its Entra/Graph role**: group resolution, plus the
  client-credentials token endpoint used to test the `ado_sp` path.
- **`ado_sp` coverage without Azure**: point an `ado_sp` credential at the mock Entra token
  endpoint and let the resulting token authenticate against Gitea (which accepts any bearer
  as basic-auth password for a valid user). That proves the token-acquisition and
  credential-plumbing path end to end; only ADO's own server is unexercised, which no CI
  can do anyway.

### E2E scenario 5, rewritten

1. Seed Gitea with a valid `.alloy` file → `Eventually` a `source=git` pipeline exists and
   Alloy applies the enlarged merge.
2. **Push a second commit** changing the file → the pipeline updates and a revision is
   created. *(This is the path that was silently broken until `0194541`; with a real git
   server it is finally testable end to end.)*
3. Push an invalid file → `sync_status=error`, last good config still served, hash unchanged.
4. Fix the file → recovers.
5. Repeat 1 against a `pat` credential and an `ado_sp` credential to cover both auth modes.

## 5. Migration and rollout

1. `internal/gitrepo` + auth strategies, unit-tested against a Gitea container
   (testcontainers) — no reconciler changes yet.
2. Migration `0006`, sqlc regeneration, store-level tests over the backfill.
3. Reconciler swaps `ado.Client` for `gitrepo.Repo`; `internal/ado` reduced to the token
   provider.
4. Proto/service/UI rename; `test` endpoint implemented.
5. Gitea added to dev + e2e compose; e2e scenario 5 rewritten; `mockmsft` ADO routes deleted.

Steps 1–2 are safely landable on their own; step 3 is the cutover.

## 6. What does not change

The validation gate, pipeline/revision/audit semantics, serve-cache invalidation, RBAC,
`source='git'` pipelines being read-only in the UI, and the "one repo link targets one
logical collector" model all stay exactly as they are.

## 7. Open decisions

- **SSH deploy keys.** Common for read-only GitOps, and go-git supports them. Deferred to
  keep the first cut to HTTPS; the `Auth` interface already accommodates a future
  `ssh` kind, but the schema would need a key field.
- **Self-signed / private CAs.** Internal Gitea or GitLab instances often present a private
  CA. Needs a decision: trust the system pool only (and document adding the CA to the image),
  or a per-credential `ca_cert` field.
- **Repo size ceiling.** In-memory clone is fine for config repos; decide whether to cap it
  (refuse a clone above N MB with a clear error) rather than risk an OOM on a mis-pointed URL.
- **Webhooks** stay out of scope (spec §19 non-goal) — polling only.
