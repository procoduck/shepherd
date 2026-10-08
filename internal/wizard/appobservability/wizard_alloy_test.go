package appobservability_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// The wizard's own goldens are a byte comparison against text this package
// produced, so they agree with whatever it emits — including things the real
// binary refuses. That is how `env("...")` shipped: deprecated in Alloy
// v1.18.1, rejected by `alloy validate`, and invisible to a golden diff and to
// the wizard's own "No problems" preview alike. This package's original copy
// of the binary-or-docker-shim lookup now lives in wizardtest, shared by
// every wizard.

// TestGoldensAgainstRealAlloy runs every committed golden through the real
// pinned Alloy binary's validate — see wizardtest.AssertGoldensAgainstRealAlloy's doc.
func TestGoldensAgainstRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensAgainstRealAlloy(t, "testdata")
}

// TestGoldensLoadInRealAlloy starts the pinned Alloy image on every golden
// and requires the initial load to succeed. `alloy validate` passed
// `stage.logfmt {}` and `stage.json {}`, which a running Alloy refuses while
// building loki.process ("logfmt mapping or regex is required") — see
// wizardtest.AssertGoldensLoadInRealAlloy's doc.
func TestGoldensLoadInRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensLoadInRealAlloy(t, "testdata")
}
