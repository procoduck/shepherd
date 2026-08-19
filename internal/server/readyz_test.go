package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeRow is a minimal pgx.Row backed by a closure, so tests can control what
// QueryRow.Scan populates without a real database.
type fakeRow struct {
	scan func(dest ...any) error
}

func (r fakeRow) Scan(dest ...any) error {
	return r.scan(dest...)
}

// fakeDB is a dbPinger test double.
type fakeDB struct {
	pingErr error
	scanErr error
	version uint64
	dirty   bool
}

func (f *fakeDB) Ping(_ context.Context) error { return f.pingErr }

func (f *fakeDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fakeRow{scan: func(dest ...any) error {
		if f.scanErr != nil {
			return f.scanErr
		}
		*dest[0].(*uint64) = f.version //nolint:errcheck // test double, shape is fixed by the caller
		*dest[1].(*bool) = f.dirty     //nolint:errcheck // test double, shape is fixed by the caller
		return nil
	}}
}

func doReadyz(db dbPinger, latestMigration uint64) (*httptest.ResponseRecorder, readyzResult) {
	h := handleReadyz(db, latestMigration)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	var body readyzResult
	Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
	return rec, body
}

var _ = Describe("handleReadyz", func() {
	It("returns 200 ok when the database is reachable and migrations are current", func() {
		rec, body := doReadyz(&fakeDB{version: 5, dirty: false}, 5)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(body.Status).To(Equal("ok"))
		Expect(body.Checks["database"]).To(Equal("ok"))
		Expect(body.Checks["migrations"]).To(Equal("ok"))
	})

	It("returns 503 when the database ping fails", func() {
		rec, body := doReadyz(&fakeDB{pingErr: errors.New("connection refused")}, 5)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(body.Status).To(Equal("error"))
		Expect(body.Checks["database"]).To(ContainSubstring("connection refused"))
		Expect(body.Checks).NotTo(HaveKey("migrations"))
	})

	It("returns 503 when the schema is behind the embedded migrations", func() {
		rec, body := doReadyz(&fakeDB{version: 3, dirty: false}, 5)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(body.Status).To(Equal("error"))
		Expect(body.Checks["migrations"]).To(ContainSubstring("pending"))
	})

	It("returns 503 when the migration state is dirty", func() {
		rec, body := doReadyz(&fakeDB{version: 5, dirty: true}, 5)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(body.Checks["migrations"]).To(Equal("dirty"))
	})

	It("does not fail readiness when migration state can't be determined", func() {
		rec, body := doReadyz(&fakeDB{scanErr: errors.New(`relation "schema_migrations" does not exist`)}, 5)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(body.Status).To(Equal("ok"))
		Expect(body.Checks["migrations"]).To(Equal("unknown"))
	})

	It("skips the pending-migration check when the latest version is unknown", func() {
		rec, body := doReadyz(&fakeDB{version: 0, dirty: false}, 0)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(body.Checks["migrations"]).To(Equal("ok"))
	})
})

var _ = Describe("latestMigrationVersion", func() {
	It("parses the highest version from the embedded migration filenames", func() {
		v, err := latestMigrationVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeNumerically(">=", 5)) // at least 0005_visual_source.up.sql exists today
	})
})
