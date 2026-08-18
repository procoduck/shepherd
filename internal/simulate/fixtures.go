// Package simulate provides deterministic S2 stage-trace evaluation for the visual builder.
package simulate

// BuiltinRelabelTargets returns the three built-in k8s pod discovery label sets used as S2 fixtures.
func BuiltinRelabelTargets() []map[string]string {
	return []map[string]string{
		{"__meta_kubernetes_pod_annotation_prometheus_io_scrape": "true", "__meta_kubernetes_pod_name": "api-server-abc123", "__meta_kubernetes_namespace": "production", "__meta_kubernetes_pod_label_app": "api-server", "__address__": "10.0.0.1:8080"},
		{"__meta_kubernetes_pod_name": "worker-xyz789", "__meta_kubernetes_namespace": "production", "__meta_kubernetes_pod_label_app": "worker", "__address__": "10.0.0.2:9090"},
		{"__meta_kubernetes_pod_annotation_prometheus_io_scrape": "true", "__meta_kubernetes_pod_name": "db-proxy-def456", "__meta_kubernetes_namespace": "staging", "__meta_kubernetes_pod_label_app": "db-proxy", "__address__": "10.0.0.3:5432"},
	}
}

// BuiltinLogLines returns the three built-in log line fixtures (JSON, logfmt, multiline) used as S2 fixtures.
func BuiltinLogLines() []string {
	return []string{
		`{"level":"info","ts":"2026-08-18T10:00:00Z","caller":"main.go:42","msg":"request completed","method":"GET","path":"/api/health","status":200,"duration_ms":12}`,
		`level=info ts=2026-08-18T10:01:00Z caller=handler.go:88 msg="query executed" table=pipelines rows=15 duration_ms=3`,
		"java.lang.NullPointerException: Cannot invoke method\n\tat com.example.App.process(App.java:42)\n\tat com.example.App.main(App.java:15)",
	}
}
