package simulate_test

import (
	"encoding/json"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/prometheus/model/value"

	"shepherd/internal/simulate"
)

var _ = Describe("SampleValue wire encoding", func() {
	roundTrip := func(in float64) (string, float64) {
		GinkgoHelper()
		raw, err := json.Marshal(simulate.SampleValue(in))
		Expect(err).NotTo(HaveOccurred())
		var out simulate.SampleValue
		Expect(json.Unmarshal(raw, &out)).To(Succeed())
		return string(raw), float64(out)
	}

	DescribeTable("finite values stay JSON numbers and round-trip exactly",
		func(in float64, wire string) {
			raw, out := roundTrip(in)
			Expect(raw).To(Equal(wire))
			Expect(out).To(Equal(in))
		},
		Entry("zero", 0.0, "0"),
		Entry("integer", 7.0, "7"),
		Entry("fraction", 0.25, "0.25"),
		Entry("negative", -3.5, "-3.5"),
		Entry("max float", math.MaxFloat64, "1.7976931348623157e+308"),
		Entry("smallest denormal", math.SmallestNonzeroFloat64, "5e-324"),
	)

	It("encodes NaN as the string \"NaN\" and decodes it back to a NaN that is not the stale marker", func() {
		raw, out := roundTrip(math.NaN())
		Expect(raw).To(Equal(`"NaN"`))
		Expect(math.IsNaN(out)).To(BeTrue())
		Expect(value.IsStaleNaN(out)).To(BeFalse())
	})

	It("encodes +Inf and -Inf as \"+Inf\" and \"-Inf\"", func() {
		raw, out := roundTrip(math.Inf(1))
		Expect(raw).To(Equal(`"+Inf"`))
		Expect(math.IsInf(out, 1)).To(BeTrue())

		raw, out = roundTrip(math.Inf(-1))
		Expect(raw).To(Equal(`"-Inf"`))
		Expect(math.IsInf(out, -1)).To(BeTrue())
	})

	It("keeps the Prometheus stale marker distinguishable from an ordinary NaN", func() {
		raw, out := roundTrip(math.Float64frombits(value.StaleNaN))
		Expect(raw).To(Equal(`"StaleNaN"`))
		Expect(value.IsStaleNaN(out)).To(BeTrue())
		Expect(math.Float64bits(out)).To(Equal(value.StaleNaN))
	})

	It("round-trips a series carrying non-finite values through the client's wire type", func() {
		in := simulate.ClientSeries{
			Name: "prometheus_target_interval_length_seconds", SampleCount: 2,
			FirstValue: simulate.SampleValue(math.NaN()), LastValue: simulate.SampleValue(math.Inf(1)),
		}
		raw, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"first_value":"NaN"`))
		Expect(string(raw)).To(ContainSubstring(`"last_value":"+Inf"`))

		var out simulate.ClientSeries
		Expect(json.Unmarshal(raw, &out)).To(Succeed())
		Expect(math.IsNaN(float64(out.FirstValue))).To(BeTrue())
		Expect(math.IsInf(float64(out.LastValue), 1)).To(BeTrue())
	})

	It("still decodes the plain-number shape an older simulator sends", func() {
		var out simulate.ClientSeries
		Expect(json.Unmarshal([]byte(`{"first_value":3,"last_value":1e3}`), &out)).To(Succeed())
		Expect(float64(out.FirstValue)).To(Equal(3.0))
		Expect(float64(out.LastValue)).To(Equal(1000.0))
	})

	It("leaves the value unchanged on null, as encoding/json does for a float", func() {
		v := simulate.SampleValue(5)
		Expect(json.Unmarshal([]byte(`null`), &v)).To(Succeed())
		Expect(float64(v)).To(Equal(5.0))
	})

	DescribeTable("refuses anything else rather than decoding a silent zero",
		func(wire string) {
			var v simulate.SampleValue
			Expect(json.Unmarshal([]byte(wire), &v)).NotTo(Succeed())
		},
		Entry("unknown string", `"nan"`),
		Entry("empty string", `""`),
		Entry("boolean", `true`),
		Entry("object", `{}`),
	)
})
