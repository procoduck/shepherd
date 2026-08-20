# Golden wire captures from real Alloy v1.18.1

These three files are the **actual bytes** `grafana/alloy:v1.18.1` put on the wire, recorded
off a live run. They are not encoded by the same libraries that decode them: a round-trip
through one library proves only that the library round-trips, and would stay green if the
compression handling, the accepted content types or the message schema regressed.

| File | Produced by | Wire facts it pins |
|---|---|---|
| `remote_write_alloy_v1.18.1.snappy` | `prometheus.remote_write` | `Content-Encoding: snappy`, `X-Prometheus-Remote-Write-Version: 0.1.0`, snappy **block** format wrapping `prompb.WriteRequest`; 576 series including the relabel-produced `sim_marker="shepherd"` label |
| `loki_push_alloy_v1.18.1.snappy` | `loki.write` | `Content-Encoding: snappy`, `Content-Type: application/x-protobuf`, snappy block format wrapping `push.PushRequest`; one stream, labels rendered as the string `{filename="…", job="simlogs"}` |
| `otlp_metrics_alloy_v1.18.1.pb.gz` | `otelcol.exporter.otlphttp` | **`Content-Encoding: gzip`** — the exporter gzips by default, so a receiver without a gunzip branch answers 400 and Alloy drops the batch |

## Reproducing

1. Run a recorder on the host that dumps request bodies for
   `POST /capture/prometheus/api/v1/write`, `POST /capture/loki/loki/api/v1/push` and
   `POST /capture/otlphttp/v1/metrics` (the OTLP handler must answer `200` with
   `Content-Type: application/x-protobuf`, the others `204`), listening on `:9410`.

2. Write `config.alloy` pointing at that recorder through `host.docker.internal`:

   ```alloy
   prometheus.exporter.self "sim" {}

   discovery.relabel "sim" {
     targets = prometheus.exporter.self.sim.targets
     rule {
       target_label = "sim_marker"
       replacement  = "shepherd"
     }
   }

   prometheus.scrape "sim" {
     targets         = discovery.relabel.sim.output
     forward_to      = [prometheus.remote_write.cap.receiver]
     job_name        = "sim"
     scrape_interval = "2s"
     scrape_timeout  = "1s"
   }

   prometheus.remote_write "cap" {
     endpoint {
       url = "http://host.docker.internal:9410/capture/prometheus/api/v1/write"
       queue_config { batch_send_deadline = "1s" }
     }
   }

   local.file_match "logs" {
     path_targets = [{__path__ = "/sim/logs/app.log", job = "simlogs"}]
   }

   loki.source.file "logs" {
     targets    = local.file_match.logs.targets
     forward_to = [loki.write.cap.receiver]
   }

   loki.write "cap" {
     endpoint {
       url        = "http://host.docker.internal:9410/capture/loki/loki/api/v1/push"
       batch_wait = "1s"
     }
   }

   prometheus.scrape "otel" {
     targets         = discovery.relabel.sim.output
     forward_to      = [otelcol.receiver.prometheus.otel.receiver]
     job_name        = "simotel"
     scrape_interval = "2s"
     scrape_timeout  = "1s"
   }

   otelcol.receiver.prometheus "otel" {
     output { metrics = [otelcol.exporter.otlphttp.cap.input] }
   }

   otelcol.exporter.otlphttp "cap" {
     client {
       endpoint = "http://host.docker.internal:9410/capture/otlphttp"
       tls { insecure = true }
     }
   }
   ```

   `scrape_timeout` must be set alongside a sub-10s `scrape_interval`: Alloy's default timeout
   is 10s and `scrape_timeout (10s) greater than scrape_interval (2s)` aborts the whole load.

3. Put two log lines in `./logs/app.log`, then:

   ```sh
   docker run --rm --name shepsim-golden \
     --add-host host.docker.internal:host-gateway \
     -v "$PWD/config.alloy:/etc/alloy/config.alloy:ro" \
     -v "$PWD/logs:/sim/logs" \
     grafana/alloy:v1.18.1 run /etc/alloy/config.alloy \
       --storage.path=/tmp/alloy --server.http.listen-addr=0.0.0.0:12345 --disable-reporting
   ```

   Wait ~20s (the WAL replay takes about 6s before the first remote_write batch), then take the
   second `remote_write` body, the first `loki_push` body and the second `otlp_metrics` body.

## When these must be regenerated

On an Alloy bump (`ALLOY_VERSION` in `deploy/versions.env`). The specs assert decoded content,
so a change in the wire format shows up as a decode failure or a missing label rather than as a
silently empty capture in production.
