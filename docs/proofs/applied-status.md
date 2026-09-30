# APPLIED means loaded, not polled (#115, B1)

**Claim.** After this fix, a collector whose agent rejected the served config stays **FAILED**
(with Alloy's own error) until the agent reports a status for a different config. Before it,
Shepherd showed **APPLIED** one poll after the rejection.

## What Alloy actually does (recorded 2026-09-29, Alloy v1.20.1)

A throwaway collector.v1 server logged every `GetConfig` request from a real
`grafana/alloy:v1.20.1` container polling every 10s (`remotecfg { poll_frequency = "10s" }`), while
serving, in turn: a valid config (A), a config that passes `alloy validate` but fails to decode at
load (B, `prometheus.scrape` with `scrape_timeout` > `scrape_interval`), A again, a config whose
component fails to build at load (C, `local.file` on a missing path), then A again. Both B and C
pass `alloy validate` v1.20.1 — i.e. they pass Shepherd's Stage 2 gate. (The walkthrough's
original example, an undeclared component reference, is now refused by `alloy validate` and never
reaches an agent.) Each line: the phase being served, the hash the agent sent, the status it sent,
and whether it sent `effective_config`:

```

```

After a restart the agent re-registers, polls with an empty hash, and reports `APPLIED` explicitly
for the config it fetches (recorded separately; same harness).

Read off the log:

1. A rejected config is reported **FAILED exactly once**, carrying **the rejected config's hash**.
2. Every later poll carries **that same hash and no status** — while Alloy keeps running the
   previous config (its own log: "failed to evaluate config", previous components still up).
3. A config Alloy loads is always reported **APPLIED explicitly**.
4. `effective_config` is sent only on the **first** successful apply after start — never on FAILED,
   never on later APPLIED. It cannot tell "loaded" from "rejected" (option 2 of the walkthrough's B1
   is not viable).

## Red

Shepherd's clear-back rule (`ClearStaleFailedStatus`) promoted FAILED to APPLIED on any status-less
poll whose hash matched the served hash — point 2 above, exactly. Removing the new guard from the
query (the `AND NOT (… 'FAILED' AND remote_config_status_hash IS NOT DISTINCT FROM polled_hash)`
clause) and running `ginkgo --focus B1 ./internal/agentapi`:

```
[FAIL] CollectorService GetConfig B1: stale FAILED status clearing [It] stays FAILED while the agent polls silently with the hash it rejected [integration]
FAIL! -- 5 Passed | 1 Failed | 0 Pending | 50 Skipped
```

## Green

Migration `0028` adds `collector_instances.remote_config_status_hash`; `UpdateInstanceStatus` stores
the hash a status was reported with; `ClearStaleFailedStatus` skips a FAILED recorded for the hash
now being polled. With the guard in place: `SUCCESS! -- 6 Passed` — the recorded sequence (FAILED
once, then silent polls with the same hash) stays FAILED with its error; an explicit APPLIED still
wins; a FAILED recorded for a different config, a never-reported status, and the sweeper's
`inactive` marker still promote to APPLIED on a served-hash poll, as before.
