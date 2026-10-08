package database_test

// goldenCase is one committed golden and the wizard state that renders it.
// Shared by wizard_test.go (which checks Commit against the golden) and
// gen_goldens_test.go (which writes it), so the two cannot drift.
type goldenCase struct {
	name  string
	state map[string]any
}

// goldenCases covers every engine with the default Kubernetes Secret source,
// a non-default Secret key, Redis without a password, the explicit env
// source, a state stored before credential_source existed (rendered as env,
// byte-for-byte what it rendered then), and a Secret-auth destination.
var goldenCases = []goldenCase{
	{"postgres", map[string]any{
		"engine":            "postgres",
		"credential_source": "kubernetes_secret",
		"secret_namespace":  "databases",
		"secret_name":       "app-pg",
		"job_name":          "app-db",
		"scrape_interval":   "60s",
		"metrics_dest_name": "prom-prod",
		"cluster_pattern":   "prod-.*",
	}},
	{"mysql", map[string]any{
		"engine":            "mysql",
		"credential_source": "kubernetes_secret",
		"secret_namespace":  "databases",
		"secret_name":       "app-mysql",
		"secret_key":        "mysql-dsn",
		"job_name":          "app-db",
		"scrape_interval":   "30s",
		"metrics_dest_name": "prom-staging",
		"cluster_pattern":   "staging-.*",
	}},
	{"redis", map[string]any{
		"engine":            "redis",
		"credential_source": "kubernetes_secret",
		"secret_namespace":  "databases",
		"secret_name":       "app-redis",
		"redis_addr":        "redis.example.com:6379",
		"job_name":          "app-cache",
		"scrape_interval":   "15s",
		"metrics_dest_name": "prom-prod",
		"cluster_pattern":   "prod-.*",
	}},
	{"redis-no-password", map[string]any{
		"engine":            "redis",
		"credential_source": "none",
		"redis_addr":        "redis.example.com:6379",
		"job_name":          "app-cache",
		"metrics_dest_name": "prom-prod",
	}},
	{"env-mysql", map[string]any{
		"engine":            "mysql",
		"credential_source": "env",
		"connection_env":    "APP_MYSQL_DSN",
		"job_name":          "app-db",
		"scrape_interval":   "30s",
		"metrics_dest_name": "prom-staging",
		"cluster_pattern":   "staging-.*",
	}},
	{"env-redis", map[string]any{
		"engine":            "redis",
		"credential_source": "env",
		"connection_env":    "APP_REDIS_ADDR",
		"job_name":          "app-cache",
		"scrape_interval":   "15s",
		"metrics_dest_name": "prom-prod",
	}},
	// The exact state the wizard stored before credential_source existed.
	{"legacy-env-postgres", map[string]any{
		"engine":            "postgres",
		"connection_env":    "APP_PG_DSN",
		"job_name":          "app-db",
		"scrape_interval":   "60s",
		"metrics_dest_name": "prom-prod",
		"cluster_pattern":   "prod-.*",
	}},
	// A basic_secret destination (#229): writer auth read from a spoke
	// Secret, alongside the DSN's own Secret.
	{"secret-auth", map[string]any{
		"engine":            "postgres",
		"credential_source": "kubernetes_secret",
		"secret_namespace":  "databases",
		"secret_name":       "app-pg",
		"job_name":          "app-db",
		"scrape_interval":   "60s",
		"metrics_dest_name": "prom-basic",
		"cluster_pattern":   "prod-.*",
	}},
}
