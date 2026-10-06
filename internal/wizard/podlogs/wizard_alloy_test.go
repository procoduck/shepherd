package podlogs_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// TestGoldensAgainstRealAlloy runs every committed golden through the real
// pinned Alloy binary — see wizardtest.AssertGoldensAgainstRealAlloy's doc.
func TestGoldensAgainstRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensAgainstRealAlloy(t, "testdata")
}

// TestGoldensLoadInRealAlloy starts the pinned Alloy image on every golden
// and requires the initial load to succeed — the component-construction
// rules `alloy validate` never reaches; see
// wizardtest.AssertGoldensLoadInRealAlloy's doc.
func TestGoldensLoadInRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensLoadInRealAlloy(t, "testdata")
}
