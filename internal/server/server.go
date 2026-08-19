// Package server assembles the HTTP server with all routes.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // h2c is still required for Connect/gRPC cleartext

	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitsync"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/migrations"
	"shepherd/internal/spa"
	"shepherd/internal/store"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// Server is the main HTTP server.
type Server struct {
	cfg         *config.Config
	logger      *slog.Logger
	http        *http.Server
	metricsHTTP *http.Server
	store       *store.Store
	enc         *crypto.Encryptor
	validator   *validate.Validator
}

type ctxKeyLogger struct{}

// LoggerFromCtx returns the request-scoped logger, or the default logger.
func LoggerFromCtx(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(ctxKeyLogger{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			reqLogger := logger.With("request_id", middleware.GetReqID(r.Context()))
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyLogger{}, reqLogger))
			next.ServeHTTP(ww, r)
			reqLogger.Debug("request", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// New creates a new Server. Management API and SPA will be added in subsequent milestones.
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	spaInfo, err := spa.ParseBuildInfo()
	if err != nil {
		logger.Warn("failed to parse embedded SPA build info", "err", err)
	}
	logger.Info("embedded SPA", "sha", spaInfo.GitSha, "built_at", spaInfo.BuiltAt)
	if !spaInfo.Dirty && spaInfo.GitSha != "dev" && spaInfo.GitSha != "" && version.Commit != "dev" && version.Commit != "" {
		if !strings.HasPrefix(version.Commit, spaInfo.GitSha) && spaInfo.GitSha != version.Commit {
			logger.Warn("embedded SPA is stale", "spa_sha", spaInfo.GitSha, "binary_sha", version.Commit)
		}
	}

	st, err := store.New(context.Background(), &cfg.Database, logger)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(logger))
	r.Use(middleware.RealIP) //nolint:staticcheck // RealIP deprecated but no safe replacement available yet
	r.Use(middleware.Recoverer)

	// Health endpoints — no auth, no h2c wrapping required.
	latestMigration, err := latestMigrationVersion()
	if err != nil {
		logger.Warn("failed to determine latest embedded migration version; readiness will not detect pending migrations", "err", err)
	}
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", handleReadyz(st.Pool(), latestMigration))
	apiGuard := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`)) //nolint:errcheck // error response write
	})
	r.Get("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // response write
			"binary_sha":   version.Commit,
			"spa_sha":      spaInfo.GitSha,
			"spa_built_at": spaInfo.BuiltAt,
		})
	})

	// Agent API — Connect RPC over h2c so both HTTP/1.1+JSON and HTTP/2+proto work.
	v := validate.New(&cfg.Validate)
	svc := agentapi.New(st, v, logger)
	authInterceptor := agentapi.NewAuthInterceptor(st)
	connectPath, connectHandler := collectorv1connect.NewCollectorServiceHandler(
		svc,
		connect.WithInterceptors(authInterceptor),
	)
	r.Mount(connectPath, connectHandler)

	// Management REST API with session + OIDC auth wiring.
	r.Get("/auth/methods", auth.MethodsHandler(cfg))
	// Auth handler initialization requires OIDC discovery (network call); skip in dev/test when OIDC config is empty.
	// Session and CSRF middleware are applied on a sub-router so they don't conflict
	// with the Connect RPC mount already registered above.
	// Build encryptor for secret-at-rest (may be nil if key not configured yet).
	var enc *crypto.Encryptor
	if cfg.Security.EncryptionKey != "" {
		var encErr error
		enc, encErr = crypto.NewEncryptor(cfg.Security.EncryptionKey)
		if encErr != nil {
			return nil, fmt.Errorf("initializing encryptor: %w", encErr)
		}
	}

	var authHandler *auth.Handler
	if cfg.OIDC.Issuer != "" {
		authHandler, err = auth.New(context.Background(), cfg, st, logger)
		if err != nil {
			return nil, fmt.Errorf("initializing auth: %w", err)
		}
	} else if cfg.Auth.LocalAdmin.Enabled {
		authHandler = auth.NewLocalAdmin(cfg, st, logger)
	}
	if cfg.Auth.LocalAdmin.Enabled {
		logger.Warn("local admin account enabled", "username", cfg.Auth.LocalAdmin.Username, "oidc_also_active", cfg.OIDC.Issuer != "")
	}
	r.Group(func(r chi.Router) {
		// Middleware must be registered before routes (chi requirement).
		if authHandler != nil {
			r.Use(authHandler.SessionMiddleware)
		}
		r.Use(auth.CSRFMiddleware)
		if authHandler != nil {
			if cfg.OIDC.Issuer != "" {
				r.Get("/auth/login", authHandler.LoginHandler)
				r.Get("/auth/callback", authHandler.CallbackHandler)
			}
			// Logout always registered when any auth method is active
			r.Get("/auth/logout", authHandler.LogoutHandler)
			if cfg.Auth.LocalAdmin.Enabled {
				r.Post("/api/auth/local/login", authHandler.LocalLoginHandler)
			}
		}
		r.Mount("/api", mgmtapi.Router(st, cfg, enc, logger))

		// shepherd.mgmt.v1 Connect RPC handlers — the typed contract behind the
		// /api shims above (docs/api-contract-design.md). Mounted in the same
		// group so they get session population + CSRF enforcement; each
		// handler additionally requires its own authz interceptor role.
		mgmtapi.MountRPC(r, st, cfg, enc, logger)
	})

	// Guard: reserved prefixes that didn't match any real route return 404 JSON, never the SPA.
	// Guard: unmatched /auth/*, /collector.v1.*, /shepherd.mgmt.v1.*, and /metrics paths return 404 JSON.
	// /api/* is handled by the mgmtapi sub-router's NotFound handler instead
	// (a root-level wildcard would compete with the r.Mount above).
	r.Handle("/auth/*", apiGuard)
	r.Handle("/collector.v1.*", apiGuard)
	r.Handle("/shepherd.mgmt.v1.*", apiGuard)
	r.Handle("/metrics", apiGuard) // metrics moved to separate listener (V4-4)

	r.Mount("/", spa.Handler())

	// Wrap the entire mux with h2c so agents using HTTP/2 connect without TLS.
	h2cHandler := h2c.NewHandler(r, &http2.Server{}) //nolint:staticcheck // h2c is still required for Connect/gRPC cleartext

	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           h2cHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:              cfg.Server.MetricsListen,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return &Server{
		cfg:         cfg,
		logger:      logger,
		http:        httpSrv,
		metricsHTTP: metricsSrv,
		store:       st,
		enc:         enc,
		validator:   v,
	}, nil
}

// Run starts the server and blocks until ctx is cancelled, then gracefully shuts down.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("starting server", "addr", s.cfg.Server.Listen, "metrics_addr", s.cfg.Server.MetricsListen)

	// Start the lifecycle sweeper.
	sweeper := agentapi.NewSweeper(s.store, &s.cfg.Agent, s.logger)
	sweeper.Start(ctx)

	// Start the GitOps reconciliation loop. Requires the secret-at-rest
	// encryptor to be configured, since repo_link credentials are stored
	// encrypted; without it there is no key to decrypt them with.
	if s.enc != nil {
		reconciler := gitsync.New(s.store, s.enc, s.validator, s.cfg, s.logger)
		reconciler.Start(ctx)
	} else {
		s.logger.Warn("gitops reconciler not started: encryption key not configured")
	}

	errCh := make(chan error, 2)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("main server: %w", err)
		}
	}()
	go func() {
		if err := s.metricsHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		s.logger.Info("shutting down server")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.metricsHTTP.Shutdown(shutCtx)              //nolint:contextcheck,errcheck // best-effort
		if err := s.http.Shutdown(shutCtx); err != nil { //nolint:contextcheck // shutdown needs a fresh context independent of the cancelled run ctx
			return err
		}
		s.store.Close()
		return nil
	}
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	// Cheap liveness check only — no I/O. `shepherd healthcheck` (internal/cli/healthcheck.go)
	// and the Kubernetes liveness probe (docs/spec.md §deployment) both poll this path
	// and expect it to answer without touching the database.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok")) //nolint:errcheck // health endpoint write
}

// dbPinger is the subset of *pgxpool.Pool the readiness check needs, kept as
// an interface so tests can substitute a fake instead of a live database.
type dbPinger interface {
	Ping(ctx context.Context) error
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readyzResult is the JSON body served by /readyz.
type readyzResult struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// migrationFilePattern matches embedded up-migration filenames like "0005_visual_source.up.sql".
var migrationFilePattern = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// latestMigrationVersion returns the highest migration version embedded in the
// binary, derived from the migration filenames themselves so it needs no
// database round trip.
func latestMigrationVersion() (uint64, error) {
	entries, err := migrations.FS.ReadDir("sql")
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations: %w", err)
	}
	var latest uint64
	for _, entry := range entries {
		m := migrationFilePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		if v > latest {
			latest = v
		}
	}
	return latest, nil
}

// handleReadyz builds the /readyz handler. Unlike /healthz, readiness pings
// the database and, best-effort, reports whether the schema is behind the
// migrations embedded in this binary — a pod should not receive traffic
// against a database it can't reach or a schema it doesn't match yet
// (docs/spec.md §deployment). latestMigration is 0 when it could not be
// determined, which disables the pending-migration check without failing
// the handler.
func handleReadyz(db dbPinger, latestMigration uint64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		ready := true
		checks := map[string]string{}

		if err := db.Ping(ctx); err != nil {
			ready = false
			checks["database"] = "error: " + err.Error()
		} else {
			checks["database"] = "ok"

			var current uint64
			var dirty bool
			switch scanErr := db.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&current, &dirty); {
			case scanErr != nil:
				// schema_migrations may not exist yet, or the query may fail
				// transiently — migration state is a best-effort extra and must
				// never flip readiness on its own.
				checks["migrations"] = "unknown"
			case dirty:
				checks["migrations"] = "dirty"
				ready = false
			case latestMigration > 0 && current < latestMigration:
				checks["migrations"] = fmt.Sprintf("pending (at %d, latest %d)", current, latestMigration)
				ready = false
			default:
				checks["migrations"] = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		result := readyzResult{Status: "ok", Checks: checks}
		if !ready {
			status = http.StatusServiceUnavailable
			result.Status = "error"
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(result) //nolint:errcheck // health endpoint write
	}
}
