# Git provider design — standard git first, provider auth as strategies

> Status: **proposed, not implemented** (2026-08-19, revised same day).
> Requirement: GitOps must work against **any standard git server**, as broadly as possible.
> Provider-specific work is confined to **authentication**: Azure DevOps needs Entra service
> principals, GitHub needs GitHub Apps; everything else is ordinary git credentials.
> Testing uses a real **Gitea** instance rather than a hand-written mock.
> Tracked in `docs/project-status.md` as F9. Supersedes `docs/spec.md` §10.

## 1. Why this changes

Spec §10 was written ADO-first and the implementation followed it literally: **Shepherd never
speaks git at all.** `internal/ado/client.go` calls the **Azure DevOps REST API** —
`GetLatestCommit`, `ListFiles`, `DownloadFile` — authenticated with an Entra
client-credentials token scoped to the ADO resource GUID. Consequences:

- No other git host works. Not Gitea, GitHub, GitLab, Bitbucket, or a plain git server.
- The tests mock ADO's REST shapes (`e2e/mockmsft`), so they prove Shepherd talks to *the
  mock*, not that it talks to *git*. This is how the silent update-drop bug survived until
  `0194541`.
- Provider concepts leak into the database (`ado_credentials`, `repo_links.project`) and the
  wire contract (`shepherd.mgmt.v1.AdoCredential`).

The requirement inverts this: **standard git is the baseline; each provider contributes only
an authentication strategy.**

## 2. Current ADO coupling (what has to move)

| Layer | ADO-specific today |
|---|---|
| Transport | `internal/ado/client.go` — ADO Git REST API. No git library in `go.mod` |
| Auth | Entra client-credentials → token for resource `499b84ac-1321-427f-aa17-267ca6975798` |
| Reconciler | `internal/gitsync/reconciler.go:94` constructs `ado.New(...)` and calls the three REST methods |
| Schema | `ado_credentials(ado_org_url, entra_tenant_id, client_id, client_secret_enc)`; `repo_links(project, repository, …)` |
| Wire contract | `shepherd.mgmt.v1.AdoCredential`; `GitOpsService.{List,Create,Delete}Credential` |
| UI | `GitPage`, worded around ADO credentials |
| Tests | `e2e/mockmsft` mocks ADO REST + `/__fixture` injection; e2e scenario 5 drives it |

## 3. Target architecture

### 3.1 Transport: real git, in-process

New package `internal/gitrepo` speaking the **git wire protocol** with
**`github.com/go-git/go-git/v6`** — pure Go, so the distroless image needs no `git` binary
(it currently carries only the Alloy binary).

> **Version note.** v6 is the current major and moved auth from a `CloneOptions.Auth` field to
> `ClientOptions` (`client.WithHTTPAuth`, `client.WithSSHAuth`). Any v5 example found online
> will not compile. Pin v6 and follow its `EXTENDING.md` for transports.

```go
package gitrepo

// Auth supplies per-request git credentials for one repo link.
// Token-exchange strategies (ado_sp, github_app) cache until shortly before expiry.
type Auth interface {
    // HTTP returns basic-auth credentials, or ok=false for SSH/anonymous strategies.
    HTTP(ctx context.Context) (username, password string, ok bool, err error)
    // SSH returns a signer for ssh:// remotes, or ok=false otherwise.
    SSH(ctx context.Context) (signer ssh.Signer, ok bool, err error)
}

type Repo struct {
    URL, Branch, Path string
    Auth   Auth
    TLS    TLSOptions   // per-credential CA bundle / skip-verify (§3.5)
    Limits Limits       // §3.6
}

// LatestCommit resolves the branch tip without fetching objects (ls-remote).
func (r Repo) LatestCommit(ctx context.Context) (string, error)

// Files shallow-clones the branch and returns every *.alloy file under Path.
func (r Repo) Files(ctx context.Context) ([]File, error)
```

- **Change detection** is `ls-remote` on the one branch ref — a single cheap round trip per
  poll, replacing `GetLatestCommit`. Unchanged tip ⇒ no fetch, exactly as today.
- **Fetching** is `Depth: 1`, `SingleBranch: true` into an in-memory filesystem. Config repos
  are small; nothing touches disk, so there is no clone cache to invalidate or secure.
- Everything downstream of `Files()` — stage-1 validation, pipeline create/update, revision,
  audit, cache-dirty — is unchanged. This is a transport swap, not a semantics change.

### 3.2 Authentication strategies

`credential.kind` selects the strategy. Every secret is AES-GCM encrypted at rest (§7.4).

| kind | Non-secret config | Secret(s) | Sent to git as | Covers |
|---|---|---|---|---|
| `none` | — | — | nothing | public repos |
| `basic` | `username` | password | HTTP basic | plain git over HTTPS |
| `pat` | `username` (often literal `oauth2`, `git`, `x-token-auth`) | token | HTTP basic | **Gitea, GitHub, GitLab, Bitbucket**, most hosts; GitLab/Gitea deploy tokens |
| `ssh` | `username` (default `git`), `known_hosts` | private key PEM, optional passphrase | SSH public-key | any host offering SSH; **deploy keys** |
| `ado_sp` | `tenant_id`, `client_id`, `ado_org_url` | client secret | Entra client-credentials token as basic **password** | Azure DevOps |
| `github_app` | `app_id`, `installation_id`, `api_base_url` | RSA private key PEM | installation token as basic password, username `x-access-token` | **GitHub, GitHub Enterprise Server** |

`pat` alone already covers most of the world; `ssh`, `ado_sp` and `github_app` exist because
some organisations cannot or will not issue long-lived PATs.

#### The two token-exchange strategies share one shape

Both mint a **short-lived** token from a long-lived secret, so they share a cache keyed by
credential id with refresh shortly before expiry:

- **`ado_sp`** — Entra client-credentials grant, scope
  `499b84ac-1321-427f-aa17-267ca6975798/.default`. This is all that survives of
  `internal/ado`; the three REST methods are deleted.
- **`github_app`** — sign an RS256 JWT with the app private key (`iss` = app id, ≤10 min
  lifetime), `POST {api_base}/app/installations/{installation_id}/access_tokens` with it,
  receive an installation token valid ~1 hour. `api_base` defaults to
  `https://api.github.com` and is set to `https://<host>/api/v3` for GitHub Enterprise Server.
  Nice-to-have for the UI: `GET /app/installations` with the same JWT lists installations so
  an admin can pick one instead of pasting an id.

GitHub Apps are worth the extra strategy: tokens are short-lived, scoped per installation,
attributed to the app rather than a person, and survive staff turnover — the usual reason a
GitHub shop refuses PAT-based integrations.

### 3.3 Data model

Migration `0006_git_providers` (additive-then-backfill; never edit a committed migration):

```sql
ALTER TABLE ado_credentials RENAME TO git_credentials;

ALTER TABLE git_credentials
  ADD COLUMN kind text NOT NULL DEFAULT 'ado_sp'
      CHECK (kind IN ('none','basic','pat','ssh','ado_sp','github_app')),
  ADD COLUMN username        text,
  ADD COLUMN secret2_enc     bytea,        -- optional 2nd secret (ssh key passphrase)
  ADD COLUMN provider_config jsonb NOT NULL DEFAULT '{}',  -- kind-specific NON-secret fields
  ADD COLUMN ssh_known_hosts text,
  ADD COLUMN ca_cert         text,         -- PEM bundle, §3.5
  ADD COLUMN tls_insecure_skip_verify boolean NOT NULL DEFAULT false,
  ALTER COLUMN ado_org_url     DROP NOT NULL,
  ALTER COLUMN entra_tenant_id DROP NOT NULL,
  ALTER COLUMN client_id       DROP NOT NULL;

-- client_secret_enc keeps its name and encryption; it is now "the one secret"
-- (password | PAT | client secret | GitHub App private key | SSH private key).

ALTER TABLE repo_links ADD COLUMN repo_url text;
UPDATE repo_links l SET repo_url =
  rtrim(c.ado_org_url,'/') || '/' || l.project || '/_git/' || l.repository
  FROM git_credentials c WHERE c.id = l.credential_id;
ALTER TABLE repo_links ALTER COLUMN repo_url SET NOT NULL;
ALTER TABLE repo_links DROP COLUMN project, DROP COLUMN repository;
```

**Why `provider_config` is JSONB.** Kind-specific *non-secret* fields differ per provider and
the set will keep growing (AWS CodeCommit and GCP Source Repositories would each add their
own). A JSONB blob avoids a migration per provider. Cross-cutting fields that apply to *every*
HTTPS kind — `ca_cert`, `tls_insecure_skip_verify` — stay as real columns because they are not
provider-specific. The ADO trio stays as columns for the backfill's sake; new providers use
`provider_config` only.

`repo_url` is an ordinary clone URL — `https://gitea.internal/team/configs.git`,
`git@gitea.internal:team/configs.git`, `https://dev.azure.com/org/project/_git/repo`. `branch`
and `path` are unchanged. Existing ADO rows keep working: the backfill derives their clone URL
and `kind` defaults to `ado_sp`.

### 3.4 Wire contract

`shepherd.mgmt.v1.AdoCredential` → `GitCredential` carrying `kind`, `username`, and a
`provider_config` map; secrets stay write-only. `CreateRepoLinkRequest` takes `repo_url`
instead of `project` + `repository`.

**This breaks the contract shipped in `d401cc4`.** It is a clean rename rather than a
deprecation shim because nothing consumes it in production yet — the platform fleet has no
`remotecfg` blocks at all (`docs/platform-monitoring-architecture.md`). **Re-verify that
assumption before implementing**; if an integration has appeared, add `GitCredential`
alongside and deprecate instead.

Also implements the endpoint still missing from ledger F4:
`POST /git-credentials/{id}/test` → resolve credentials and run `ls-remote`, returning
reachable / unreachable with the underlying git error (and, for token strategies, whether the
token exchange itself succeeded — the two failure modes need distinguishing).

### 3.5 TLS, private CAs, and proxies

Internal Gitea and GitLab instances routinely present a private CA, so this is required, not
optional.

- **Per-credential `ca_cert`** (PEM bundle) is appended to a clone of the system pool.
- **`tls_insecure_skip_verify`** exists as an explicit, per-credential, default-off escape
  hatch. The UI must label it as unsafe, and enabling it should be an audited change.
- **Cluster-wide CAs** can also be mounted into the image and trusted via the system pool —
  document that as the preferred route when every repo shares one CA.
- **Proxy support** honours the standard `HTTPS_PROXY` / `NO_PROXY` environment, which covers
  the common corporate egress case without new config.

> **Implementation constraint (verified against go-git v6 docs).** The documented way to
> customise TLS is `transport.Register("https", githttp.NewTransport(&githttp.TransportOptions{
> Client: customClient}))` — and that registration is **global**, which cannot express
> per-credential CAs. `ClientOptions` documents auth options only. So `internal/gitrepo` must
> build a transport **per credential** and drive it through go-git's plumbing API rather than
> the `PlainClone` porcelain. Confirm at implementation time whether v6 exposes a
> per-call transport option; if it does, prefer it. Do **not** paper over this with a global
> registration — one repo's skip-verify would silently apply to every other repo.

go-git's default redirect policy already blocks HTTPS→HTTP downgrades and non-initial
redirects; keep the default rather than relaxing it.

### 3.6 Resource limits

An in-memory clone of a mis-pointed URL is an OOM risk, so limits are enforced, configurable
under `gitsync`:

| Limit | Default | Behaviour on breach |
|---|---|---|
| `max_repo_bytes` | 50 MiB | abort the fetch, mark `sync_status=error` naming the limit |
| `max_file_bytes` | 1 MiB | skip the file, record a diagnostic |
| `max_files` | 500 | abort with a clear error |
| `fetch_timeout` | 60s | abort |

Enforced by a counting reader in the per-credential transport, so the limit applies to bytes
actually transferred, not to a post-hoc size check. A repo that legitimately outgrows memory
is the trigger to switch the billy backend to a temp directory — the `Repo` interface does not
change.

## 4. Testing with Gitea

Replace the ADO REST mock with a **real git server** so the tests exercise real git.

- **Compose service** (dev and e2e): `gitea/gitea:1-rootless`, SQLite backend, install lock
  set, admin user and access token created at first boot by an init step.
- **Fixtures**: a seed step creates a repo and pushes `.alloy` files — real commits, real refs.
  Mutating a fixture means pushing a commit, which is what production does. This replaces
  `mockmsft`'s `/__fixture` injection.
- **`mockmsft` keeps only its Entra/Graph role**: group resolution, plus the token endpoints
  the two exchange strategies need.
- **Auth coverage without Azure or GitHub:**
  - `pat` and `basic` — directly against Gitea.
  - `ssh` — against Gitea's SSH port with a generated deploy key; also covers `known_hosts`
    verification, including the negative case (wrong host key ⇒ refuse to sync).
  - `ado_sp` — mock Entra token endpoint issues a token; Gitea accepts it as the basic-auth
    password. Proves acquisition + plumbing; only ADO's own server is unexercised, which no
    CI can reach anyway.
  - `github_app` — mock the two GitHub App endpoints (`/app/installations/{id}/access_tokens`)
    in `mockmsft`; assert the JWT is RS256, correctly signed by the configured key, and within
    its lifetime, then use the returned token against Gitea. The JWT assertion is the part
    worth testing — it is where GitHub App integrations usually break.
- **TLS**: give Gitea a self-signed cert in one e2e variant to exercise `ca_cert` (trusted)
  and the negative case (untrusted ⇒ sync error, not a silent success).

### E2E scenario 5, rewritten

1. Seed Gitea with a valid `.alloy` file → `Eventually` a `source=git` pipeline exists and
   Alloy applies the enlarged merge.
2. **Push a second commit** changing the file → the pipeline updates and a revision is created.
   *(The path that was silently broken until `0194541`, finally testable end to end.)*
3. Push an invalid file → `sync_status=error`, last good config still served, hash unchanged.
4. Fix the file → recovers.
5. Repeat step 1 per auth kind (`pat`, `ssh`, `ado_sp`, `github_app`) as a table-driven case.

## 5. Migration and rollout

1. `internal/gitrepo` + the `pat`/`basic`/`none` strategies, unit-tested against a Gitea
   testcontainer. No reconciler changes yet.
2. `ssh`, then the two token-exchange strategies (`ado_sp` reusing the existing Entra code,
   `github_app` new) behind the same `Auth` interface.
3. Migration `0006`, sqlc regeneration, store tests over the backfill.
4. Reconciler swaps `ado.Client` for `gitrepo.Repo`; `internal/ado` reduced to a token provider.
5. Proto/service/UI rename; credential `test` endpoint; the `GitPage` CRUD that ledger B2 says
   is missing entirely.
6. Gitea in dev + e2e compose; scenario 5 rewritten; `mockmsft` ADO routes deleted.

Steps 1–3 land safely on their own; step 4 is the cutover.

## 6. What does not change

The validation gate, pipeline/revision/audit semantics, serve-cache invalidation, RBAC,
`source='git'` pipelines being read-only in the UI, and "one repo link targets one logical
collector" all stay exactly as they are.

## 7. Decisions taken

Resolved 2026-08-19 in favour of breadth:

- **SSH deploy keys — in scope** (`ssh` kind). Host keys are verified against a per-credential
  `known_hosts`; there is deliberately **no** accept-any-host-key mode, because an unverified
  SSH host key is an undetectable MITM, unlike the TLS case where the operator at least has to
  tick a box labelled unsafe.
- **Private CAs — in scope**, per-credential `ca_cert` plus system-pool trust, with an explicit
  default-off `tls_insecure_skip_verify` escape hatch (§3.5).
- **Clone size ceiling — in scope**, configurable, defaults in §3.6.
- **GitHub Apps — in scope** (`github_app` kind), including GitHub Enterprise Server via
  `api_base_url` (§3.2).

## 8. Still open

- **Additional providers.** AWS CodeCommit (SigV4) and GCP Source Repositories would each need
  a strategy. Both are reachable today via their HTTPS git credentials using `basic`, so
  neither is urgent — revisit only on a concrete request.
- **Credential rotation.** GitHub App keys and ADO client secrets expire. Shepherd currently
  surfaces failures only as `sync_status=error` on each repo link. A pre-expiry warning would
  be better, but needs a decision on where it surfaces (audit? a health surface? the collector
  status model?).
- **Webhooks** remain a spec §19 non-goal — polling only.
