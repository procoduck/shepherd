// Package schema loads, merges, and serves the Alloy component schema artifact + overlay.
//
// The artifact (schema/alloy-v<X>.json) is committed to the repo and embedded at build time.
// The overlay (schema/overlay.json) is also embedded and deep-merged over the artifact before serving.
//
// GET /api/schema/{version} clients cache per version (ETag: content hash, immutable).
// The text editor's autocomplete now derives from this same payload — no second component list.
package schema

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// FS is satisfied by embed.FS and any fs.FS implementation (useful in tests).
type FS interface {
	fs.FS
}

// Registry holds the loaded schema artifacts indexed by version string.
type Registry struct {
	mu      sync.Mutex
	fsys    FS
	overlay map[string]any
	cache   map[string]mergedEntry
	current string // the fleet-pinned version, e.g. "alloy-v1.18.1"
}

type mergedEntry struct {
	merged map[string]any
	etag   string
}

// ErrNotFound is returned when the requested schema version is not available.
var ErrNotFound = errors.New("schema not found")

// IsNotFound reports whether err is a not-found error.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// New creates a Registry backed by fsys.
// fsys must contain:
//   - schema/overlay.json
//   - schema/alloy-v<X>.json (one or more)
//
// currentVersion is the version returned by GetCurrent, e.g. "alloy-v1.18.1".
func New(fsys FS, currentVersion string) (*Registry, error) {
	r := &Registry{
		fsys:    fsys,
		cache:   make(map[string]mergedEntry),
		current: currentVersion,
	}

	// Load overlay once.
	overlayData, err := fs.ReadFile(fsys, "artifacts/overlay.json")
	if err != nil {
		return nil, fmt.Errorf("schema: read overlay: %w", err)
	}
	if err := json.Unmarshal(overlayData, &r.overlay); err != nil {
		return nil, fmt.Errorf("schema: parse overlay: %w", err)
	}

	return r, nil
}

// CurrentVersion returns the fleet-pinned schema version (e.g. "alloy-v1.18.1").
func (r *Registry) CurrentVersion() string {
	return r.current
}

// Get returns the merged schema for the given version and its ETag.
// The result is cached after first load.
func (r *Registry) Get(version string) (map[string]any, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.cache[version]; ok {
		return entry.merged, entry.etag, nil
	}

	// Load the artifact.
	path := "artifacts/" + version + ".json"
	artifactData, err := fs.ReadFile(r.fsys, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: %s", ErrNotFound, version)
		}
		return nil, "", fmt.Errorf("schema: read artifact %s: %w", version, err)
	}

	var artifact map[string]any
	if err := json.Unmarshal(artifactData, &artifact); err != nil {
		return nil, "", fmt.Errorf("schema: parse artifact %s: %w", version, err)
	}

	merged := deepMerge(artifact, r.overlay)

	// Compute ETag from the merged content.
	// json.Marshal on map[string]any does not fail in practice; if it does, ETag is empty.
	b, _ := json.Marshal(merged) //nolint:errcheck // map[string]any marshal cannot produce an error
	h := sha256.Sum256(b)
	etag := fmt.Sprintf(`"%x"`, h[:16])

	entry := mergedEntry{merged: merged, etag: etag}
	r.cache[version] = entry

	return entry.merged, entry.etag, nil
}

// ValidateOverlay checks that every key in the overlay's "components" map exists
// in the artifact for the current version. Returns a list of violation messages.
func (r *Registry) ValidateOverlay() ([]string, error) {
	artifact, _, err := r.Get(r.current)
	if err != nil {
		return nil, fmt.Errorf("schema: validate overlay: %w", err)
	}

	artifactComponents, _ := artifact["components"].(map[string]any) //nolint:errcheck // safe: deepMerge always produces map[string]any
	overlayComponents, _ := r.overlay["components"].(map[string]any) //nolint:errcheck // safe: overlay is parsed and validated at init

	var violations []string
	for key := range overlayComponents {
		if _, exists := artifactComponents[key]; !exists {
			violations = append(violations, fmt.Sprintf("overlay key %q not found in artifact", key))
		}
	}

	// Check discovery-stub map keys are all discovery.* components in the artifact.
	for key, compOverlay := range overlayComponents {
		comp, ok := compOverlay.(map[string]any)
		if !ok {
			continue
		}
		if _, hasStub := comp["discovery_stub"]; hasStub {
			// discovery_stub is valid on discovery.* components AND loki source components
			// (§6.4: loki.source.kubernetes/file are also stubbed in S3 simulation).
			if !strings.HasPrefix(key, "discovery.") && !strings.HasPrefix(key, "loki.source.") {
				violations = append(violations, fmt.Sprintf("discovery_stub on non-discovery/loki-source component %q", key))
			}
		}
	}

	violations = append(violations, validatePortDisplayOrder(artifactComponents, overlayComponents)...)
	violations = append(violations, validateSimPolicy(artifactComponents, overlayComponents)...)

	return violations, nil
}

// validatePortDisplayOrder checks that a component's overlay port_display_order
// names exactly the ports the artifact declares for it — every port once, and no
// name that does not exist. Without this the overlay rots silently within a
// single Alloy version: a renamed or newly split port leaves the canvas ordering
// pointing at nothing, and no test notices.
func validatePortDisplayOrder(artifactComponents, overlayComponents map[string]any) []string {
	var violations []string
	keys := make([]string, 0, len(overlayComponents))
	for key := range overlayComponents {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		comp, ok := overlayComponents[key].(map[string]any)
		if !ok {
			continue
		}
		rawOrder, ok := comp["port_display_order"].([]any)
		if !ok {
			continue
		}
		artComp, ok := artifactComponents[key].(map[string]any)
		if !ok {
			continue // already reported as an orphaned overlay key
		}

		declared := declaredPortNames(artComp)
		seen := make(map[string]bool, len(rawOrder))
		for _, entry := range rawOrder {
			name, ok := entry.(string)
			if !ok {
				violations = append(violations, fmt.Sprintf("port_display_order on %q contains a non-string entry", key))
				continue
			}
			switch {
			case !declared[name]:
				violations = append(violations, fmt.Sprintf("port_display_order on %q names port %q which the artifact does not declare", key, name))
			case seen[name]:
				violations = append(violations, fmt.Sprintf("port_display_order on %q lists port %q twice", key, name))
			}
			seen[name] = true
		}
		missing := make([]string, 0, len(declared))
		for name := range declared {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		for _, name := range missing {
			violations = append(violations, fmt.Sprintf("port_display_order on %q omits declared port %q", key, name))
		}
	}
	return violations
}

// declaredPortNames returns the set of port ids an artifact component declares:
// "prop" for inputs (Arguments fields) and "export" for outputs (Exports fields).
func declaredPortNames(artComp map[string]any) map[string]bool {
	names := map[string]bool{}
	collect := func(listKey, nameKey string) {
		list, ok := artComp[listKey].([]any)
		if !ok {
			return
		}
		for _, raw := range list {
			port, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := port[nameKey].(string); ok && name != "" {
				names[name] = true
			}
		}
	}
	collect("inputs", "prop")
	collect("outputs", "export")
	return names
}

// Migrations returns the raw migrations map from the overlay (component → entry).
// Returns nil if the overlay contains no migrations key.
func (r *Registry) Migrations() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.overlay["migrations"].(map[string]any); ok {
		return m
	}
	return nil
}

// deepMerge returns a new map that is base with overlay applied recursively.
// Overlay map values replace base map values at the same key; non-map values replace directly.
// Arrays are replaced (not appended).
func deepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, overlayVal := range overlay {
		if overlayMap, ok := overlayVal.(map[string]any); ok {
			if baseMap, ok := result[k].(map[string]any); ok {
				result[k] = deepMerge(baseMap, overlayMap)
				continue
			}
		}
		result[k] = overlayVal
	}
	return result
}
