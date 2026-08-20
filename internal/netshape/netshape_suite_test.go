package netshape_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNetshape(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Netshape Suite")
}
