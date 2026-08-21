# Red–green proof: what the S3 transform does, and does not, do to addresses

Behavior proved: the S3 transform (VB-1 §6.4) removes every authored ENDPOINT from the sandbox
config, and does **not** — cannot — bound what the sandbox can REACH. The reachability control is
the sandbox's network; its executable proof is `docs/proofs/simulator-containment.md` §P0.

**Stale counts, not re-run.** The availability fix (VB-1 §6.4, closing the round-2 finding that 39
artifact components rendered a transformed body missing a required attribute) added five entries to
`relabelDestinationPaths` for a DIFFERENT reason than the five below: a required attribute that is
address-SHAPED by name but not address-shaped by semantics (a local bind address the sandbox itself
listens on, or a label name rather than a network target) now survives on purpose, because dropping
it would leave a config the sandbox cannot load at all. It also moved 19 components — most with an
`address`-shaped required key of their own, following the `prometheus.exporter.blackbox`/`snmp`
precedent below — to `sim_unsupported`. Both changes move the "679 probed / 575 rendered" numbers
this document's transcript captured (now 679 probed / 539 rendered, per `transform_address_test.go`)
and the five-entry table just below (now ten; see `relabelDestinationPaths`'s own doc comment). The
transcript below is reproduced verbatim from when it was captured and was not re-run for this fix —
the live counts are the Go test, not this file.

This document used to be titled "no authored address reaches the sandbox" and claimed the
transform closed the reachability half of §6.4's containment claim. That claim was wrong, an
independent attack panel proved it wrong against real Alloy v1.18.1, and the correction is the
whole point of this revision. The feature remains **disabled by default**.

## The correction, stated first

A `discovery.relabel` rule retargets a scrape at **runtime**:

```
rule {
  source_labels = ["o1", "o2", "o3", "o4"]
  separator     = "."
  regex         = "(.*)"
  target_label  = "__address__"
  replacement   = "${1}:9999"
}
```

`rule.*.target_label` and `rule.*.replacement` are on the keep list, and have to be — relabel is
the single mechanism S3 exists to let people simulate, and the alternative is deleting relabel.
The address this graph dials exists only after Alloy has joined four ordinary label values; no
host-shaped token appears anywhere in the rendered config. Static analysis of rendered text
therefore cannot bound where the scrape lands, no matter how the analysis is written.

The overlay's own acknowledgement for those paths said *"a relabel destination LABEL name, never a
network address"*. That was the error in one line: `__address__` **is** a label name, and writing
it is exactly how Prometheus retargets. All five acknowledgements have been rewritten to say so.

### Rule P5 is deleted

`address_not_harness` parsed the render and failed the run on any host-shaped literal that was not
a harness address. It was permeable and deny-everything at the same time — the signature of the
wrong instrument — and both halves are executed below:

- **Permeable.** With P5 restored verbatim, the three retarget specs still pass. It never saw the
  attack it was built for.
- **Deny-everything.** With P5 restored verbatim, all five relabel-destination probes in the
  artifact sweep are refused outright.

## The mechanisms that remain, and what each is for

1. **The network** (`e2e/sandbox_egress_test.go`, `make e2e-egress`, run by
   `.github/workflows/e2e.yml`). THE reachability control. The simulator runs on a Docker
   `internal: true` network — not alone: `shepherd` is attached to `sim-internal` too, which is
   exactly open critical **B-CONTAIN-1** (`docs/project-status.md`) — and the sandbox runs in the
   simulator container's namespace, so it has no route off that network. `P-deny-ip` dials a hermetic canary by literal IP from that namespace and goes red the
   moment `internal: true` is removed.
2. **Rule K + the `target_set` value class** (`internal/simulate/transform.go`). The CREDENTIAL
   control, which also removes the trivial literal-address case: `targets` is kept with class
   `target_set`, so rule K rebuilds each label set, forcing `__address__` to
   `Harness.TargetAddress` and `__scheme__` to `http` and dropping the other request-steering meta
   labels. Defence in depth and a fidelity win. Not containment.
3. **`CheckEndpoints`** (`internal/simsvc/guard.go`). Declared defence in depth, deny-by-default
   over any-scheme URLs, bracketed IPv6, DSN-embedded hosts and any *computed* expression. It
   makes a transform bug that left a NAMED endpoint in the config fail loudly. It sees no host in
   the retarget graph either, and the specs say so.

`internal/netshape` is the shared shape table for 2 and 3. Its package doc has been rewritten:
it recognises shapes in text and cannot bound a computed address.

---

## Green run

```
$ go build ./...
$ go test ./internal/... -count=1
ok  	shepherd/internal/ado	0.280s
ok  	shepherd/internal/agentapi	11.567s
ok  	shepherd/internal/auth	3.987s
ok  	shepherd/internal/cli	0.783s
ok  	shepherd/internal/config	0.857s
ok  	shepherd/internal/crypto	0.956s
ok  	shepherd/internal/gitrepo	21.829s
ok  	shepherd/internal/gitsync	20.049s
ok  	shepherd/internal/graph	1.280s
ok  	shepherd/internal/merge	1.648s
ok  	shepherd/internal/mgmtapi	12.960s
ok  	shepherd/internal/netshape	2.031s
ok  	shepherd/internal/schema	3.368s
ok  	shepherd/internal/server	1.808s
ok  	shepherd/internal/simsvc	3.046s
ok  	shepherd/internal/simulate	20.143s
ok  	shepherd/internal/store	5.818s
ok  	shepherd/internal/validate	1.947s
ok  	shepherd/internal/visual	14.762s
ok  	shepherd/internal/wizard/appobservability	0.506s

$ golangci-lint run ./...
0 issues.
```

The artifact-derived sweep, restated so the numbers are on the record: **679** address-named
attribute paths probed with an outside host planted at each, **579** of them reaching a rendered
sandbox config, and the outside host surviving at exactly **5** — every one a relabel rule's
`target_label`, enumerated in `relabelDestinationPaths` so a sixth cannot appear silently:

```
discovery.relabel                rule.*.target_label
loki.relabel                     rule.*.target_label
prometheus.relabel               rule.*.target_label
prometheus.remote_write          endpoint.*.write_relabel_config.*.target_label
pyroscope.relabel                rule.*.target_label
```

Before this change the sweep asserted `leaked` was EMPTY and reported 574 renders. The difference
is entirely rule P5 refusing those five probes, which is what "deny-everything" looked like from
the inside.

---

## RED A — rule P5 restored verbatim: it refuses five ordinary paths

`internal/simulate/provenance.go`, the deleted host check and `harnessHosts` put back exactly as
they were, plus the `CodeAddressNotHarness` constant:

```
$ go test ./internal/simulate/ -count=1

  [FAILED] the set of address-named paths that keep their authored value changed; if that is
  intended, update relabelDestinationPaths and docs/proofs/simulator-containment.md together
  Expected
      <[]string | len:0, cap:0>: nil
  to consist of
      <[]string | len:5, cap:5>: [
          "discovery.relabel rule.*.target_label",
          "loki.relabel rule.*.target_label",
          "prometheus.relabel rule.*.target_label",
          "prometheus.remote_write endpoint.*.write_relabel_config.*.target_label",
          "pyroscope.relabel rule.*.target_label",
      ]

Summarizing 1 Failure:
  [FAIL] Transform: no authored address reaches the sandbox [It] plants an outside host at every
         address-named attribute path in the artifact and finds it only at the relabel destinations

Ran 81 of 81 Specs in 14.133 seconds
FAIL! -- 80 Passed | 1 Failed | 0 Pending | 0 Skipped
```

`leaked` is **nil** under the mutation: P5 refuses the whole run for all five, so nothing reaches
a render at all. Those five paths are how a user writes a relabel rule.

## RED A, second half — the same restored P5 cannot see the attack

Same mutation, still in place, focused on the runtime-retarget specs:

```
$ go test ./internal/simulate/ -count=1 -v -args -ginkgo.focus="runtime retarget"
Ran 3 of 81 Specs in 2.130 seconds
SUCCESS! -- 3 Passed | 0 Failed | 0 Pending | 78 Skipped
```

Green. The gate that refuses five legitimate relabel paths passes the graph that steers the
sandbox at an arbitrary host. That contrast — one mutation, both results — is the argument for
deleting it.

Restored: `ok shepherd/internal/simulate 14.809s`, `golangci-lint 0 issues`.

## RED B — the retarget spec can fail

The new spec asserts a NEGATIVE (the transform does not contain the retarget), so it has to be
shown capable of going red. `rule.*.target_label` removed from `discovery.relabel`'s `sim_keep`
in `internal/schema/artifacts/overlay.json`:

```
$ go test ./internal/simulate/ -count=1 -v -args -ginkgo.focus="runtime retarget"
• [FAILED] [0.072 seconds]
  [FAILED] rendered:
  Expected
  to contain substring
      <string>: target_label = "__address__"

Ran 3 of 81 Specs in 3.107 seconds
FAIL! -- 2 Passed | 1 Failed | 0 Pending | 78 Skipped
```

That is the intended trip-wire: anyone who reintroduces a static reachability gate, by keep list
or otherwise, turns this red and has to update VB-1 §6.4 and
`docs/proofs/simulator-containment.md` in the same change rather than leaving two documents
disagreeing about which control is load-bearing.

Restored: `ok shepherd/internal/simulate`, `ok shepherd/internal/schema`.

## RED C — the old `extractHost` restored (H6's five bypasses return)

Unchanged by this revision, and still the reason `CheckEndpoints` is worth having as defence in
depth. `internal/simsvc/guard.go` reverted to recognising http(s) URLs plus
`^[A-Za-z0-9_.\-]+:[0-9]{1,5}$`, with the `CallExpr`/`BinaryExpr` refusal removed:

```
$ go test ./internal/simsvc/ -count=1

Summarizing 6 Failures:
  [FAIL] Endpoint allowlist refuses every shape finding H6 proved bypassed the old allowlist
         [It] bypass 1: bracketed IPv6 host:port
  [FAIL] … [It] bypass 2: a bare host name with no port
  [FAIL] … [It] bypass 3: an endpoint the config COMPUTES rather than names
  [FAIL] … [It] bypass 4: a host buried in a MySQL DSN
  [FAIL] … [It] bypass 5: a scheme that is not http(s)
  [FAIL] … [It] string concatenation, which builds a URL out of parts

Ran 76 of 76 Specs in 1.007 seconds
FAIL! -- 70 Passed | 6 Failed | 0 Pending | 0 Skipped
```

---

## A leak this work found rather than fixed-by-plan

Six stub fixtures carry a realistic host name in a discovery meta label
(`__meta_eureka_app_instance_hostname`, `__meta_dns_name`, `__meta_puppetdb_certname`,
`__meta_nerve_endpoint_host`, `__meta_uyuni_minion_hostname`, `__meta_url`), because relabel rules
downstream match on those label names and inventing them would make S3 behave differently from
production. The canonical relabel idiom for those mechanisms is `target_label = "__address__"`, so
a fixture host name was one user-authored rule away from being a scrape target — and, per this
document, nothing downstream would have caught it.

`targetsValue` substitutes the harness address for every fixture label value that names a host.
That is hygiene on values the TRANSFORM invents; it is not a control on values the user authored.

---

## Scope: what is still open

- **H5 (Helm).** The chart now ships the simulator artifacts —
  `deploy/helm/shepherd/templates/{deployment,service,serviceaccount,networkpolicy}-simulator.yaml`,
  default-deny egress plus `automountServiceAccountToken: false`, asserted by
  `deploy/helm/chart_test.go`. What remains open is that a rendered-template assertion is not a
  probe: nothing dials from inside a real cluster's simulator Pod the way `P-deny-ip` dials from
  inside the compose one (see `docs/proofs/simulator-containment.md` and
  `docs/kind-test-environment-plan.md`).
- **M9 (unstubbed sources).** The 80 unstubbed `sources` components no longer leak credentials
  (empty keep list) but still render as empty bodies rather than failing closed.
- **M13 (renderer list-cardinality).** **Fixed** (`refValue` in `internal/visual/render.go`;
  evidence in `docs/proofs/sandbox-sim-e2e.md` §1). The e2e retarget spec no longer settles for
  acceptance plus network denial: it now asserts the retargeted scrape actually EXECUTES against
  the canary's address, read off the sandbox's own captured series
  (`instance=<canary-ip>:8080`). Non-containment by the transform is proven by execution.
- Findings 7, 8, 12, 14, 17 are untouched by this work.
