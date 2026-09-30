# APPLIED means loaded, not polled (#115, B1 — and F1)

**Claim.** A collector whose agent rejected the config it was served shows **FAILED**, with Alloy's
error, until the agent reports a different outcome — including when Shepherd serves it a *new*
config that fails the same way. A collector shows **APPLIED** only after Alloy reported it or
proved it (see below).

This file supersedes its first version (#186), whose model of Alloy's reporting was incomplete: it
fixed the case below marked (a) and missed (b), which a UI walkthrough then found as F1.

## How Alloy reports (v1.20.1, read from source and captured on the wire)

`internal/service/remotecfg/config_manager.go`, `getRemoteConfigStatusForRequest`:

```go
// Send status if we've never sent one before (first call) or if it has changed
if cm.lastSentConfigStatus == nil ||
    cm.remoteConfigStatus.Status != cm.lastSentConfigStatus.Status ||
    cm.remoteConfigStatus.ErrorMessage != cm.lastSentConfigStatus.ErrorMessage {
```

So a status is sent only when the **(status, error message)** pair changes — never because the
config hash changed. The request `hash` is the last hash Alloy *received*, loaded or not.
`effective_config` is set only after a successful load and is sent whenever the loaded config
changes.

Captured requests from a real `grafana/alloy:v1.20.1` polling a recording collector.v1 server:

```
(a) one bad config
    hash=(empty)  status=UNSET
    hash=H(bad)   status=FAILED "2:3: Failed to build component: decoding configuration: …"
    hash=H(bad)   status=nil                                    (every later poll)

(b) the served config changes, the failure does not (F1) — the real dev served config;
    the header timestamp moved, the failing component stayed at line 40
    hash=HA       status=FAILED "40:3: Failed to build component: … KUBERNETES_SERVICE_HOST …"
    hash=HA       status=nil   (x3)
    hash=HB       status=nil   (x6)     ← HB also failed to load (Alloy logged it); no status re-sent
    hash=HC       status=FAILED "41:3: …"  ← one extra line above: a DIFFERENT message, so re-sent
    hash=HD       status=APPLIED  effective_config=set   ← the failing pipeline removed

(c) a good config replacing a good config
    hash=H2       status=nil  effective_config=set          ← no status: APPLIED → APPLIED is no change
```

A passive capture of the dev stack's own agent (tcpdump in its network namespace) matched (b):
after a label edit changed the served hash, every poll carried the new hash and no status.

What this means for a status-less poll with the served hash: **the agent's outcome is unchanged**
— unless the poll carries `effective_config`, which proves a load.

## Red

Before this change, `ClearStaleFailedStatus` promoted a FAILED to APPLIED on any status-less poll
with the served hash unless the FAILED had been recorded for that same hash. In (b) it was recorded
for HA and the poll carried HB, so the row read APPLIED while Alloy was failing. Reproduced live on
the dev stack (`make dev`): prod-eu-1/metrics showed APPLIED while `dev-alloy-metrics-1` logged
`failed to parse and load new remote configuration … received_hash=9c66c310 loaded_hash=""`.

The spec that asserted the old rule — "clears a FAILED recorded for a different config once the
agent polls the served hash" — encoded the bug and was replaced.

## Green

`ApplySilentPoll` (`internal/store/queries/collector_instances.sql`) replaces it: a status-less
poll with the served hash
- carrying `effective_config` → APPLIED (a verified load);
- on a row that never had a status (NULL, `''`, the sweeper's cleared `inactive`) → APPLIED;
- otherwise → the status stands, and its `remote_config_status_hash` follows the polled hash.

`MarkStaleInstancesInactive` no longer overwrites a FAILED: `inactive` is cleared on reconnect and
a cleared status is promoted, so it would have turned a failure into APPLIED.

Specs (`internal/agentapi/service_test.go`, "B1: stale FAILED status clearing"): (a) and (b) stay
FAILED with the hash following; `effective_config` on a failing and on an applied row both give
APPLIED; the sweeper leaves FAILED alone. Mutation checks, each run and each failing exactly one
spec: promoting FAILED again; ignoring `effective_config`; letting the sweeper overwrite FAILED.

Live, on the dev stack with the fix: after restarting `dev-alloy-metrics-1` the row went
`FAILED 47841195`; a label edit then served `ae0b4366`, Alloy failed to load it and sent no status,
and the row read `FAILED ae0b4366` — failed, on the config it had just been served.
