package validate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/token"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// Version-aware validation (#375). Stage 2's `alloy validate` runs the one
// pinned binary, so it accepts whatever the pinned Alloy release accepts. A
// collector running an older release can still reject that config — a
// component or attribute added after its release — and an Alloy that rejects
// a remote config keeps its last good one (or, for an unrecognised
// attribute, the whole remote config). CheckAlloyVersions closes that gap
// with the component schemas Shepherd already ships for older releases
// (internal/schema/artifacts): it checks a config against the schema each
// Alloy version a collector's active instances report resolves to.
//
// The check is a DIFFERENCE against the pinned schema: an item is refused
// for a schema only when the pinned schema knows it and that schema does
// not. An item the pinned schema does not know is Stage 2's to judge
// (`alloy validate` refuses a component the pinned release lacks), so a gap
// in the generated schema can never turn into a refusal here.
//
// Resolution (ResolveVersion): a release version with its own shipped
// schema uses it. Otherwise the NEAREST OLDER shipped schema stands in —
// the newest of the same minor not newer than the version, else the newest
// older one; a version older than every shipped schema uses the oldest —
// with a warning that the check is an approximation. The pinned schema is
// the fallback only for a version newer than every shipped one, one that is
// not a plain release, none reported, or no active instance, each with a
// warning. Versions resolving to the same schema are checked in one walk,
// so the work per check is bounded by the number of shipped schemas however
// many distinct versions are reported.

// StabilityFlag is the Alloy run flag that admits components below general
// availability; `alloy validate` here always runs with
// --stability.level=experimental (Validator.Stage2).
const StabilityFlag = "--stability.level"

// MaxReportedVersions caps the distinct reported versions one check
// resolves (the oldest release versions first, then the rest): instance ids
// and their collector.version are chosen by agents, and nothing else bounds
// them. Versions past the cap are counted in a warning, not resolved.
const MaxReportedVersions = 256

// MaxRecordedVersionLen bounds a reported version as RecordedVersion keeps
// it, in bytes.
const MaxRecordedVersionLen = 64

// maxVersionSamples is how many reported versions a warning quotes.
const maxVersionSamples = 3

// releaseVersion is a plain Alloy release version as collectors report it in
// collector.version ("v1.20.1"; the leading v is optional).
var releaseVersion = regexp.MustCompile(`^v?(\d{1,6})\.(\d{1,6})\.(\d{1,6})$`)

type semver [3]int

func parseRelease(s string) (semver, bool) {
	m := releaseVersion.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := range 3 {
		v[i], _ = strconv.Atoi(m[i+1]) //nolint:errcheck // the regexp admits digits only
	}
	return v, true
}

func (a semver) less(b semver) bool {
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func (a semver) String() string { return fmt.Sprintf("%d.%d.%d", a[0], a[1], a[2]) }

// DisplayVersion is an artifact name as an operator reads it
// ("alloy-v1.21.2" → "v1.21.2").
func DisplayVersion(artifact string) string {
	return strings.TrimPrefix(artifact, "alloy-")
}

// SanitizeVersion makes an agent-reported version safe to quote in a
// message: at most 24 characters of [0-9A-Za-z.+_-], anything else as '?'.
func SanitizeVersion(s string) string { return sanitize(s, 24) }

// SanitizeName is SanitizeVersion for an agent-chosen instance name: at most
// 64 characters.
func SanitizeName(s string) string { return sanitize(s, 64) }

func sanitize(s string, limit int) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		if i >= limit {
			b.WriteString("…")
			break
		}
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '+', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('?')
		}
	}
	return b.String()
}

// blockSchema is one level of a component's arguments: its attribute names
// and nested blocks.
type blockSchema struct {
	attrs  map[string]bool
	blocks map[string]*blockSchema
}

type componentSchema struct {
	body      *blockSchema
	stability string
}

type versionSchema struct {
	artifact   string
	components map[string]*componentSchema
}

// rawBlock is the artifact's shape for a component or block.
type rawBlock struct {
	Attributes []struct {
		Name string `json:"name"`
	} `json:"attributes"`
	Blocks    []rawNamedBlock `json:"blocks"`
	Stability string          `json:"stability"`
}

type rawNamedBlock struct {
	Name string `json:"name"`
	rawBlock
}

func (b *rawBlock) index() *blockSchema {
	out := &blockSchema{attrs: make(map[string]bool, len(b.Attributes)), blocks: make(map[string]*blockSchema, len(b.Blocks))}
	for _, a := range b.Attributes {
		out.attrs[a.Name] = true
	}
	for i := range b.Blocks {
		out.blocks[b.Blocks[i].Name] = b.Blocks[i].index()
	}
	return out
}

// versionSchemas caches each shipped version's reduced schema: loaded on
// first use, once per process, and only the names and stability this check
// reads — never the whole artifact per check.
var (
	versionSchemasMu sync.Mutex
	versionSchemas   = map[string]*versionSchema{}
	shippedOnce      sync.Once
	shipped          []shippedSchema // ascending by version
	schemaFS         fs.FS           = schema.Embedded
	pinnedArtifact                   = version.AlloySchemaVersion
)

type shippedSchema struct {
	artifact string
	v        semver
}

// ShippedVersions returns the artifact names of every schema this binary
// embeds ("alloy-v1.18.1", ...), oldest first.
func ShippedVersions() []string {
	loadShipped()
	out := make([]string, 0, len(shipped))
	for _, s := range shipped {
		out = append(out, s.artifact)
	}
	return out
}

func loadShipped() {
	shippedOnce.Do(func() {
		entries, err := fs.ReadDir(schemaFS, "artifacts")
		if err != nil {
			slog.Error("version check: listing schema artifacts", "err", err)
			return
		}
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !strings.HasPrefix(name, "alloy-v") {
				continue
			}
			if v, ok := parseRelease(strings.TrimPrefix(name, "alloy-")); ok {
				shipped = append(shipped, shippedSchema{artifact: name, v: v})
			}
		}
		sort.Slice(shipped, func(i, j int) bool { return shipped[i].v.less(shipped[j].v) })
	})
}

func loadVersionSchema(artifact string) (*versionSchema, error) {
	versionSchemasMu.Lock()
	defer versionSchemasMu.Unlock()
	if vs, ok := versionSchemas[artifact]; ok {
		return vs, nil
	}
	data, err := fs.ReadFile(schemaFS, "artifacts/"+artifact+".json")
	if err != nil {
		return nil, fmt.Errorf("validate: read schema %s: %w", artifact, err)
	}
	var raw struct {
		Components map[string]rawBlock `json:"components"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("validate: parse schema %s: %w", artifact, err)
	}
	vs := &versionSchema{artifact: artifact, components: make(map[string]*componentSchema, len(raw.Components))}
	for name, c := range raw.Components {
		vs.components[name] = &componentSchema{body: c.index(), stability: c.Stability}
	}
	versionSchemas[artifact] = vs
	return vs, nil
}

// Resolution is how a reported version maps to a schema.
type Resolution int

const (
	// ResolvedExact is the version's own shipped schema.
	ResolvedExact Resolution = iota
	// ResolvedNearestOlder is no schema of its own; the nearest older one.
	ResolvedNearestOlder
	// ResolvedOldest is older than every shipped schema; the oldest one,
	// which is newer than the version.
	ResolvedOldest
	// ResolvedNewer is newer than every shipped schema; the pinned one.
	ResolvedNewer
	// ResolvedNotRelease is not a plain vX.Y.Z release; the pinned one.
	ResolvedNotRelease
	// ResolvedMissing is the instance reported no version; the pinned one.
	ResolvedMissing
	// ResolvedNoInstances is the collector has no active instance; the pinned
	// one.
	ResolvedNoInstances
)

// ResolvedVersion is one reported Alloy version and the schema it is checked
// against.
type ResolvedVersion struct {
	// Reported is the version as the collector reported it ("" for none).
	Reported string
	// Artifact is the schema it is checked against.
	Artifact string
	// How says how Artifact was chosen.
	How Resolution
}

// ResolveVersion maps one reported version to the schema it is checked
// against (see the package comment on versions.go).
func ResolveVersion(reported string) ResolvedVersion {
	loadShipped()
	reported = strings.TrimSpace(reported)
	rv := ResolvedVersion{Reported: reported, Artifact: pinnedArtifact}
	if reported == "" {
		rv.How = ResolvedMissing
		return rv
	}
	v, ok := parseRelease(reported)
	if !ok || len(shipped) == 0 {
		rv.How = ResolvedNotRelease
		return rv
	}
	if newest := shipped[len(shipped)-1]; newest.v.less(v) {
		rv.How = ResolvedNewer
		return rv
	}
	var sameMinor, older *shippedSchema
	for i := range shipped {
		s := &shipped[i]
		if s.v == v {
			rv.Artifact, rv.How = s.artifact, ResolvedExact
			return rv
		}
		if v.less(s.v) {
			continue
		}
		older = s // ascending: the last one not newer than v
		if s.v[0] == v[0] && s.v[1] == v[1] {
			sameMinor = s
		}
	}
	switch {
	case sameMinor != nil:
		rv.Artifact, rv.How = sameMinor.artifact, ResolvedNearestOlder
	case older != nil:
		rv.Artifact, rv.How = older.artifact, ResolvedNearestOlder
	default:
		rv.Artifact, rv.How = shipped[0].artifact, ResolvedOldest
	}
	return rv
}

// ResolveVersions resolves every distinct reported version (at most
// MaxReportedVersions of them, oldest release versions first). An empty
// reported — no active instance — resolves to the pinned schema.
func ResolveVersions(reported []string) []ResolvedVersion {
	plan := planVersions(reported)
	return plan.resolved
}

// truncatedMarker prefixes the entry WithTruncatedVersions appends.
const truncatedMarker = "\x00truncated:"

// WithTruncatedVersions marks a version list a caller cut short (a
// per-collector cap in SQL): dropped more versions were reported. The check
// then also runs against the oldest shipped schema and warns, as it does
// for versions past MaxReportedVersions — truncated data never falls back
// to the pinned schema alone.
func WithTruncatedVersions(versions []string, dropped int) []string {
	if dropped <= 0 {
		return versions
	}
	return append(slices.Clone(versions), truncatedMarker+strconv.Itoa(dropped))
}

// versionPlan is a reported version set grouped by the schema each version
// resolves to: one walk per schema.
type versionPlan struct {
	resolved []ResolvedVersion
	// dropped counts distinct versions past MaxReportedVersions.
	dropped int
	// groups by artifact, in a stable order.
	artifacts []string
	byArt     map[string][]ResolvedVersion
}

func planVersions(reported []string) versionPlan {
	loadShipped()
	p := versionPlan{byArt: map[string][]ResolvedVersion{}}
	if len(reported) == 0 {
		p.resolved = []ResolvedVersion{{Artifact: pinnedArtifact, How: ResolvedNoInstances}}
	} else {
		distinct := map[string]bool{}
		var list []string
		for _, r := range reported {
			if n, ok := strings.CutPrefix(r, truncatedMarker); ok {
				if dropped, err := strconv.Atoi(n); err == nil && dropped > 0 {
					p.dropped += dropped
				}
				continue
			}
			r = strings.TrimSpace(r)
			if !distinct[r] {
				distinct[r] = true
				list = append(list, r)
			}
		}
		sortReported(list)
		if len(list) > MaxReportedVersions {
			p.dropped += len(list) - MaxReportedVersions
			list = list[:MaxReportedVersions]
		}
		for _, r := range list {
			p.resolved = append(p.resolved, ResolveVersion(r))
		}
	}
	for _, rv := range p.resolved {
		if _, ok := p.byArt[rv.Artifact]; !ok {
			p.artifacts = append(p.artifacts, rv.Artifact)
		}
		p.byArt[rv.Artifact] = append(p.byArt[rv.Artifact], rv)
	}
	// Versions that were not resolved may include any release: check
	// against the strictest stand-in, the oldest shipped schema, rather
	// than let truncation fall back to the pin alone.
	if p.dropped > 0 && len(shipped) > 0 {
		if oldest := shipped[0].artifact; !slices.Contains(p.artifacts, oldest) {
			p.artifacts = append(p.artifacts, oldest)
		}
	}
	sort.Slice(p.artifacts, func(i, j int) bool {
		vi, _ := parseRelease(DisplayVersion(p.artifacts[i]))
		vj, _ := parseRelease(DisplayVersion(p.artifacts[j]))
		return vi.less(vj)
	})
	return p
}

// sortReported orders distinct reported versions oldest release versions
// first, then the rest: what a cap keeps is what the strictest schemas check.
func sortReported(list []string) {
	sort.SliceStable(list, func(i, j int) bool {
		vi, oki := parseRelease(list[i])
		vj, okj := parseRelease(list[j])
		switch {
		case oki && okj:
			if vi == vj {
				return list[i] < list[j]
			}
			return vi.less(vj)
		case oki != okj:
			return oki
		default:
			return list[i] < list[j]
		}
	})
}

// RecordedVersion is a reported version as the version check keeps it — in
// a collector's check fingerprint and audit rows — and compares it: trimmed,
// and at most MaxRecordedVersionLen bytes. A longer one is cut (on a rune
// boundary) and ends in "…", which no release version contains, so it stays
// "not a release version" whatever the cut leaves; two versions cut to the
// same text resolve, and are quoted, alike (a quote shows at most 24
// sanitised characters), so the cut loses nothing the check uses.
func RecordedVersion(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxRecordedVersionLen {
		return s
	}
	const ellipsis = "…"
	cut := MaxRecordedVersionLen - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// RecordedVersions is reported (a collector's active instances' versions) as
// the version check records it: each RecordedVersion, distinct, oldest
// release versions first, at most MaxReportedVersions — and total, the
// number of distinct versions before the cap. A caller checking kept passes
// WithTruncatedVersions(kept, total-len(kept)), so the versions cut are still
// accounted for.
func RecordedVersions(reported []string) (kept []string, total int) {
	seen := make(map[string]bool, len(reported))
	for _, r := range reported {
		r = RecordedVersion(r)
		if !seen[r] {
			seen[r] = true
			kept = append(kept, r)
		}
	}
	sortReported(kept)
	total = len(kept)
	if len(kept) > MaxReportedVersions {
		kept = kept[:MaxReportedVersions]
	}
	return kept, total
}

// notes are the warnings a plan carries beyond the walk's own findings:
// every version not checked against its exact schema, grouped by how it
// was resolved, quoting at most maxVersionSamples sanitised versions each.
func (p versionPlan) notes() []string {
	type key struct {
		art string
		how Resolution
	}
	samples := map[key][]string{}
	var order []key
	for _, rv := range p.resolved {
		if rv.How == ResolvedExact {
			continue
		}
		k := key{rv.Artifact, rv.How}
		if _, ok := samples[k]; !ok {
			order = append(order, k)
		}
		samples[k] = append(samples[k], rv.Reported)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].how != order[j].how {
			return order[i].how < order[j].how
		}
		return order[i].art < order[j].art
	})
	var out []string
	for _, k := range order {
		out = append(out, resolutionNote(k.how, DisplayVersion(k.art), samples[k]))
	}
	if p.dropped > 0 && len(shipped) > 0 {
		out = append(out, fmt.Sprintf("%d further reported versions were not checked individually (at most %d per collector); also checked against the oldest shipped schema, %s",
			p.dropped, MaxReportedVersions, DisplayVersion(shipped[0].artifact)))
	}
	return out
}

// quoteVersions renders reported versions for a message: sanitised, at most
// maxVersionSamples, then a count.
func quoteVersions(vs []string) string {
	shown := make([]string, 0, maxVersionSamples)
	for i, v := range vs {
		if i == maxVersionSamples {
			break
		}
		if r, ok := parseRelease(v); ok {
			shown = append(shown, r.String())
		} else {
			shown = append(shown, strconv.Quote(SanitizeVersion(v)))
		}
	}
	s := strings.Join(shown, ", ")
	if n := len(vs) - len(shown); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

func resolutionNote(how Resolution, schemaName string, reported []string) string {
	switch how {
	case ResolvedNearestOlder:
		return fmt.Sprintf("runs Alloy %s, which has no shipped schema; checked against the nearest older schema, %s (an approximation)",
			quoteVersions(reported), schemaName)
	case ResolvedOldest:
		return fmt.Sprintf("runs Alloy %s, older than every shipped schema; checked against the oldest, %s (an approximation: it may accept what that version cannot load)",
			quoteVersions(reported), schemaName)
	case ResolvedNewer:
		return fmt.Sprintf("runs Alloy %s, newer than every shipped schema; checked against %s only", quoteVersions(reported), schemaName)
	case ResolvedNotRelease:
		return fmt.Sprintf("runs Alloy %s, which is not a release version; checked against %s only", quoteVersions(reported), schemaName)
	case ResolvedMissing:
		return fmt.Sprintf("reports no Alloy version; checked against %s only", schemaName)
	case ResolvedNoInstances:
		return fmt.Sprintf("has no active instance; checked against %s only", schemaName)
	case ResolvedExact:
		return ""
	default:
		return ""
	}
}

// VersionSetKey is the dedupe key of a reported version set: two collectors
// with the same served bytes and the same key get the same version verdict
// and the same messages.
func VersionSetKey(reported []string) string {
	p := planVersions(reported)
	parts := append([]string{}, p.artifacts...)
	parts = append(parts, p.notes()...)
	return strings.Join(parts, "\x00")
}

// itemFinding is one missing item, merged across the schemas that lack it.
type itemFinding struct {
	pos     token.Position
	what    string
	schemas []string
}

// stabilityFinding is one component below GA, merged across schemas.
type stabilityFinding struct {
	pos       token.Position
	component string
	stability string
	schemas   []string
}

// CheckAlloyVersions checks content against the schema of every version in
// reported (a collector's active instances' collector.version values) and
// returns Stage 3 diagnostics: an error (Warning=false) for each component,
// block or attribute the pinned schema knows and a resolved schema lacks
// (one diagnostic per item, naming every schema that lacks it, in
// Diagnostic.AlloySchemas), and warnings for each version not checked
// against its exact schema and each component below general availability
// (one per component and level, naming the --stability.level it needs).
//
// Content that does not parse yields no diagnostics: Stage 1 refuses it.
// A schema that fails to load is a build defect the schema tests catch; it
// is logged and that schema is skipped, never refused on.
func CheckAlloyVersions(content string, reported []string) []Diagnostic {
	file, err := alloysyntax.ParseFile("<merged>", []byte(content))
	if err != nil {
		return nil
	}
	pin, err := loadVersionSchema(pinnedArtifact)
	if err != nil {
		slog.Error("version check unavailable", "err", err)
		return nil
	}
	plan := planVersions(reported)
	items := map[string]*itemFinding{}
	var itemOrder []string
	stab := map[string]*stabilityFinding{}
	var stabOrder []string
	components := 0
	for _, art := range plan.artifacts {
		vs, err := loadVersionSchema(art)
		if err != nil {
			slog.Error("version check: schema unavailable", "version", art, "err", err)
			continue
		}
		c := &versionChecker{pin: pin, ver: vs, budget: maxVersionModuleBytes}
		c.walkTop(file.Body, 0)
		components = max(components, c.components)
		name := DisplayVersion(art)
		for _, f := range c.items {
			k := fmt.Sprintf("%d:%d:%s", f.pos.Line, f.pos.Column, f.what)
			if _, ok := items[k]; !ok {
				items[k] = &itemFinding{pos: f.pos, what: f.what}
				itemOrder = append(itemOrder, k)
			}
			items[k].schemas = append(items[k].schemas, name)
		}
		for _, f := range c.stability {
			k := f.component + "\x00" + f.stability
			if _, ok := stab[k]; !ok {
				stab[k] = &stabilityFinding{pos: f.pos, component: f.component, stability: f.stability}
				stabOrder = append(stabOrder, k)
			}
			stab[k].schemas = append(stab[k].schemas, name)
		}
	}
	var out []Diagnostic
	for _, k := range itemOrder {
		f := items[k]
		out = append(out, Diagnostic{
			Line: f.pos.Line, Col: f.pos.Column, Stage: 3,
			Message:      fmt.Sprintf("%s is not available in Alloy %s", f.what, strings.Join(f.schemas, ", ")),
			AlloySchemas: f.schemas,
		})
	}
	// A note about how versions were resolved is worth saying only about a
	// config with a component in it: there is nothing to check an empty one
	// against.
	if components > 0 {
		for _, n := range plan.notes() {
			out = append(out, Diagnostic{Stage: 3, Warning: true, Message: n})
		}
	}
	for _, k := range stabOrder {
		f := stab[k]
		need := StabilityFlag + "=experimental"
		if f.stability == "public-preview" {
			need = StabilityFlag + "=public-preview (or experimental)"
		}
		out = append(out, Diagnostic{
			Line: f.pos.Line, Col: f.pos.Column, Stage: 3, Warning: true,
			Message: fmt.Sprintf("component %s is %s in Alloy %s; collectors need %s to load it",
				f.component, f.stability, strings.Join(f.schemas, ", "), need),
		})
	}
	return out
}

// AlloyVersionErrors is CheckAlloyVersions without its warnings: only the
// findings that refuse.
func AlloyVersionErrors(content string, reported []string) []Diagnostic {
	var out []Diagnostic
	for _, d := range CheckAlloyVersions(content, reported) {
		if !d.Warning {
			out = append(out, d)
		}
	}
	return out
}

const (
	// maxVersionModuleDepth and maxVersionModuleBytes bound the import.string
	// modules the check reads, as signals.Derive does.
	maxVersionModuleDepth = 8
	maxVersionModuleBytes = 1 << 20
)

type rawItem struct {
	pos  token.Position
	what string
}

type rawStability struct {
	pos       token.Position
	component string
	stability string
}

type versionChecker struct {
	pin, ver  *versionSchema
	budget    int
	items     []rawItem
	stability []rawStability
	seenStab  map[string]bool
	// components counts the pinned-schema components the walk met.
	components int
	// at, when valid, is the import.string block a module's statements are
	// reported at: their own positions are relative to the module text.
	at token.Pos
}

func (c *versionChecker) position(pos token.Pos) token.Position {
	if c.at.Valid() {
		pos = c.at
	}
	return pos.Position()
}

// walkTop walks a statement list in which a block may be a component: the
// config's top level, and the body of any block that is not a component the
// pinned schema knows (declare, foreach and its template, and the like).
func (c *versionChecker) walkTop(body ast.Body, moduleDepth int) {
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(block.Name, ".")
		pinComp, known := c.pin.components[name]
		if !known {
			if name == "import.string" && moduleDepth < maxVersionModuleDepth {
				if mod, ok := c.importedModule(block); ok {
					outer := c.at
					if !outer.Valid() {
						c.at = block.NamePos
					}
					c.walkTop(mod.Body, moduleDepth+1)
					c.at = outer
				}
				continue
			}
			c.walkTop(block.Body, moduleDepth)
			continue
		}
		c.components++
		verComp, ok := c.ver.components[name]
		if !ok {
			c.items = append(c.items, rawItem{c.position(block.NamePos), "component " + name})
			continue
		}
		c.checkStability(block, name, verComp.stability)
		c.walkBody(block.Body, pinComp.body, verComp.body, name+blockLabel(block))
	}
}

func blockLabel(b *ast.BlockStmt) string {
	if b.Label == "" {
		return ""
	}
	return fmt.Sprintf(" %q", b.Label)
}

func (c *versionChecker) checkStability(block *ast.BlockStmt, name, stability string) {
	switch stability {
	case "experimental", "public-preview":
	default:
		return
	}
	if c.seenStab == nil {
		c.seenStab = map[string]bool{}
	}
	if c.seenStab[name] {
		return
	}
	c.seenStab[name] = true
	c.stability = append(c.stability, rawStability{c.position(block.NamePos), name, stability})
}

// walkBody compares one level of a component's body: an attribute or block
// the pinned schema knows here and the version's does not is refused; one
// the pinned schema does not know is left to Stage 2.
func (c *versionChecker) walkBody(body ast.Body, pin, ver *blockSchema, path string) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			if pin.attrs[s.Name.Name] && !ver.attrs[s.Name.Name] {
				c.items = append(c.items, rawItem{c.position(s.Name.NamePos), fmt.Sprintf("attribute %s (in %s)", s.Name.Name, path)})
			}
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			pinBlock, known := pin.blocks[name]
			if !known {
				continue
			}
			verBlock, ok := ver.blocks[name]
			if !ok {
				c.items = append(c.items, rawItem{c.position(s.NamePos), fmt.Sprintf("block %s (in %s)", name, path)})
				continue
			}
			c.walkBody(s.Body, pinBlock, verBlock, path+" > "+name)
		}
	}
}

// importedModule parses an import.string block's literal content within the
// remaining budget; ok is false for content the check cannot read.
func (c *versionChecker) importedModule(block *ast.BlockStmt) (*ast.File, bool) {
	var content ast.Expr
	for _, st := range block.Body {
		if a, ok := st.(*ast.AttributeStmt); ok && a.Name.Name == "content" {
			content = a.Value
		}
	}
	for {
		p, ok := content.(*ast.ParenExpr)
		if !ok {
			break
		}
		content = p.Inner
	}
	lit, ok := content.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return nil, false
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil || len(text) > c.budget {
		return nil, false
	}
	c.budget -= len(text)
	file, err := alloysyntax.ParseFile("<import.string "+block.Label+">", []byte(text))
	if err != nil {
		return nil, false
	}
	return file, true
}
