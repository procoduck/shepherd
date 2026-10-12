package blackbox_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/wizard"
	"shepherd/internal/wizard/wizardtest"
)

// #299: the catalog card promised DNS probes and alerting, neither of which
// this wizard renders. Each probe type the description names must be a module
// the wizard renders, and it must not promise alerting (the pipeline ships
// probe metrics; alert rules live in the metrics backend).
var _ = Describe("BlackboxWizard catalog description (#299)", func() {
	wiz, getErr := wizard.Default().Get("blackbox")
	Expect(getErr).NotTo(HaveOccurred())
	desc := wiz.Schema().Description

	It("names only probe types the wizard can render", func() {
		rendered := ""
		for _, module := range []string{"http_2xx", "tcp_connect"} {
			result, err := wiz.Commit(map[string]any{
				"probe_targets": "https://example.com", "module": module, "metrics_dest_name": "prom-prod",
			}, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			rendered += result.Contents
		}
		for term, prober := range map[string]string{"HTTP": "http", "TCP": "tcp", "DNS": "dns", "ICMP": "icmp"} {
			if strings.Contains(desc, term) {
				Expect(rendered).To(ContainSubstring("prober: "+prober),
					"the description names %s probes but no module renders prober %s", term, prober)
			}
		}
	})

	It("does not promise alerting", func() {
		Expect(strings.ToLower(desc)).NotTo(ContainSubstring("alert"))
	})
})
