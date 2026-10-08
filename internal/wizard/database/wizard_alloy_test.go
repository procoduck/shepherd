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
// Each exporter parses its connection string while being built: an empty or
// malformed one refuses the collector's whole config ("cannot parse DSN:
// invalid connection protocol" — what the sys.env-only wizard did on every
// collector, since nothing set the variable). So the fake in-cluster API
// serves each Secret golden a representative DSN under the key the golden
// reads, and the env goldens get a value for their variable, the shape an
// operator stores. The exporters do not connect at build time; these hosts
// are never dialled before the load completes.
func TestGoldensLoadInRealAlloy(t *testing.T) {
	wizardtest.AssertGoldensLoadInRealAlloyWith(t, "testdata", wizardtest.LoadFixtures{
		SecretData: map[string]map[string]string{
			"databases/app-pg":    {"dsn": "postgresql://shepherd:s3cret@db.example.com:5432/postgres?sslmode=disable"},
			"databases/app-mysql": {"mysql-dsn": "shepherd:s3cret@tcp(db.example.com:3306)/"},
			"databases/app-redis": {"password": "s3cret"},
		},
		Env: map[string]string{
			"APP_PG_DSN":     "postgresql://shepherd@db.example.com:5432/postgres?sslmode=disable",
			"APP_MYSQL_DSN":  "shepherd@tcp(db.example.com:3306)/",
			"APP_REDIS_ADDR": "redis.example.com:6379",
		},
	})
}
