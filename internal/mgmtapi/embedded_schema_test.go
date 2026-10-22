package mgmtapi_test

import (
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// embeddedSchema is the registry internal/server loads once at boot and hands
// to mgmtapi.MountRPC / mgmtapi.Router.
func embeddedSchema() *schema.Registry {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	return reg
}
