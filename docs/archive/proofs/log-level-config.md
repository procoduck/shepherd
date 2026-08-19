# Logging level and format configuration proof

Shepherd defaults to JSON logs at `info` level. The `SHEPHERD_LOG_LEVEL` and
`SHEPHERD_LOG_FORMAT` environment variables are loaded by the configuration
loader, while `serve --log-level` and `serve --log-format` override the loaded
values. Supported levels are `debug`, `info`, `warn`, and `error`; supported
formats are `json` and `text`. Invalid values stop server startup with an
explicit error.

The e2e Shepherd service sets `SHEPHERD_LOG_LEVEL=debug` so request diagnostics
are available in compose logs.
