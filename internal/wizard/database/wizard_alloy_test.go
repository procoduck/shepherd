package database_test

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
//
// This wizard reads the connection string from the collector's environment
// (sys.env), and each exporter parses it while being built, so the container
// gets a representative value per golden variable — the shape an operator
// sets on the collector. The exporters do not connect at build time; these
// hosts are never dialled before the load completes.
func TestGoldensLoadInRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensLoadInRealAlloyWithEnv(t, "testdata", map[string]string{
		"APP_PG_DSN":     "postgresql://shepherd@db.example.com:5432/postgres?sslmode=disable",
		"APP_MYSQL_DSN":  "shepherd@tcp(db.example.com:3306)/",
		"APP_REDIS_ADDR": "redis.example.com:6379",
	})
}
