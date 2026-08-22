package clustermetrics_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// TestGoldensAgainstRealAlloy runs every committed golden through the real
// pinned Alloy binary — see wizardtest.AssertGoldensAgainstRealAlloy's doc
// for why a byte-for-byte golden comparison alone is not enough.
func TestGoldensAgainstRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensAgainstRealAlloy(t, "testdata")
}
