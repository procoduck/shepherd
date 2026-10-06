# APPLIED means loaded, not polled (#115, B1 — and F1)

**Claim.** A collector whose agent rejected the config it was served shows **FAILED**, with Alloy's
error, until the agent reports a different outcome — including when Shepherd serves it a *new*
config that fails the same way. A collector shows **APPLIED** only after Alloy reported it or
proved it (see below) — and only while it is about the config being served now: from the
moment Shepherd serves a new config until the agent reports on it, the collector shows APPLYING.

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

(Since #213 the served header carries no generation timestamp, so a recompute of an unchanged
config no longer moves the hash as it did in the captures above. (b) stays reachable: any real
change to the served config that leaves the failing component's line and message alone — a
pipeline edited below it, say — produces the same new-hash-no-status sequence.)

What this means for a status-less poll with the served hash: **the agent's outcome is unchanged**
— unless the poll carries `effective_config`, which proves a load.

### Refused after APPLIED (2026-10-06, before v0.15.0)

#281's e2e-k8s run 37448835299 read the collector as APPLIED while its Alloy logged
`failed to parse and load new remote configuration … received_hash=339c1c85 loaded_hash=811c9dc5`
(`811c9dc5` is Alloy's FNV hash of the empty config: the claimed cluster had no pipeline yet) for
a `stage.logfmt {}` it refused. Re-captured the same way as (a)–(c): a real
`grafana/alloy:v1.20.1` (by the `deploy/versions.env` digest, `poll_frequency = "10s"`) polling a
throwaway recording collector.v1 server that serves a scripted sequence, answers `not_modified`
when the polled hash is the served one, and logs each request. Hashes are sha256 of the content,
as Shepherd's are; `effective_config` is shown as the sha256 of its body, so it names the config
it describes. `BAD1` is `loki.process "p" { forward_to = [] stage.logfmt { } }`, `BAD2` adds a
good component below it (same message), `BAD3` one above it (message moves to `2:1`).

```
(d) APPLIED for an empty config, then a config Alloy refuses (the #281 sequence)
    hash=(empty)  status=UNSET                                   → served EMPTY
    hash=EMPTY    status=APPLIED  effective_config=unset         ← an empty config sets none
    hash=EMPTY    status=nil
    hash=EMPTY    status=nil                                     → served BAD1
    hash=BAD1     status=FAILED "1:1: … logfmt mapping or regex is required"   (same second:
                                                                   the notify after the load)
    hash=BAD1     status=nil   (every later poll)
    alloy: failed to parse and load new remote configuration received_hash=ed34c73c
           loaded_hash=811c9dc5 … successfully restored cached configuration

(e) APPLIED for config A, then a config B Alloy refuses; an outage; F1; recovery
    hash=(empty)  status=UNSET                                   → served A
    hash=A        status=APPLIED  effective_config=A
    hash=A        status=nil                                     → served BAD1
    hash=BAD1     status=FAILED "1:1: …"   effective_config=unset   ← A was restored from cache;
                                                                       A's was already sent
    hash=BAD1     status=nil  (x2)
    hash=BAD1     (request answered with an error)
    hash=BAD1     status=FAILED "unavailable: …"  effective_config=A ← a failed request resets
                                                                       BOTH; they travel together
    hash=BAD1     status=nil  (x2)                               → served BAD2
    hash=BAD2     status=FAILED "1:1: …"   ← re-sent only because the outage changed the message
    hash=BAD2     status=nil  (x2)                               → served BAD3
    hash=BAD3     status=FAILED "2:1: …"
    hash=BAD3     status=nil  (x2)                               → served C
    hash=C        status=APPLIED  effective_config=C

(f) a good config replacing a good one — (c) again
    hash=A        status=nil                                     → served C
    hash=C        status=nil  effective_config=C

(g) Alloy restarts while served a config it refuses (cache holds A)
    hash=(empty)  status=UNSET                                   → served BAD1
    hash=BAD1     status=FAILED "1:1: …"  effective_config=A     ← A loaded from the on-disk cache
    hash=BAD1     status=nil  (every later poll)
```

So Alloy does report the refusal, at once and with the new hash (`fetchLoadConfig` →
`notifyStatusUpdate` makes an extra GetConfig right after a load attempt), and Shepherd records
FAILED for it. Checked and ruled out: `effective_config` describing the *previous* config arriving
on a status-less poll. After a refused load Alloy restores its cached config and `effective_config`
is that config again — but it was already sent, so nothing is sent; and when a request fails Alloy
resets the last-sent status *and* effective config together (`getConfig` in `remotecfg.go`), so a
re-sent stale `effective_config` always travels with a status, and a status always wins
(`applySilentPoll` is a no-op). Neither (d), (e) nor (g) ever has a status-less poll carrying an
`effective_config` that is not the served config.

The hole was in reading: the row is the agent's last outcome **and the hash it was about**
(`remote_config_status_hash`), and the management API presented the outcome without the hash.
From the moment Shepherd serves a new config until the agent's next request — up to a full
`poll_frequency` — the row still says APPLIED about the previous config. In run 37448835299 the
spec read APPLIED at 10:30:41.259; Alloy received the new config at 10:30:41.380 and refused it
at 10:30:41.420.

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
- on a row that never had a status (NULL, `''`) → APPLIED;
- otherwise → the status stands, and its `remote_config_status_hash` follows the polled hash.

Staleness never touches the outcome (#237). "Inactive" is derived when status is read — an
instance whose `last_seen` is older than `agent.inactive_after` is *presented* as `inactive` by the
management API (`presentInstanceStatus`, `internal/mgmtapi/rpc_fleet.go`) and left out of
`CountActiveInstances` — and is never written to `remote_config_status`. The lifecycle sweeper only
hard-deletes past `agent.delete_after`, and a reconnect (`UpsertCollectorInstance`) only moves
`last_seen`. So a stale FAILED instance that comes back and polls silently is still FAILED, and
the "never had a status → APPLIED" branch above can only be reached by a row that genuinely never
reported. (Until #237 the sweeper wrote an `inactive` sentinel that reconnect cleared to NULL —
which that branch then promoted to APPLIED — so the sweeper had to skip FAILED rows, and a
replaced Kubernetes pod's FAILED row, never reconnected under its old id, stayed FAILED until
`delete_after`. Migration `0029` turned leftover sentinels into NULL, the value reconnect used to
give them.)

Specs (`internal/agentapi/service_test.go`, "B1: stale FAILED status clearing"): (a) and (b) stay
FAILED with the hash following; `effective_config` on a failing and on an applied row both give
APPLIED; a FAILED instance that goes stale, is swept and reconnects with a silent poll stays FAILED.
`internal/agentapi/sweeper_test.go` asserts the sweep leaves every stale outcome as it was;
`internal/mgmtapi/collectors_metadata_test.go` ("inactive at read time (#237)") asserts the
presentation. Mutation checks, each run and each failing exactly one spec: promoting FAILED again;
ignoring `effective_config`; (pre-#237) letting the sweeper overwrite FAILED. Since #237, making a
reconnect reset a stale row's status to NULL fails the stale-reconnect FAILED spec above, its
agentapi sibling and the mgmtapi reconnect spec — and the #237 specs themselves were red before
the change (a stale FAILED instance read FAILED; the sweep wrote `inactive` over APPLIED).

Live, on the dev stack with the fix: after restarting `dev-alloy-metrics-1` the row went
`FAILED 47841195`; a label edit then served `ae0b4366`, Alloy failed to load it and sent no status,
and the row read `FAILED ae0b4366` — failed, on the config it had just been served.

## Green: APPLIED is about the served config

`presentInstanceStatus` (`internal/mgmtapi/rpc_fleet.go`) presents a stored APPLIED whose `remote_config_status_hash` is
not the collector's current `serve_cache.hash` as **APPLYING** — Alloy's own word for a config it
has been sent and not reported on. `ListCollectorInstancesByCollector` and
`GetLatestCollectorInstanceSummary` return both hashes (a `LEFT JOIN serve_cache`). Only APPLIED is
qualified: a FAILED about an earlier config stays FAILED (F1 above). A status with no recorded hash
(a row from before `0028`), a collector with no `serve_cache` row and a dirty placeholder's empty
hash are shown as stored. Nothing is written: the stored row is unchanged, and the next report
(the notify right after the load) turns it APPLIED or FAILED. Specs:
`internal/agentapi/service_test.go` "APPLIED is about the config being served" replays (d), (e)
and (c)/(f) request by request through `GetConfig` and reads `FleetService.GetCollector` and
`ListCollectors`; `internal/mgmtapi/collectors_metadata_test.go` "APPLIED about an earlier config
than the one served" pins the rule. Red before the change: the three agentapi specs and the first
mgmtapi spec read APPLIED where APPLYING was expected. Mutation checks, each failing exactly one
spec group: dropping the APPLYING branch (the three agentapi specs and the mgmtapi APPLYING spec);
dropping the empty-served-hash guard (the "cannot tell" spec); qualifying every status, not only
APPLIED (the F1 spec).
