package clustermetrics_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/wizard"
	"shepherd/internal/wizard/wizardtest"
)

// #299: the catalog card listed kubelet and kube-state-metrics, but the wizard
// renders a cAdvisor exporter only. Every metrics source the description names
// must appear in what the wizard renders.
var _ = Describe("ClusterMetricsWizard catalog description (#299)", func() {
	wiz, getErr := wizard.Default().Get("cluster-metrics")
	Expect(getErr).NotTo(HaveOccurred())

	It("names only the metrics sources the wizard renders", func() {
		result, err := wiz.Commit(map[string]any{"metrics_dest_name": "prom-prod"}, wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		desc := strings.ToLower(wiz.Schema().Description)
		for term, marker := range map[string]string{
			"cadvisor":           "prometheus.exporter.cadvisor",
			"kubelet":            "kubelet",
			"kube-state-metrics": "kube_state",
		} {
			if strings.Contains(desc, term) {
				Expect(result.Contents).To(ContainSubstring(marker),
					"the description names %s but the wizard renders no %s", term, marker)
			}
		}
		Expect(desc).To(ContainSubstring("cadvisor"))
	})
})
