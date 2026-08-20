// Package helm_test asserts, by actually running `helm template`, that the
// containment controls VB-1 §6.4 and finding H5 require on the S3 sandbox
// simulator's Kubernetes manifests are present and stay present. A control
// only described in a code comment is not a control — this is the same
// argument the S3 security review makes about untested egress claims,
// applied to the Helm chart.
package helm_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHelm(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Helm Chart Suite")
}
