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

// ServedBudget is the one byte budget of a strict org's served-form build:
// the pinned-module inliner (pinnedimport.Inline) and the __tenant_id__ drop
// pass (InsertTenantDropsWithin) charge it, before allocating, for every
// piece of new text they build, so the build never holds much more than
// MaxContentBytes of it, however many modules or drops a small text
// expands to.
//
// What is charged is always at most the length the finished served form
// will have, so the budget refuses exactly the builds whose result would be
// too large, and never one that fits:
//
//   - a pass that returns its text unchanged charges nothing (it builds
//     nothing);
//   - a pass that rewrites a text charges each replacement text before
//     building it, and the bytes it keeps (ApplySplices) when it joins them,
//     so it ends having charged exactly the length of its result;
//   - a pass that embeds a nested pass's changed result (a module text,
//     re-quoted) gives that result's length back (Give) and charges the
//     piece it embeds it in, which is at least as long.
//
// A nested result is inside its parent's result, re-quoted (never shorter),
// and the parent's other pieces are disjoint from it, so the sum charged
// along the way never exceeds the final length.
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
