// Package database implements the "database" wizard: scrapes a
// Postgres/MySQL/Redis exporter and forwards the resulting metrics to a
// Prometheus destination. One of the five catalog wizards
// docs/gateway-tier-plan.md W8 asks for.
//
// This wizard always targets role=metrics (see Role): every exporter it can
// select (prometheus.exporter.postgres/mysql/redis) speaks only the
// prom.metrics wire type via its "targets" plumbing output, and
// prometheus.scrape/prometheus.remote_write are likewise metrics-only.
// wizard.Register still checks that claim against the actual generated
// output on every commit (internal/wizard/role.go).
//
// The connection credential is never accepted as wizard state. By default
// (credential_source=kubernetes_secret) the wizard takes a Kubernetes
// Secret's namespace, name and key, and the collector reads the value itself
// at load time through `remote.kubernetes.secret` — the same contract
// destination auth uses (#229/#260, wizard.RenderWriter): Shepherd stores
// only the reference and the key name. The value is passed straight into a
// secret-typed exporter attribute (postgres data_source_names, mysql
// data_source_name, redis redis_password), so it is never converted to a
// plain string.
//
// credential_source=env renders sys.env("<NAME>") and is only correct when
// the operator sets that variable on the collector themselves — Shepherd
// sets nothing. Before the Secret option existed every database pipeline
// rendered sys.env and the help text claimed Shepherd injected it; nothing
// did, the exporter then failed to parse an empty DSN, and the collector
// refused its whole config. A stored state from that era (connection_env
// set, no credential_source) still renders the same text, so a collector
// whose operator did set the variable keeps working; re-running the wizard
// moves it to a Secret.
package database

import (
	"fmt"
	"regexp"
	"strings"

	"shepherd/internal/wizard"
)

// Kind identifies the database wizard.
const Kind = "database"

// role is the fixed collector role this wizard's output is always checked
// against.
const role = "metrics"

// The credential_source values.
const (
	sourceSecret = "kubernetes_secret" //nolint:gosec // G101: a credential_source enum value, not a credential
	sourceEnv    = "env"
	sourceNone   = "none"
)

// secretLabel is the remote.kubernetes.secret component label the
// connection credential is read through. Distinct from the writer's
// "<label>_auth" Secret (wizard.RenderWriter).
const secretLabel = "db_credentials"

func init() {
	wizard.Register(&Wizard{})
}

// Wizard generates a database exporter metrics pipeline.
type Wizard struct{}

// Kind returns the wizard kind identifier.
func (w *Wizard) Kind() string { return Kind }

// Role always returns "metrics": every engine this wizard can select emits
// only prom.metrics-wire components, regardless of state.
func (w *Wizard) Role(map[string]any) string { return role }

// engines maps the "engine" step field's allowed values to the schema
// component that implements it. Checked against the pinned schema artifact
// by schema_conformance_test.go, not assumed.
var engines = map[string]string{
	"postgres": "prometheus.exporter.postgres",
	"mysql":    "prometheus.exporter.mysql",
	"redis":    "prometheus.exporter.redis",
}

// defaultDataKey is the Secret key read when secret_key is left empty: a DSN
// for postgres/mysql, the password for redis (whose address is a plain
// answer, redis_addr — redis_exporter's address attribute is not
// secret-typed, so a password must not travel inside it).
//
// CodeQL's sensitive-data heuristic treats any identifier whose name says
// "password" as a password source and follows it into every hash of the
// rendered config (go/weak-sensitive-data-hashing), although this is a Secret
// key NAME, never a value. So the redis key is a local constant named for
// what it is (keyRedisAuth), equal to wizard.SecretKeyPassword — pinned by
// TestDefaultDataKeys.
func defaultDataKey(engine string) string {
	switch engine {
	case "redis":
		return keyRedisAuth
	default:
		return keyDSN
	}
}

// keyDSN is the default Secret key holding a postgres/mysql DSN; keyRedisAuth
// the one holding redis' AUTH secret (the same key name destination basic auth
// uses).
const (
	keyDSN       = "dsn"
	keyRedisAuth = "password"
)

// secretValue describes, per engine, what the Secret key must hold.
var secretValue = map[string]string{ //nolint:gosec // G101: placeholder examples in help text, not credentials
	"postgres": "a PostgreSQL connection URL, e.g. postgresql://user:password@db.example.com:5432/postgres?sslmode=require",
	"mysql":    "a MySQL DSN, e.g. user:password@tcp(db.example.com:3306)/",
	"redis":    "the Redis password",
}

// envNameRE is a POSIX environment variable name.
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Schema returns the wizard's input schema.
func (w *Wizard) Schema() wizard.Schema {
	return wizard.Schema{
		Kind:        Kind,
		Title:       "Database Metrics",
		Description: "Collect metrics from a PostgreSQL, MySQL or Redis instance.",
		Steps: []wizard.Step{
			{
				ID:    "engine",
				Title: "Database engine",
				Fields: []wizard.StepField{
					{
						Name: "engine", Label: "Engine", Type: "select", Required: true,
						Options: []string{"postgres", "mysql", "redis"},
					},
					{Name: "job_name", Label: "Job label", Type: "text", Default: "database"},
					{Name: "scrape_interval", Label: "Scrape interval", Type: "text", Default: "60s"},
				},
			},
			{
				ID:    "connection",
				Title: "Connection",
				Fields: []wizard.StepField{
					{
						Name: "credential_source", Label: "Credential source", Type: "select", Required: true,
						Options: []string{sourceSecret, sourceEnv, sourceNone},
						Default: sourceSecret,
						Description: "kubernetes_secret: the collector reads the credential from a Kubernetes Secret " +
							"on its own cluster — Shepherd stores only the Secret's namespace, name and key, " +
							"never the value; only for collectors running in Kubernetes (one on a host or VM " +
							"refuses its whole config). env: the collector reads an environment variable that YOU must " +
							"set on every matching collector — Shepherd does not set it. none: Redis without " +
							"a password only.",
					},
					{
						Name: "secret_namespace", Label: "Secret namespace", Type: "text",
						Placeholder: "monitoring",
						Description: "kubernetes_secret only. Namespace of the Secret on the collector's cluster. " +
							"The collector's service account needs get on Secrets here (it re-reads the Secret " +
							"with a GET every minute).",
					},
					{
						Name: "secret_name", Label: "Secret name", Type: "text",
						Placeholder: "app-db-credentials",
						Description: "kubernetes_secret only. It must exist on every cluster this pipeline matches.",
					},
					{
						Name: "secret_key", Label: "Secret key", Type: "text",
						Placeholder: "dsn",
						Description: "kubernetes_secret only. The key holding the credential: for postgres and mysql " +
							"the full DSN (default key dsn), for redis the password (default key password). " +
							"If a redis Secret lacks the key, the collector connects without a password.",
					},
					{
						Name: "redis_addr", Label: "Redis address", Type: "text",
						Placeholder: "redis.example.com:6379",
						Description: "redis only, with kubernetes_secret or none: host:port or redis://host:port, " +
							"without credentials or a query string (the password comes from the Secret).",
					},
					{
						Name: "connection_env", Label: "Environment variable name", Type: "text",
						Placeholder: "MYAPP_DB_DSN",
						Description: "env only. You must set this variable on every matching collector yourself " +
							"(e.g. in the Alloy Deployment's env) — Shepherd does not. It holds the DSN " +
							"(redis: the address). If it is unset or invalid, the collector refuses its whole " +
							"config, not only this pipeline.",
					},
				},
			},
			{
				ID:    "destinations",
				Title: "Destinations",
				Fields: []wizard.StepField{
					{
						Name: "metrics_dest_name", Label: "Metrics destination", Type: "text", Required: true,
						Description: "Name of a Prometheus-type destination in this org.",
					},
				},
			},
			{
				ID:    "matchers",
				Title: "Collector matching",
				Fields: []wizard.StepField{
					{
						Name: "cluster_pattern", Label: "Cluster pattern (regex)", Type: "text",
						Placeholder: "prod-.*", Description: "Applies this pipeline to clusters matching the regex.",
					},
				},
			},
		},
	}
}

// Commit generates an Alloy pipeline from the wizard state.
func (w *Wizard) Commit(state map[string]any, dests wizard.Destinations) (wizard.CommitResult, error) {
	get := func(key string) string {
		v, _ := state[key].(string) //nolint:errcheck // type assert ok flag; empty string is safe default
		return v
	}

	engine := get("engine")
	if _, ok := engines[engine]; !ok {
		return wizard.CommitResult{}, fmt.Errorf(
			"engine %q is not supported, want one of: postgres|mysql|redis", engine)
	}
	metricsDest := get("metrics_dest_name")
	if metricsDest == "" {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name is required")
	}
	jobName := get("job_name")
	if jobName == "" {
		jobName = "database"
	}
	scrapeInterval := get("scrape_interval")
	if scrapeInterval == "" {
		scrapeInterval = "60s"
	}

	exporter, warnings, err := renderExporter(engine, get)
	if err != nil {
		return wizard.CommitResult{}, err
	}

	var sb strings.Builder
	_, _ = sb.WriteString(exporter)
	_, _ = fmt.Fprintf(&sb, `
prometheus.scrape "database" {
  targets         = prometheus.exporter.%s.db.targets
  forward_to      = [prometheus.remote_write.metrics.receiver]
  scrape_interval = %s
  job_name        = %s
}
`, engine, wizard.Quote(scrapeInterval), wizard.Quote(jobName))

	writer, err := wizard.RenderWriter(wizard.WriterPrometheus, "metrics", dests, metricsDest)
	if err != nil {
		return wizard.CommitResult{}, fmt.Errorf("metrics_dest_name: %w", err)
	}
	_, _ = sb.WriteString("\n" + writer)

	var matchers []string
	if cp := get("cluster_pattern"); cp != "" {
		matchers = append(matchers, fmt.Sprintf(`cluster=~%q`, cp))
	}
	matchers = append(matchers, fmt.Sprintf(`role=%q`, role))

	return wizard.CommitResult{
		Contents: sb.String(),
		Matchers: matchers,
		Warnings: warnings,
	}, nil
}

// credentialSource resolves credential_source, defaulting a state stored
// before the field existed (connection_env set, no source) to env so it
// renders exactly what it rendered then.
func credentialSource(get func(string) string) string {
	if s := get("credential_source"); s != "" {
		return s
	}
	if get("connection_env") != "" {
		return sourceEnv
	}
	return sourceSecret
}

// renderExporter renders the engine's exporter block — preceded, for a
// Secret source, by the remote.kubernetes.secret it reads — plus the
// warnings the Review step shows about what the collector needs.
func renderExporter(engine string, get func(string) string) (string, []string, error) {
	switch source := credentialSource(get); source {
	case sourceSecret:
		return renderSecretExporter(engine, get)
	case sourceEnv:
		return renderEnvExporter(engine, get("connection_env"), get("credential_source") == "")
	case sourceNone:
		if engine != "redis" {
			return "", nil, fmt.Errorf("credential_source none is only for redis: %s needs a DSN — use kubernetes_secret", engine)
		}
		addr, err := redisAddr(get("redis_addr"))
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("prometheus.exporter.redis \"db\" {\n  redis_addr = %s\n}\n", wizard.Quote(addr)), nil, nil
	default:
		return "", nil, fmt.Errorf("credential_source %q is not supported, want one of: %s|%s|%s",
			source, sourceSecret, sourceEnv, sourceNone)
	}
}

func renderSecretExporter(engine string, get func(string) string) (string, []string, error) {
	namespace, name := get("secret_namespace"), get("secret_name")
	if err := wizard.ValidateSecretName(namespace, name); err != nil {
		return "", nil, fmt.Errorf("credential_source %s needs %w", sourceSecret, err)
	}
	key := get("secret_key")
	if key == "" {
		key = defaultDataKey(engine)
	}
	if err := wizard.ValidateSecretKey(key); err != nil {
		return "", nil, fmt.Errorf("secret_key: %w", err)
	}

	value := fmt.Sprintf("remote.kubernetes.secret.%s.data[%s]", secretLabel, wizard.Quote(key))
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, `remote.kubernetes.secret %s {
  namespace = %s
  name      = %s
}

`, wizard.Quote(secretLabel), wizard.Quote(namespace), wizard.Quote(name))

	// Every attribute the Secret value lands in is secret-typed in the
	// pinned schema (schema_conformance_test.go), so it is passed as is —
	// never through convert.nonsensitive.
	switch engine {
	case "postgres":
		_, _ = fmt.Fprintf(&sb, "prometheus.exporter.postgres \"db\" {\n  data_source_names = [%s]\n}\n", value)
	case "mysql":
		_, _ = fmt.Fprintf(&sb, "prometheus.exporter.mysql \"db\" {\n  data_source_name = %s\n}\n", value)
	case "redis":
		addr, err := redisAddr(get("redis_addr"))
		if err != nil {
			return "", nil, err
		}
		_, _ = fmt.Fprintf(&sb, "prometheus.exporter.redis \"db\" {\n  redis_addr     = %s\n  redis_password = %s\n}\n",
			wizard.Quote(addr), value)
	}

	warnings := []string{
		fmt.Sprintf("Each matching collector reads key %q of Secret %s/%s itself when it loads this config, and "+
			"re-reads it with a GET every minute; the key must hold %s. The Secret must exist in namespace %s on "+
			"every cluster this pipeline matches, and the collector's service account needs get on Secrets in %s "+
			"(the get/list/watch Role on the Destinations page covers it). If it cannot read the Secret, the "+
			"collector refuses its whole config — every pipeline on it stops getting updates.",
			key, namespace, name, secretValue[engine], namespace, namespace),
		"Every collector this pipeline matches must run in Kubernetes: a collector on a host or VM cannot read a " +
			"Kubernetes Secret and refuses its whole config. Narrow the matchers to Kubernetes clusters, or use " +
			"credential_source env for those collectors.",
	}
	if engine == "redis" {
		warnings = append(warnings, fmt.Sprintf("If Secret %s/%s has no key %q, the collector still loads and "+
			"connects to Redis without a password — check the key name.", namespace, name, key))
	}
	return sb.String(), warnings, nil
}

// renderEnvExporter renders the sys.env form. Its text is unchanged from
// before credential_source existed, so stored pipelines from that era
// regenerate byte-for-byte.
//
// legacy is a state stored before credential_source existed. Those were
// never checked, so they keep any name that cannot break out of the quoted
// string — refusing, say, APP-PG-DSN now would make every destination such
// a pipeline names refuse its next update (a destination save regenerates
// its pipelines). A newly chosen env source must be a POSIX name.
func renderEnvExporter(engine, connEnv string, legacy bool) (string, []string, error) {
	if connEnv == "" {
		return "", nil, fmt.Errorf("credential_source %s needs connection_env, the variable's name", sourceEnv)
	}
	if legacy {
		if strings.ContainsFunc(connEnv, func(r rune) bool { return r == '"' || r == '\\' || r < 0x20 || r == 0x7f }) {
			return "", nil, fmt.Errorf("connection_env %q contains a quote, backslash or control character", connEnv)
		}
	} else if !envNameRE.MatchString(connEnv) {
		return "", nil, fmt.Errorf("connection_env %q is not an environment variable name", connEnv)
	}
	// connEnv is spliced in raw, as before credential_source existed, so a
	// legacy name renders the same bytes it always did; both checks above
	// rule out anything that could end the string literal.
	var out string
	switch engine {
	case "postgres":
		out = "prometheus.exporter.postgres \"db\" {\n  data_source_names = [sys.env(\"" + connEnv + "\")]\n}\n"
	case "mysql":
		out = "prometheus.exporter.mysql \"db\" {\n  data_source_name = sys.env(\"" + connEnv + "\")\n}\n"
	case "redis":
		out = "prometheus.exporter.redis \"db\" {\n  redis_addr = sys.env(\"" + connEnv + "\")\n}\n"
	}
	warning := fmt.Sprintf("Shepherd does not set %s. You must set it on every collector this pipeline matches "+
		"yourself (for example in the Alloy Deployment's env). If it is unset or not a valid %s, the collector "+
		"refuses its whole config — every pipeline on it stops getting updates. Prefer credential_source "+
		"kubernetes_secret on Kubernetes.", connEnv, map[string]string{
		"postgres": "PostgreSQL DSN", "mysql": "MySQL DSN", "redis": "Redis address",
	}[engine])
	return out, []string{warning}, nil
}

// redisAddr checks the plain-text Redis address: required, and free of
// credentials — a password belongs in the Secret.
func redisAddr(addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("redis_addr is required for redis (host:port or redis:// URL)")
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return "", fmt.Errorf("redis_addr %q must not contain whitespace", addr)
	}
	if strings.Contains(addr, "@") || strings.Contains(strings.ToLower(addr), "password=") {
		return "", fmt.Errorf("redis_addr must not carry credentials — put the password in the Secret (secret_key)")
	}
	if strings.Contains(addr, "?") {
		return "", fmt.Errorf("redis_addr %q must not carry a query string — host:port or redis://host:port only", addr)
	}
	return addr, nil
}
