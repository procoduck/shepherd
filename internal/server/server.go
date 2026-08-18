// Package server assembles the HTTP server with all routes.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // h2c is still required for Connect/gRPC cleartext

	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/mgmtapi"
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
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", handleReadyz)
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
	})

	// Guard: reserved prefixes that didn't match any real route return 404 JSON, never the SPA.
	// Guard: unmatched /auth/*, /collector.v1.*, and /metrics paths return 404 JSON.
	// /api/* is handled by the mgmtapi sub-router's NotFound handler instead
	// (a root-level wildcard would compete with the r.Mount above).
	r.Handle("/auth/*", apiGuard)
	r.Handle("/collector.v1.*", apiGuard)
	r.Handle("/metrics", apiGuard) // metrics moved to separate listener (V4-4)

	// TODO milestone 6: serve embedded SPA
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
	}, nil
}

// Run starts the server and blocks until ctx is cancelled, then gracefully shuts down.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("starting server", "addr", s.cfg.Server.Listen, "metrics_addr", s.cfg.Server.MetricsListen)

	// Start the lifecycle sweeper.
	sweeper := agentapi.NewSweeper(s.store, &s.cfg.Agent, s.logger)
	sweeper.Start(ctx)

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
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok")) //nolint:errcheck // health endpoint write
}

func handleReadyz(w http.ResponseWriter, _ *http.Request) {
	// TODO milestone 2+: check DB connectivity + pending-migration state.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok")) //nolint:errcheck // health endpoint write
}
