package receiver_test

import "shepherd/internal/receiver"

// fixtureNames lists every golden fixture, shared by the golden comparison
// test, the real-alloy-binary validation test, and the (opt-in) generator.
var fixtureNames = []string{
	"otlp-basic",
	"otlp-passthrough",
	"faro-basic",
	"faro-cors-disabled",
	"faro-cors-wildcard",
	"combined",
}

// fixture returns the Config for one named fixture. Each one is real input
// this package's caller (a future serve-time assembler, outside this
// package's territory) would plausibly build: literal/secret headers use the
// same sys.env(...) convention internal/wizard/appobservability already
// established, so a destination URL or bearer token is never a literal in
// generated config.
func fixture(name string) receiver.Config {
	switch name {
	case "otlp-basic":
		// One tenant, both listeners, all three signals, a mix of gRPC and
		// HTTP exporters, and a mix of literal (tenant id) and secret-env
		// (bearer token) headers.
		return receiver.Config{
			OTLP: []receiver.OTLPPipeline{{
				Label: "acme",
				GRPC: &receiver.OTLPGRPCListener{
					ListenAddr: "0.0.0.0:4317", MaxRecvMsgSize: "4MiB", MaxConcurrentStreams: 100,
				},
				HTTP: &receiver.OTLPHTTPListener{
					ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB",
				},
				Batch: receiver.BatchConfig{Timeout: "5s", SendBatchSize: 1000, SendBatchMaxSize: 1500},
				Metrics: &receiver.OTLPExporter{
					Name: "acme_mimir", Protocol: receiver.ExporterGRPC,
					EndpointExpr: `sys.env("SHEPHERD_DEST_ACME_MIMIR_URL")`,
					Headers:      map[string]string{"X-Scope-OrgID": "acme"},
				},
				Logs: &receiver.OTLPExporter{
					Name: "acme_loki_otlp", Protocol: receiver.ExporterHTTP,
					EndpointExpr:    `sys.env("SHEPHERD_DEST_ACME_LOKI_URL")`,
					Headers:         map[string]string{"X-Scope-OrgID": "acme"},
					SecretHeaderEnv: map[string]string{"Authorization": "SHEPHERD_DEST_ACME_LOKI_TOKEN"},
				},
				Traces: &receiver.OTLPExporter{
					Name: "acme_tempo", Protocol: receiver.ExporterGRPC,
					EndpointExpr:    `sys.env("SHEPHERD_DEST_ACME_TEMPO_URL")`,
					SecretHeaderEnv: map[string]string{"Authorization": "SHEPHERD_DEST_ACME_TEMPO_TOKEN"},
				},
			}},
		}

	case "otlp-passthrough":
		// D10 shared/pass-through mode: ONE otelcol.receiver.otlp pipeline
		// serves every tenant behind the gateway. No exporter here carries a
		// tenant identifier literal — Render wires it from the
		// gateway-injected context automatically (Mode alone decides it).
		// Bearer-token auth to the destination is a separate concern from
		// tenant identity and still flows through SecretHeaderEnv exactly as
		// in the static fixture.
		return receiver.Config{
			OTLP: []receiver.OTLPPipeline{{
				Label: "shared",
				Mode:  receiver.TenancyPassThrough,
				HTTP: &receiver.OTLPHTTPListener{
					ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB",
				},
				Batch: receiver.BatchConfig{Timeout: "5s", SendBatchSize: 1000, SendBatchMaxSize: 1500},
				Metrics: &receiver.OTLPExporter{
					Name: "shared_mimir", Protocol: receiver.ExporterGRPC,
					EndpointExpr: `sys.env("SHEPHERD_DEST_SHARED_MIMIR_URL")`,
				},
				Logs: &receiver.OTLPExporter{
					Name: "shared_loki_otlp", Protocol: receiver.ExporterHTTP,
					EndpointExpr:    `sys.env("SHEPHERD_DEST_SHARED_LOKI_URL")`,
					SecretHeaderEnv: map[string]string{"Authorization": "SHEPHERD_DEST_SHARED_LOKI_TOKEN"},
				},
				Traces: &receiver.OTLPExporter{
					Name: "shared_tempo", Protocol: receiver.ExporterGRPC,
					EndpointExpr:    `sys.env("SHEPHERD_DEST_SHARED_TEMPO_URL")`,
					SecretHeaderEnv: map[string]string{"Authorization": "SHEPHERD_DEST_SHARED_TEMPO_TOKEN"},
				},
			}},
		}

	case "faro-basic":
		// A browser RUM pipeline with an explicit, named allowlist and rate
		// limiting left at the schema's own default values (named
		// explicitly, not inherited silently).
		return receiver.Config{
			Faro: []receiver.FaroPipeline{{
				Label: "acme_faro",
				Server: receiver.FaroServer{
					ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
					RateLimit: receiver.RateLimit{Enabled: true, Strategy: "global", Rate: 50, BurstSize: 100},
				},
				CORS: receiver.CORSPolicy{Origins: []string{"https://app.acme.example.com"}},
				Logs: &receiver.LokiExporter{
					Name: "acme_faro_logs", URLExpr: `sys.env("SHEPHERD_DEST_ACME_LOKI_URL")`,
					TenantID: "acme", BearerTokenEnv: "SHEPHERD_DEST_ACME_LOKI_TOKEN",
				},
				Traces: &receiver.OTLPExporter{
					Name: "acme_faro_tempo", Protocol: receiver.ExporterGRPC,
					EndpointExpr:    `sys.env("SHEPHERD_DEST_ACME_TEMPO_URL")`,
					SecretHeaderEnv: map[string]string{"Authorization": "SHEPHERD_DEST_ACME_TEMPO_TOKEN"},
				},
			}},
		}

	case "faro-cors-disabled":
		// CORSPolicy left at its zero value: the deliberate "no browser
		// origin may read this receiver's response" choice, logs only, rate
		// limiting explicitly turned off.
		return receiver.Config{
			Faro: []receiver.FaroPipeline{{
				Label: "globex_faro",
				Server: receiver.FaroServer{
					ListenAddr: "0.0.0.0", ListenPort: 12348, MaxAllowedPayloadSize: "2MiB",
					RateLimit: receiver.RateLimit{Enabled: false},
				},
				Logs: &receiver.LokiExporter{
					Name: "globex_faro_logs", URLExpr: `sys.env("SHEPHERD_DEST_GLOBEX_LOKI_URL")`,
					TenantID: "globex",
				},
			}},
		}

	case "faro-cors-wildcard":
		// The one place a wildcard is reachable: AllowAll set explicitly, as
		// its own named field rather than a "*" slipped into Origins.
		return receiver.Config{
			Faro: []receiver.FaroPipeline{{
				Label: "public_faro",
				Server: receiver.FaroServer{
					ListenAddr: "0.0.0.0", ListenPort: 12349, MaxAllowedPayloadSize: "5MiB",
					RateLimit: receiver.RateLimit{Enabled: true, Strategy: "per_app", Rate: 20, BurstSize: 40},
				},
				CORS: receiver.CORSPolicy{AllowAll: true},
				Traces: &receiver.OTLPExporter{
					Name: "public_faro_tempo", Protocol: receiver.ExporterHTTP,
					EndpointExpr: `sys.env("SHEPHERD_DEST_PUBLIC_TEMPO_URL")`,
				},
			}},
		}

	case "combined":
		// One OTLP pipeline and one Faro pipeline for the same tenant in a
		// single Config, deliberately sharing the human-readable tenant name
		// "acme" across both pipeline Labels to prove the batch-processor
		// naming scheme (faroTracesBatchLabel) cannot collide with the
		// OTLP pipeline's own batch processor label.
		otlp := fixture("otlp-basic")
		otlp.OTLP[0].Label = "acme"
		faro := fixture("faro-basic")
		faro.Faro[0].Label = "acme"
		faro.Faro[0].Logs.Name = "acme_faro_logs2"
		faro.Faro[0].Traces.Name = "acme_faro_tempo2"
		return receiver.Config{OTLP: otlp.OTLP, Faro: faro.Faro}

	default:
		panic("unknown fixture " + name)
	}
}
