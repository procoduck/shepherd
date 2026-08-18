package simulate_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
)

var _ = Describe("RelabelTrace", func() {
	It("keeps only annotated pods with keep rule", func() {
		result, err := simulate.RelabelTrace(simulate.RelabelRequest{Rules: []simulate.RelabelRule{{SourceLabels: []string{"__meta_kubernetes_pod_annotation_prometheus_io_scrape"}, Regex: "true", Action: "keep"}}, SampleTargets: simulate.BuiltinRelabelTargets()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Kept).To(BeTrue())
		Expect(result.Traces[1].Kept).To(BeFalse())
		Expect(result.Traces[2].Kept).To(BeTrue())
	})

	It("replaces pod names with instance labels", func() {
		result, err := simulate.RelabelTrace(simulate.RelabelRequest{Rules: []simulate.RelabelRule{{SourceLabels: []string{"__meta_kubernetes_pod_name"}, TargetLabel: "instance", Action: "replace"}}, SampleTargets: simulate.BuiltinRelabelTargets()})
		Expect(err).NotTo(HaveOccurred())
		for _, trace := range result.Traces {
			Expect(trace.Kept).To(BeTrue())
			Expect(trace.Output).To(HaveKey("instance"))
		}
	})

	It("drops meta labels", func() {
		result, err := simulate.RelabelTrace(simulate.RelabelRequest{Rules: []simulate.RelabelRule{{Regex: "__meta_.*", Action: "labeldrop"}}, SampleTargets: simulate.BuiltinRelabelTargets()})
		Expect(err).NotTo(HaveOccurred())
		for _, trace := range result.Traces {
			Expect(trace.Output).NotTo(HaveKey("__meta_kubernetes_pod_name"))
		}
	})

	It("records per-rule label state", func() {
		result, err := simulate.RelabelTrace(simulate.RelabelRequest{Rules: []simulate.RelabelRule{{SourceLabels: []string{"__meta_kubernetes_pod_name"}, TargetLabel: "instance", Action: "replace"}, {Regex: "__meta_.*", Action: "labeldrop"}}, SampleTargets: simulate.BuiltinRelabelTargets()})
		Expect(err).NotTo(HaveOccurred())
		for _, trace := range result.Traces {
			Expect(trace.Steps).To(HaveLen(2))
		}
	})
})

var _ = Describe("LogTrace", func() {
	It("parses JSON and labels fields", func() {
		result, err := simulate.LogTrace(simulate.LogsRequest{Stages: []simulate.StageSpec{{Type: "json"}, {Type: "labels", Labels: map[string]string{"level": "level", "status": "status"}}}, SampleLines: []string{simulate.BuiltinLogLines()[0]}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Steps[1].LabelsAfter).To(And(HaveKeyWithValue("level", "info"), HaveKeyWithValue("status", "200")))
	})

	It("parses logfmt", func() {
		result, err := simulate.LogTrace(simulate.LogsRequest{Stages: []simulate.StageSpec{{Type: "logfmt"}, {Type: "labels", Labels: map[string]string{"level": "level"}}}, SampleLines: []string{simulate.BuiltinLogLines()[1]}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Steps[1].LabelsAfter).To(HaveKeyWithValue("level", "info"))
	})

	It("drops matching lines", func() {
		result, err := simulate.LogTrace(simulate.LogsRequest{Stages: []simulate.StageSpec{{Type: "logfmt"}, {Type: "drop", DropValue: "info"}}, SampleLines: []string{simulate.BuiltinLogLines()[1]}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Dropped).To(BeTrue())
	})

	It("renders unsupported stages as not_simulated", func() {
		result, err := simulate.LogTrace(simulate.LogsRequest{Stages: []simulate.StageSpec{{Type: "timestamp"}}, SampleLines: []string{"line"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Steps[0].Simulated).To(BeFalse())
		Expect(result.Traces[0].Steps[0].Note).To(ContainSubstring("not_simulated"))
	})
})
