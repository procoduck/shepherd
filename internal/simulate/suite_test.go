package simulate_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/testutil"
)

func TestSimulate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Simulate Suite")
}

// sharedPG backs worker_test.go's RunWorker specs (Label("integration")):
// the transform/policy/fixture specs elsewhere in this package need no
// database and are unaffected by this suite-level setup running once.
var sharedPG *testutil.SharedPostgres

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})
