package simsvc

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// APIError is the control API's error body.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Server is the control API Shepherd drives. It is internal-only: it carries no
// session, no org and no user, and its sole authentication is the shared bearer
// token, because the network posture (an internal-only network / a NetworkPolicy
// that admits only the Shepherd pod) is what actually gates access.
type Server struct {
	cfg    Config
	queue  *Queue
	logger *slog.Logger
	ready  func() error
}

// NewServer builds the control API. ready is the readiness probe — it must
// assert the things a run needs (an executable Alloy, bound capture listeners),
// so a simulator that would fail every run never reports itself ready.
func NewServer(cfg Config, q *Queue, ready func() error, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if ready == nil {
		ready = func() error { return nil }
	}
	return &Server{cfg: cfg, queue: q, ready: ready, logger: logger}
}

// Handler returns the control API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // probe response; a write failure is the client's problem
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := s.ready(); err != nil {
			s.writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready")) //nolint:errcheck // probe response
	})
	mux.Handle("POST /v1/runs", s.authenticated(http.HandlerFunc(s.startRun)))
	mux.Handle("GET /v1/runs/{id}", s.authenticated(http.HandlerFunc(s.getRun)))
	mux.Handle("DELETE /v1/runs/{id}", s.authenticated(http.HandlerFunc(s.cancelRun)))
	return mux
}

// authenticated enforces the bearer token when one is configured. The compare
// is constant-time so a token cannot be recovered a byte at a time from
// response latency.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.Token)) != 1 {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	// MaxBytesReader rather than a length check: a chunked body has no
	// Content-Length to check, and reading it whole first is the thing the cap
	// exists to prevent.
	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxConfigBytes)
	raw, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "config_too_large", "config exceeds the simulator's size limit")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_request", "could not read request body")
		return
	}
	var req StartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return
	}
	if strings.TrimSpace(req.Config) == "" {
		s.writeError(w, http.StatusBadRequest, "invalid_config", "config is empty")
		return
	}
	if err := CheckEndpoints(req.Config, s.cfg.AllowedHosts); err != nil {
		code := "invalid_config"
		if errors.Is(err, ErrEndpointNotAllowed) {
			code = "endpoint_not_allowed"
		}
		// The message names only the offending host, never the config text:
		// this service logs and returns no user configuration.
		s.writeError(w, http.StatusBadRequest, code, err.Error())
		return
	}

	// Submit deliberately does not take the request context: the run outlives
	// this HTTP exchange by design (§6.4 — the client polls), so binding it to
	// the request would cancel every run the moment its POST returned.
	run, err := s.queue.Submit(req) //nolint:contextcheck // a run outlives the request that started it
	switch {
	case errors.Is(err, ErrQueueFull):
		w.Header().Set("Retry-After", "10")
		s.writeError(w, http.StatusTooManyRequests, "queue_full", "the simulator's run backlog is full")
		return
	case errors.Is(err, ErrShuttingDown):
		s.writeError(w, http.StatusServiceUnavailable, "shutting_down", "the simulator is shutting down")
		return
	case err != nil:
		s.writeError(w, http.StatusInternalServerError, "internal", "could not accept the run")
		return
	}
	s.writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.queue.Get(r.PathValue("id"))
	if errors.Is(err, ErrRunNotFound) {
		s.writeError(w, http.StatusNotFound, "run_not_found", "no such run")
		return
	}
	s.writeJSON(w, http.StatusOK, runView{Run: run, RemainingSeconds: remainingSeconds(run)})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.queue.Cancel(r.PathValue("id")); errors.Is(err, ErrRunNotFound) {
		s.writeError(w, http.StatusNotFound, "run_not_found", "no such run")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runView is the poll response: a run plus the countdown §6.4 step 4's
// "running 30s countdown" state needs, which the client cannot compute without
// knowing the server's clock.
type runView struct {
	Run
	RemainingSeconds int `json:"remaining_seconds"`
}

func remainingSeconds(run Run) int {
	if terminal(run.State) {
		return 0
	}
	remaining := int(time.Until(run.ExpiresAt) / time.Second)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Warn("control api: encode response failed", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, APIError{Code: code, Message: message})
}
