package receiver_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/receiver"
)

// Found by running the rendered config in Alloy (receiver tier PR 2): with
// send_batch_max_size omitted, Alloy applies its own non-zero default cap and
// refuses to start whenever send_batch_size exceeds it ("send_batch_max_size
// must be greater or equal to send_batch_size when not 0"). `alloy validate`
// accepts that config — only `alloy run` rejects it — and every golden
// fixture's sizes sit under the default, so nothing caught it.
var _ = Describe("batch processor sizing", func() {
	withBatch := func(b receiver.BatchConfig) receiver.Config {
		cfg := fixture("otlp-passthrough")
		cfg.OTLP[0].Batch = b
		return cfg
	}

	It("always renders send_batch_max_size, so Alloy's default cap never applies", func() {
		out, err := receiver.Render(withBatch(receiver.BatchConfig{Timeout: "5s", SendBatchSize: 8192}))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("send_batch_max_size = 0"))
	})

	It("refuses a max below the batch size, which Alloy would refuse at run time", func() {
		err := receiver.Validate(withBatch(receiver.BatchConfig{SendBatchSize: 8192, SendBatchMaxSize: 100}))
		Expect(err).To(MatchError(ContainSubstring("send_batch_max_size")))
	})

	It("accepts a max of 0 (no cap) or at least the batch size", func() {
		Expect(receiver.Validate(withBatch(receiver.BatchConfig{SendBatchSize: 8192}))).To(Succeed())
		Expect(receiver.Validate(withBatch(receiver.BatchConfig{SendBatchSize: 8192, SendBatchMaxSize: 8192}))).To(Succeed())
	})
})
