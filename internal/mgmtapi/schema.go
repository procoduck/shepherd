package mgmtapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"shepherd/internal/schema"
)

// SchemaHandler serves the Alloy component schema artifact merged with the overlay.
type SchemaHandler struct {
	registry *schema.Registry
}

// NewSchemaHandler creates a SchemaHandler backed by the given registry.
func NewSchemaHandler(reg *schema.Registry) *SchemaHandler {
	return &SchemaHandler{registry: reg}
}

// Get handles GET /api/schema/{version}.
// Returns the schema artifact deep-merged with the overlay for the requested version.
// The response includes an ETag based on the content hash; clients should cache per version.
// Accessible to any authenticated user (reader role).
func (h *SchemaHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		respondError(w, http.StatusServiceUnavailable, "unavailable", "schema registry not initialized")
		return
	}
	version := chi.URLParam(r, "version")
	if version == "" {
		respondError(w, http.StatusBadRequest, "bad_request", "version is required")
		return
	}
	// Sanitize: only allow version strings matching alloy-v<semver> or "current".
	if !isValidSchemaVersion(version) {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid schema version format")
		return
	}

	merged, etag, err := h.registry.Get(version)
	if err != nil {
		if schema.IsNotFound(err) {
			respondError(w, http.StatusNotFound, "not_found", fmt.Sprintf("schema %q not found", version))
			return
		}
		respondError(w, http.StatusInternalServerError, "internal_error", "failed to load schema")
		return
	}

	// ETag-based conditional GET.
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		return // headers already sent; nothing more to do
	}
}

// GetCurrent handles GET /api/schema/current — returns the artifact for the fleet-pinned version.
func (h *SchemaHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		respondError(w, http.StatusServiceUnavailable, "unavailable", "schema registry not initialized")
		return
	}
	version := h.registry.CurrentVersion()
	if version == "" {
		respondError(w, http.StatusServiceUnavailable, "unavailable", "no schema available")
		return
	}

	merged, etag, err := h.registry.Get(version)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal_error", "failed to load schema")
		return
	}

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		return // headers already sent; nothing more to do
	}
}

// isValidSchemaVersion returns true for strings matching "alloy-v<semver>" or "current".
func isValidSchemaVersion(v string) bool {
	if v == "current" {
		return true
	}
	if !strings.HasPrefix(v, "alloy-v") {
		return false
	}
	rest := strings.TrimPrefix(v, "alloy-v")
	if len(rest) == 0 {
		return false
	}
	for _, c := range rest {
		if !isVersionChar(c) {
			return false
		}
	}
	return true
}

func isVersionChar(c rune) bool {
	return (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || (c >= 'a' && c <= 'z')
}
