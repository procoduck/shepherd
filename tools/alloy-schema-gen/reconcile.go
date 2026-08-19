// Command reconcile keeps internal/schema/artifacts/overlay.json in sync with
// the freshly generated artifact after an Alloy version bump (VB refinement
// spec §C3). run.sh invokes it once the artifact JSON has been written.
//
// It appends a skeleton entry for every artifact component missing from the
// overlay ({"category": <heuristic>, "doc": "", "needs_review": true}) and
// deletes overlay entries whose component no longer exists in the artifact.
// Editorial fields (doc, icon, port_display_order, discovery_stub, ...) on
// entries that survive are never touched, and only the top-level
// "components" map is modified — "wire_types" and "categories" pass through
// unchanged.
//
// Usage: go run reconcile.go <artifact.json> <overlay.json>
//
// Exit code is nonzero only on I/O errors (the overlay file cannot be read,
// parsed, or written back) — a reconciliation that adds or removes entries
// is expected, routine output, not a failure.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf16"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: reconcile.go <artifact.json> <overlay.json>")
		os.Exit(1)
	}
	artifactPath := os.Args[1]
	overlayPath := os.Args[2]

	artifact, err := loadJSON(artifactPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: reconcile: reading artifact: %v\n", err)
		os.Exit(1)
	}
	overlay, err := loadJSON(overlayPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: reconcile: reading overlay: %v\n", err)
		os.Exit(1)
	}

	artifactComponents, _ := artifact["components"].(map[string]any) //nolint:errcheck // absent/wrong-shape treated as empty
	overlayComponents, _ := overlay["components"].(map[string]any)   //nolint:errcheck // absent/wrong-shape treated as empty
	if overlayComponents == nil {
		overlayComponents = map[string]any{}
	}

	added, removed := reconcileComponents(artifactComponents, overlayComponents)
	overlay["components"] = overlayComponents

	if len(added) == 0 && len(removed) == 0 {
		fmt.Println("==> overlay reconciliation: no changes (0 added, 0 removed)")
		return
	}

	if err := writeJSON(overlayPath, overlay); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: reconcile: writing overlay: %v\n", err)
		os.Exit(1)
	}

	sort.Strings(added)
	sort.Strings(removed)
	fmt.Printf("==> overlay reconciliation: %d added, %d removed\n", len(added), len(removed))
	for _, name := range added {
		fmt.Printf("    + %s (needs_review)\n", name)
	}
	for _, name := range removed {
		fmt.Printf("    - %s\n", name)
	}
}

// reconcileComponents mutates overlayComponents in place: it appends a
// needs_review skeleton for every artifactComponents entry missing from
// overlayComponents, and deletes every overlayComponents entry whose key is
// no longer present in artifactComponents. Entries present in both maps are
// left completely untouched. It returns the sorted-by-caller-if-needed list
// of component names added and removed.
func reconcileComponents(artifactComponents, overlayComponents map[string]any) (added, removed []string) {
	for name := range artifactComponents {
		if _, ok := overlayComponents[name]; ok {
			continue
		}
		overlayComponents[name] = map[string]any{
			"category":     categorize(name),
			"doc":          "",
			"needs_review": true,
		}
		added = append(added, name)
	}
	for name := range overlayComponents {
		if _, ok := artifactComponents[name]; ok {
			continue
		}
		delete(overlayComponents, name)
		removed = append(removed, name)
	}
	return added, removed
}

// categorize applies the component-path heuristic from
// docs/visual-builder-refinement.md §C3 to guess a starting palette category
// for a component new to the overlay. It is best-effort only: every entry it
// produces is marked needs_review for a human to confirm or correct.
//
// Patterns are checked in priority order (first match wins) because some
// overlap: e.g. "discovery.relabel" matches both the "discovery.*" sources
// prefix and the "*.relabel" transform suffix — it is a transform (it
// relabels discovery targets), so transform is checked first.
//
//  1. transform    — *.relabel, *.process*
//  2. destinations — *.remote_write, *.write, otelcol.exporter.*
//  3. sources      — discovery.*, *.source.*, *.exporter.*
//  4. config       — remote.*, local.*
//  5. advanced     — everything else
func categorize(name string) string {
	switch {
	case strings.HasSuffix(name, ".relabel"), strings.Contains(name, ".process"):
		return "transform"
	case strings.HasSuffix(name, ".remote_write"), strings.HasSuffix(name, ".write"), strings.HasPrefix(name, "otelcol.exporter."):
		return "destinations"
	case strings.HasPrefix(name, "discovery."), strings.Contains(name, ".source."), strings.Contains(name, ".exporter."):
		return "sources"
	case strings.HasPrefix(name, "remote."), strings.HasPrefix(name, "local."):
		return "config"
	default:
		return "advanced"
	}
}

func loadJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}

// writeJSON writes v to path as indented JSON matching the committed
// overlay.json's existing formatting: 2-space indent, alphabetically sorted
// object keys (Go's default map-marshal order), and non-ASCII characters
// \u-escaped (matching how the file was originally authored) so untouched
// doc strings elsewhere in the file don't move on every reconcile run.
func writeJSON(path string, v map[string]any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := os.WriteFile(path, escapeNonASCII(buf.Bytes()), 0o644); err != nil { //nolint:gosec // overlay.json is a repo-tracked, non-secret file
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// escapeNonASCII rewrites every rune above ASCII in b to a JSON \uXXXX
// escape (surrogate-pair escaped when above the BMP). encoding/json does not
// do this by default; this mirrors the ensure_ascii-style encoding the
// committed overlay.json already uses.
func escapeNonASCII(b []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(b) {
		if r < 128 {
			out.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", r1, r2)
			continue
		}
		fmt.Fprintf(&out, "\\u%04x", r)
	}
	return out.Bytes()
}
