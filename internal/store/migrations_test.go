package store_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/testutil"
)

var sharedPG *testutil.SharedPostgres

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("Migration up→down→up cycle", Label("integration"), func() {
	It("applies, rolls back, and re-applies migrations cleanly", func() {
		ctx := context.Background()

		// Use the shared container's root URL for migration tests.
		url := sharedPG.RootURL

		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		Expect(store.MigrateDown(ctx, url)).To(Succeed())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
	})
})
