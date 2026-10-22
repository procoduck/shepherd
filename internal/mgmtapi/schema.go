package mgmtapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"shepherd/internal/schema"
)

// SchemaHandler serves the Alloy component schema artifact merged with the overlay.
//
// The artifact is immutable per version (~680 KB of JSON, ~50 KB gzipped), so
// the handler encodes it once per version — identity and gzip — and serves the
// cached bytes from then on (#279). Each representation carries its own strong
// ETag (a strong validator names exactly one byte sequence, so the gzip body
// cannot share the identity body's tag), and a matching If-None-Match is
// answered with 304. Brotli is not offered: it would need a new dependency.
type SchemaHandler struct {
	registry *schema.Registry

	mu      sync.Mutex
	encoded map[string]*encodedSchema // keyed by the registry ETag of the merged document
}

// encodedSchema is one schema version's cached wire representations.
type encodedSchema struct {
	identity     []byte
	identityETag string
	gzip         []byte
	gzipETag     string
}

// NewSchemaHandler creates a SchemaHandler backed by the given registry.
func NewSchemaHandler(reg *schema.Registry) *SchemaHandler {
	return &SchemaHandler{registry: reg, encoded: make(map[string]*encodedSchema)}
}

// Get handles GET /api/schema/{version}.
// Returns the schema artifact deep-merged with the overlay for the requested version.
// The response includes an ETag based on the content hash; clients should cache per version.
// Accessible to any authenticated user (viewer role).
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

	enc, err := h.encode(etag, merged)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal_error", "failed to encode schema")
		return
	}
	writeSchema(w, r, enc, "public, max-age=86400, immutable")
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

	enc, err := h.encode(etag, merged)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal_error", "failed to encode schema")
		return
	}
	// NOT immutable: unlike /api/schema/{version}, which is content-addressed by a
	// version that never changes meaning, "current" is a MOVING POINTER. Marking it
	// immutable told browsers not to revalidate for a day, so after a schema
	// regeneration or a server upgrade an open tab kept serving the old component
	// model — ports lose their names, every stored edge stops resolving, and the
	// canvas silently drops all its wires. Revalidate every time; the ETag
	// still makes the common case a cheap 304.
	writeSchema(w, r, enc, "no-cache")
}

// encode returns the cached wire representations for the merged document whose
// content hash is etag, encoding them on first use.
func (h *SchemaHandler) encode(etag string, merged map[string]any) (*encodedSchema, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if enc, ok := h.encoded[etag]; ok {
		return enc, nil
	}

	identity, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("schema: marshal: %w", err)
	}
	identity = append(identity, '\n')

	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("schema: gzip writer: %w", err)
	}
	if _, err := zw.Write(identity); err != nil {
		return nil, fmt.Errorf("schema: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("schema: gzip close: %w", err)
	}

	// The registry's ETag is already a quoted strong tag ("<hex>"); the gzip
	// representation gets a distinct tag derived from it.
	enc := &encodedSchema{
		identity:     identity,
		identityETag: etag,
		gzip:         buf.Bytes(),
		gzipETag:     strings.TrimSuffix(etag, `"`) + `-gzip"`,
	}
	h.encoded[etag] = enc
	return enc, nil
}

// writeSchema answers with the representation the client accepts, or 304 when
// If-None-Match already names the representation it would send.
func writeSchema(w http.ResponseWriter, r *http.Request, enc *encodedSchema, cacheControl string) {
	body, tag, encoding := enc.identity, enc.identityETag, ""
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body, tag, encoding = enc.gzip, enc.gzipETag, "gzip"
	}

	hdr := w.Header()
	hdr.Set("Vary", "Accept-Encoding")
	hdr.Set("ETag", tag)
	hdr.Set("Cache-Control", cacheControl)

	// 304 only when the client holds the representation it would get now: a
	// client with the cached identity body that now accepts gzip receives the
	// gzip body, not a 304 naming bytes it never received.
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagListMatches(inm, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	hdr.Set("Content-Type", "application/json")
	if encoding != "" {
		hdr.Set("Content-Encoding", encoding)
	}
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:errcheck,gosec // headers already sent; body is the embedded schema artifact served as application/json, never request input
}

// acceptsGzip reports whether an Accept-Encoding header admits gzip with a
// non-zero quality. An explicitly named gzip (or x-gzip) decides on its own
// qvalue, whatever "*" says (RFC 9110 §12.5.3); "*" decides only when gzip is
// not named.
func acceptsGzip(header string) bool {
	named, namedOK := false, false
	wildcard, wildcardOK := false, false
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		q, hasQ := qualityParam(params)
		admits := !hasQ || q > 0
		switch coding {
		case "gzip", "x-gzip":
			namedOK = true
			named = named || admits
		case "*":
			wildcardOK = true
			wildcard = wildcard || admits
		}
	}
	if namedOK {
		return named
	}
	return wildcardOK && wildcard
}

// qualityParam extracts the q= value of an Accept-Encoding member's parameters.
func qualityParam(params string) (float64, bool) {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return q, true
	}
	return 0, false
}

// etagListMatches applies If-None-Match's weak comparison (RFC 9110 §13.1.2):
// "*" matches, and a W/ prefix is ignored. tag is the selected
// representation's ETag.
func etagListMatches(header, tag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == "*" || candidate == tag {
			return true
		}
	}
	return false
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
