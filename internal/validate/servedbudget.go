package validate

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrServedFormTooLarge is a strict org's served form that would be more
// than MaxContentBytes: its build (pinnedimport.Inline, InsertTenantDrops)
// stops as soon as it would pass that, before building the rest.
var ErrServedFormTooLarge = errors.New("the pipeline as served (pinned modules inlined, " + tenantLabel +
	" dropped after each labelmap) would be more than the " + fmt.Sprint(MaxContentBytes) +
	" bytes a pipeline may be")

// ServedBudget is the one byte budget of a strict org's served-form build
// (pinnedimport.Inline, InsertTenantDrops): every piece of new text the
// build makes is charged before it is allocated, so the build never holds
// much more than MaxContentBytes of it, however many modules or drops a
// small text expands to.
//
// The build is one pass per level that makes the final served form
// directly (BuildServedLevel: each level's modules inlined and drops
// inserted at once), never an intermediate text. No intermediate is
// charged, because one can be longer than the result: a content literal
// written with escapes the encoder does not use (\x41, \u0061 for "a") is
// re-quoted shorter, so text inlined before the drops are inserted may
// pass the cap while the served form fits.
//
// Invariant: at every moment what is charged is at most the length of the
// final served form F, and when the build finishes having changed the
// text it is exactly len(F). So Take fails if and only if len(F) would
// pass the cap (an unchanged text charges nothing; Inline checks its
// length), and every served form that fits is built byte for byte. Proof,
// by the rules every level follows:
//
//   - a level that returns its text unchanged charges nothing;
//   - a level that rewrites its text charges each replacement before
//     building it, and the bytes it keeps (ApplySplices, the text outside
//     every replaced range) when it joins them: it ends charged exactly the
//     length of its result R, and each piece it charged is a distinct part
//     of R;
//   - a level that embeds a nested level's changed result N gives back
//     len(N) (Give) before charging the piece that holds N re-quoted,
//     which is at least len(N)+2 (quoting never shortens), so while the
//     nested level runs it holds at most len(N) — less than the piece it
//     becomes.
//
// Each level's result appears in F inside its parent's piece, re-quoted
// once per enclosing level (never shorter), and the pieces of different
// levels are disjoint, so the sum charged never exceeds len(F). Literal
// ranges that are replaced are never charged as kept bytes, so how they
// were written does not matter.
type ServedBudget struct {
	left int
}

// NewServedBudget is a budget of MaxContentBytes.
func NewServedBudget() *ServedBudget { return &ServedBudget{left: MaxContentBytes} }

// Take charges n bytes: ErrServedFormTooLarge (and nothing charged) when
// fewer are left.
func (b *ServedBudget) Take(n int) error {
	if n < 0 || n > b.left {
		return ErrServedFormTooLarge
	}
	b.left -= n
	return nil
}

// Give returns n bytes charged earlier (see ServedBudget).
func (b *ServedBudget) Give(n int) { b.left += n }

// QuotedLen is len(alloysyntax.QuoteString(s)) — the Go %q encoding, which
// the Alloy encoder prints unchanged — counted without building it, so the
// budget is charged before the quoted text is allocated.
func QuotedLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		r, width := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, width = utf8.DecodeRuneInString(s[i:])
		}
		i += width
		n += escapedRuneLen(r, width)
	}
	return n
}

// escapedRuneLen is how many bytes strconv.Quote writes for rune r, width
// bytes in the input (strconv's appendEscapedRune, for a '"' quote).
func escapedRuneLen(r rune, width int) int {
	if width == 1 && r == utf8.RuneError {
		return 4 // \xHH
	}
	if r == '"' || r == '\\' {
		return 2
	}
	if strconv.IsPrint(r) {
		return width
	}
	switch r {
	case '\a', '\b', '\f', '\n', '\r', '\t', '\v':
		return 2
	}
	switch {
	case r < ' ' || r == 0x7f:
		return 4 // \xHH
	case !utf8.ValidRune(r):
		return 6 // �
	case r < 0x10000:
		return 6 // \uHHHH
	default:
		return 10 // \UHHHHHHHH
	}
}

// Splice is one rewrite of the byte range [Start, End) of a text by Text.
type Splice struct {
	Start, End int
	Text       string
}

// ApplySplices is text with every splice applied, built once: in offset
// order, and, at one offset, the splice made later first (as applying them
// one by one from the last offset back put them). The ranges must not
// overlap. The bytes of text kept are charged to b before the result is
// built; the splices' texts are charged by whoever built them.
func ApplySplices(text string, splices []Splice, b *ServedBudget) (string, error) {
	sorted := slices.Clone(splices)
	slices.Reverse(sorted)
	slices.SortStableFunc(sorted, func(a, b Splice) int { return a.Start - b.Start })
	kept, added, at := len(text), 0, 0
	for _, sp := range sorted {
		if sp.Start < at || sp.End < sp.Start || sp.End > len(text) {
			return "", fmt.Errorf("the served form's rewrites overlap at offset %d", sp.Start)
		}
		kept -= sp.End - sp.Start
		added += len(sp.Text)
		at = sp.End
	}
	if err := b.Take(kept); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.Grow(kept + added)
	at = 0
	for _, sp := range sorted {
		sb.WriteString(text[at:sp.Start])
		sb.WriteString(sp.Text)
		at = sp.End
	}
	sb.WriteString(text[at:])
	return sb.String(), nil
}
