# The S3 simulation transform drops every secret-typed prop

**Claim proved:** VB-1 §6.4's containment claim — "no real credentials ever enter the sandbox" —
is a property of `internal/simulate.Transform`, not of the validator, not of the NetworkPolicy, and
not of a case list that happens to be complete today.

**The line under test** (`internal/simulate/transform.go`):

```go
rewrites = append(rewrites, dropSecretTypedProps(&out, req.Schema)...)
```

Rules G (source stubs) and D (destination endpoints) delete by **path** and by **reference**. This
sweep is the only code that deletes by **declared type**, at any depth, in any value form. That
separation is a design constraint, not an implementation detail: if a future refactor made
`rewriteDestinations` opportunistically strip secret-typed siblings, deleting this line would leave
the suite green and this proof would silently stop proving anything. A reviewer re-running the drill
should check that separation first.

---

## Why validation cannot be the safety net

Verified against the real binary, `grafana/alloy:v1.18.1`. The exact config the transform would ship
with the sweep removed — capture URL rewritten, `sys.env` password intact — validates cleanly:

```alloy
prometheus.scrape "app" {
  targets = [discovery.relabel.k8s.output]
  forward_to = [prometheus.remote_write.sink.receiver]
  bearer_token = sys.env("TOKEN")
}

discovery.relabel "k8s" {
  targets = [{__address__ = "simulator.example.com:9111"}]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
    basic_auth {
      username = "shepherd"
      password = sys.env("PROM_PASSWORD")
    }
  }
}
```

```
$ docker run --rm -v "$dir:$dir:ro" grafana/alloy:v1.18.1 \
    validate --stability.level=experimental "$dir/leak.alloy"
exit=0
```

Exit 0. `alloy validate` has no opinion about whether a password is real. Only the transform does.

The same run also confirms the correction this milestone had to make to the design text —
`discovery.static`, which §6.4 names as the discovery stub, does not exist in Alloy v1.18.1:

```
$ docker run ... validate --stability.level=experimental "$dir/static.alloy"
Error: .../static.alloy:1:1: cannot find the definition of component name "discovery.static"
Error: validation failed
exit=1
```

The stub is therefore a real `discovery.relabel` carrying a literal targets list. All nine
transformed corpus graphs pass stages 1–2 against the real binary
(`internal/simulate/transform_validate_test.go`, `Label("needs-alloy-binary")`):

```
$ TMPDIR=... SHEPHERD_VALIDATE_ALLOY_BINARY=<alloy-in-docker shim> \
    go test ./internal/simulate/ -count=1 --ginkgo.label-filter="needs-alloy-binary"
ok  	shepherd/internal/simulate	6.890s
```

---

## The fixture

`internal/simulate/transform_secrets_test.go` builds a graph carrying a credential in every form the
renderer can emit. Two of them are the ones that matter, because **no other rule can see them**:

- `prometheus.remote_write` → `endpoint[0].basic_auth.password = {"$expr": "sys.env(\"PROM_PASSWORD\")"}`
  — two blocks deep, environment-sourced. Not at a mapped endpoint path (rule D never visits it) and
  not a reference to a secret-source node (rule S1 has nothing to match).
- `prometheus.scrape` → `GraphBinding{Prop: "bearer_token", Ref.Expr: "sys.env(\"TOKEN\")"}` — a
  binding with no secret-source node behind it. The renderer emits it as a plain expression, so it
  raises no `secret_by_value` diagnostic either.

---

## Red run 1 — the sweep deleted

`rewrites = append(rewrites, dropSecretTypedProps(&out, req.Schema)...)` replaced by
`_ = dropSecretTypedProps`. The transform's own post-condition (rule P) catches it and refuses to
return a graph at all:

```
$ go test ./internal/simulate/ -count=1

• [FAILED] [0.036 seconds]
Transform: no credential reaches the sandbox [It] drops every credential form the renderer can emit
/Users/.../internal/simulate/transform_secrets_test.go:42

  [FAILED] Unexpected error:
      <simulate.TransformErrors | len:5, cap:8>:
      transformed graph still carries a secret-typed value at bearer_token;
      transformed graph still carries a secret-typed value at endpoint.0.basic_auth.0.password;
      transformed graph still carries a secret-typed value at endpoint.0.bearer_token;
      transformed graph still binds the secret-typed attribute "bearer_token";
      "bearer_token" is a secret: supply it as an expression (a binding or {"$expr": ...}), never as a literal

• [FAILED] [0.037 seconds]
Transform: no credential reaches the sandbox [It] drops a secret-typed binding that no other rule can see

  [FAILED] Unexpected error:
      <simulate.TransformErrors | len:1, cap:1>:
      transformed graph still binds the secret-typed attribute "bearer_token"

Summarizing 2 Failures:
  [FAIL] Transform: no credential reaches the sandbox [It] drops every credential form the renderer can emit
  [FAIL] Transform: no credential reaches the sandbox [It] drops a secret-typed binding that no other rule can see

Ran 35 of 36 Specs in 1.073 seconds
FAIL! -- 33 Passed | 2 Failed | 0 Pending | 1 Skipped
```

The self-check names the exact paths that would have leaked. Every other spec still passes, which is
what makes the attribution unambiguous.

## Red run 2 — the sweep AND the self-check deleted

Rule P is the runtime backstop; the grep over the rendered text is the test-level one. To show the
grep is itself load-bearing rather than shadowed by the self-check, the second drill also replaced
`if e := checkContainment(...)` with `_, _ = checkContainment, removed`:

```
$ go test ./internal/simulate/ -count=1

  [FAILED] "sys.env(\"SCRAPE_TOKEN\")" survived the transform:
  // generated by shepherd visual builder — do not edit by hand ...

  prometheus.scrape "app" {
    bearer_token = sys.env("SCRAPE_TOKEN")
  }

  prometheus.scrape "app2" {
    bearer_token = sys.env("TOKEN")
  }

  loki.write "logs" {
    endpoint {
      url = "http://simulator.example.com:9110/capture/loki/loki/api/v1/push"
    }
  }

  otelcol.exporter.otlphttp "otlp" {
    client {
      endpoint = "http://simulator.example.com:9110/capture/otlphttp"
    }
  }

  prometheus.remote_write "sink" {
    endpoint {
      url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
      basic_auth {
        username = "shepherd"
        password = sys.env("PROM_PASSWORD")
      }
    }
  }

Summarizing 2 Failures:
  [FAIL] ... [It] drops every credential form the renderer can emit
  [FAIL] ... [It] drops a secret-typed binding that no other rule can see

Ran 35 of 36 Specs in 1.156 seconds
FAIL! -- 33 Passed | 2 Failed | 0 Pending | 1 Skipped
```

That render is the leak, in full: the destination has been re-pointed at the harness and the
credentials are still there. It is also, per the section above, a config `alloy validate` accepts.

## Green run — both restored

```
$ go test ./internal/simulate/ -count=1
ok  	shepherd/internal/simulate	1.625s

$ go test ./internal/simulate/... ./internal/schema/... -count=1
ok  	shepherd/internal/simulate	1.725s
ok  	shepherd/internal/schema	0.973s
```

---

## Scope note

The specs are in `internal/simulate/transform_secrets_test.go` so the drill names one file. The
containment property is asserted four ways, deliberately split between structure and text so a bug
that defeats one is unlikely to defeat the other:

| Check | Where | What it asserts |
|---|---|---|
| P1 | inside `Transform`, re-asserted in tests | no surviving prop or binding has declared type `secret` |
| P2 | `checkContainment` | no removed secret source's `<component>.<label>.` token appears in the render |
| P2' | `checkContainment` | no `sim_secret_source` component name appears in the render at all |
| P3 | `checkContainment` | the render produces zero `secret_by_value` diagnostics |
