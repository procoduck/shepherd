// The simsvc specs live in the package under test rather than in simsvc_test:
// the wire decoders, the run queue's slot bookkeeping and the capture sink are
// unexported by design (nothing outside this service has any business calling
// them), and testing them through the HTTP surface alone would leave the
// golden-bytes assertions unable to name what they decoded.
package simsvc

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSimsvc(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Simsvc Suite")
}
