# Platform observability architecture — Grafana/Alloy discovery

> Point-in-time notes from 2026-08-18; re-verify against the platform repo before relying on specifics.

Sourced from: `<internal platform repository>`
Date: 2026-08-18

## Summary

The real platform uses **Grafana Alloy Operator** (`collectors.grafana.com/v1alpha1`) to manage
four distinct Alloy collector instances on a Kubernetes cluster named `test-cluster`.
All telemetry goes to Azure AD OAuth2-authenticated backends (Mimir, Loki, Tempo) plus a
local in-cluster Prometheus.

None of the current Alloy instances use `remotecfg` — they are driven by static ConfigMaps
via the Alloy Operator. Shepherd integration would require adding a `remotecfg` block to each.

---

## Four Alloy instances — roles and controller type

| Instance | Controller | Replicas | Role |
|---|---|---|---|
| `alloy-logs` | DaemonSet | 1 per node | Log collection from pods, nodes, k8s events |
| `alloy-metrics` | StatefulSet | 2–20 (HPA) | Cluster metrics, node-exporter, kube-state-metrics, operator objects |
| `alloy-receiver` | Deployment | 2–10 (HPA) | OTLP receiver (gRPC :4317, HTTP :4318) for app traces/metrics/logs |
| `alloy-singleton` | Deployment | 1 | K8s events, self-reporting metrics |

Matches Shepherd's supported roles: `logs`, `metrics`, `receiver`, `singleton`.

---

## Attribute/label schema (what Alloy stamps on all signals)

### Mandatory cluster identity labels
```
cluster            = "test-cluster"
k8s_cluster_name   = "test-cluster"
```
Applied as `external_labels` on `prometheus.remote_write` and `loki.write`, and via
OTTL `set` statements on OTel signals.

### Per-signal labels (relabeled from k8s metadata)
```
namespace, pod, container, workload, workload_type
app, component, source, node, instance, job
service_name, service_namespace, service_instance_id
```

### Log structured metadata
```
k8s_pod_name, pod, service_instance_id
k8s_deployment_name, k8s_statefulset_name, k8s_daemonset_name
```

---

## Telemetry destinations

### Local Prometheus (in-cluster)
- URL: `http://kube-prometheus-stack-prometheus.<namespace>.svc.cluster.local:9090/api/v1/write`
- Auth: none
- Used by: alloy-metrics, alloy-singleton

### Remote Mimir
- URL: from k8s Secret `oidc-secret` key `url` + `/api/v1/push`
- Auth: Azure AD OAuth2 — tenant `<azure-tenant-id>`
- Scope: `api://<azure-app-id>/.default`
- Multi-tenancy: `X-Scope-OrgID: test-cluster` (from secret key `tenantId`)
- TLS: mTLS (CA, cert, key from secret)
- Used by: alloy-metrics, alloy-singleton

### Remote Loki
- URL: from secret `url` + `/loki/api/v1/push`
- Auth: same Azure AD OAuth2
- Used by: alloy-logs, alloy-singleton
- Retry: 10 retries, backoff 500ms–5m

### Remote Tempo
- URL: from secret `url` (OTLP HTTP)
- Auth: `otelcol.auth.oauth2` — same tenant/scope
- Multi-tenancy: `X-Scope-OrgID: test-cluster`
- Used by: alloy-receiver

All destinations share a single `oidc-secret` populated by ExternalSecret from Azure Key Vault
path `<vault-path>/monitoring-oidc`. Secret keys: `client_id`, `client_secret`, `url`, `tenantId`.

---

## alloy-logs pipeline (key patterns)

```alloy
// Pod log collection — only pods in namespaces with label cpt_managed=true
discovery.kubernetes "pods" { role = "pod" }
// Annotation-based opt-out: logs.grafana.com/pods.enabled = false|no|skip

// service_name resolution priority:
// 1. annotation resource.opentelemetry.io/service.name
// 2. label app.kubernetes.io/name
// 3. container name

// Runtime detection: CRI-O/containerd → stage.cri; Docker → stage.docker

// Node logs via journald: /var/log/journal, max_age = 8h
// K8s manifest tail: pods with app.kubernetes.io/name=k8s-manifest-tail
```

## alloy-metrics pipeline (key patterns)

```alloy
// Annotation autodiscovery: prometheus.io/scrape = true
// k8s.grafana.com/metrics.* annotations for port, path, interval, timeout
// Default scrape interval: 60s, timeout: 10s

// Cluster metrics:
// - Kubelet /metrics (HTTPS, ~35 kept metrics)
// - Kubelet /metrics/resource
// - cAdvisor /metrics/cadvisor (~22 kept metrics)
// - kube-state-metrics (in monitoring namespace)

// Node exporter: pods with release=monitoring,app.kubernetes.io/name=node-exporter
// Alloy self-monitoring: all pods with app.kubernetes.io/name in (alloy-*)
// Prometheus Operator objects: PodMonitors, ServiceMonitors, Probes
// cert-manager: app.kubernetes.io/name=cert-manager

// All use clustering=true except singleton scrapes
```

## alloy-receiver pipeline (OTLP → Tempo)

```alloy
// OTLP receiver:
otelcol.receiver.otlp "default" {
  grpc { endpoint = "0.0.0.0:4317" max_recv_msg_size_mib = 4 }
  http { endpoint = "0.0.0.0:4318" max_request_body_size_mib = 20 }
}

// Processing chain:
// 1. resourcedetection (env + hostname)
// 2. k8sattributes (10 k8s metadata attrs; pod IP/UID/connection association)
// 3. connector.host_info (node metrics from traces)
// 4. processor.transform (cluster labels, semconv span name normalization)
// 5. processor.batch (8192 items, 2s timeout)

// Also receives:
// - otelcol.receiver.prometheus.tempo (prometheus scrapes → OTel)
// - otelcol.receiver.loki.tempo (loki logs → OTel)
// Normalizes flat Prometheus label names to OTel semconv before Tempo export
```

## alloy-singleton pipeline

```alloy
// Kubernetes events → Loki
loki.source.kubernetes_events "cluster_events" {
  job_name = "integrations/kubernetes/eventhandler"
  log_format = "logfmt"
}
// Extracts: component, kind, level (Normal→Info), name, node, reason

// Self-reporting metrics:
prometheus.exporter.unix "release_info" {
  textfile_directory = "/etc/release-info"
}
// Scrapes grafana_kubernetes_monitoring_* metrics → localprometheus + mimir
```

---

## Secret architecture (important for Shepherd integration)

The configs use `remote.kubernetes.secret` for dynamic credential injection:

```alloy
remote.kubernetes.secret "oidc" {
  namespace = "monitoring"
  name      = "oidc-secret"
}
// Then reference:
// remote.kubernetes.secret.oidc.data["url"]
// remote.kubernetes.secret.oidc.data["tenantId"]
// remote.kubernetes.secret.oidc.data["client_id"]
// remote.kubernetes.secret.oidc.data["client_secret"]
```

This means Alloy resolves secrets locally from k8s — Shepherd serves the config with
`remote.kubernetes.secret` blocks intact. Shepherd does NOT need to resolve these secrets.

---

## Implications for Shepherd seed / demo pipeline

1. **Role naming matches exactly**: `logs`, `metrics`, `receiver`, `singleton` — Shepherd's
   `validRoles` already covers all four. ✅

2. **Cluster attribute is the primary matcher key**: Shepherd matchers should use `cluster` label
   as the top-level discriminator. The real fleet uses `cluster = "test-cluster"`.

3. **Secrets via `remote.kubernetes.secret`**: The served config can contain these blocks verbatim.
   Shepherd doesn't need secret management — Alloy pulls secrets from the k8s cluster it runs on.

4. **A realistic dev seed pipeline for `alloy-metrics` role**:

```alloy
// Minimal but realistic: self-monitoring + cluster label
prometheus.exporter.self "shepherd_demo" {}

prometheus.scrape "self" {
  targets    = prometheus.exporter.self.shepherd_demo.targets
  forward_to = [prometheus.remote_write.local.receiver]
}

prometheus.remote_write "local" {
  endpoint {
    url = "http://localhost:9090/api/v1/write"  // dev no-op
  }
  external_labels = {
    cluster = "prod-eu-1",
  }
}
```

5. **Matchers for the dev seed**: `cluster="prod-eu-1"` (matches the dev Alloy containers).
   For multi-role demo, add `role="metrics"` as a second matcher.

6. **`remotecfg` block required**: None of the current Alloy configs have `remotecfg`. The dev
   Alloy containers already have it (via `alloy-metrics.alloy` etc.) but the production stack
   would need it added to connect to Shepherd. This is a key gap for the Shepherd rollout.

7. **`livedebugging { enabled = true }`** is in all configs — good practice for dev stack too.

---

## Not in scope for Shepherd (operates at config-serving layer only)

- ExternalSecret / Azure Key Vault integration — Shepherd serves rendered Alloy config
- Alloy Operator CRDs — Shepherd is agnostic to how Alloy is deployed
- K8s RBAC for Alloy — cluster-level concern
- Network policies — cluster-level concern
- HPA scaling — deployment-level concern
