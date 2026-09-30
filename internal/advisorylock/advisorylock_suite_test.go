package advisorylock_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAdvisorylock(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Advisorylock Suite")
}
