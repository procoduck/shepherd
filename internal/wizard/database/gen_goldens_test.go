//go:build ignore

package database_test

// This file is used to regenerate golden files via:
//   go test -run=TestGenGoldens ./internal/wizard/database/  (after temporarily
//   stripping this build tag — `-tags ignore` itself breaks this repo's
//   toolchain, see podlogs' sibling file).
// Not part of the normal test suite. The cases live in cases_test.go.

import (
	"os"
	"testing"

	db "shepherd/internal/wizard/database"
	"shepherd/internal/wizard/wizardtest"
)

func TestGenGoldens(t *testing.T) {
	w := &db.Wizard{}
	for _, c := range goldenCases {
		res, err := w.Commit(c.state, wizardtest.Destinations())
		if err != nil {
			t.Fatal(err)
		}
		path := "testdata/" + c.name + ".golden.alloy"
		if err := os.WriteFile(path, []byte(res.Contents), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote", path)
	}
}
