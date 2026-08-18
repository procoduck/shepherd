package simulate_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSimulate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Simulate Suite")
}
