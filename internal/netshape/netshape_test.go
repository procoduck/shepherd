package netshape_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/netshape"
)

var _ = Describe("Hosts", func() {
	// The five shapes finding H6 measured walking through the simulator's
	// allowlist, named one per entry so a regression says which one came back
	// rather than "the guard got worse".
	DescribeTable("finds the host in every shape that bypassed the old allowlist",
		func(literal string, want ...string) {
			Expect(netshape.Hosts(literal)).To(ConsistOf(want))
		},
		Entry("bracketed IPv6 host:port", `[2001:db8::1]:9090`, "2001:db8::1"),
		Entry("bare dotted name, no port at all", "broker.evil.example.com", "broker.evil.example.com"),
		Entry("a scheme that is not http(s)", "ftp://evil.example.com/x", "evil.example.com"),
		Entry("a host inside a MySQL DSN", "u:p@tcp(db.evil.example.com:3306)/x", "db.evil.example.com"),
		Entry("a host inside a Postgres URI", "postgres://u:p@db.evil.example.com:5432/s", "db.evil.example.com"),
		// The two shapes the old regex already caught, kept as controls: a
		// rewrite that lost them would be a regression the entries above
		// would not notice.
		Entry("plain http URL", "https://metrics.evil.example.com/api/v1/write", "metrics.evil.example.com"),
		Entry("bare host:port", "otel.evil.example.com:4317", "otel.evil.example.com"),
		// An IP literal is the most direct address form there is, and the
		// letter-final-label rule that keeps bare names from firing on every
		// file name would otherwise miss it.
		Entry("dotted-quad IPv4", "203.0.113.7", "203.0.113.7"),
		Entry("dotted-quad IPv4 with a port", "203.0.113.7:9090", "203.0.113.7"),
		Entry("scheme URL with an IPv6 host", "http://[2001:db8::2]:9090/write", "2001:db8::2"),
	)

	It("reports both hosts of a literal that names two, so one allowed host cannot cover the other", func() {
		Expect(netshape.Hosts("http://simulator:9110/proxy?to=other.evil.example.com:8080")).
			To(ConsistOf("simulator", "other.evil.example.com"))
	})

	// The other half of the contract. A gate that answers "names a host" for
	// ordinary pipeline text does not fail closed, it fails constantly — and a
	// simulation refused for a relabel regex is a bug users cannot work around.
	// Every value below is one the shipped corpus or the stub fixtures really
	// render.
	DescribeTable("finds no host in the ordinary text a transformed config is made of",
		func(literal string) {
			Expect(netshape.Hosts(literal)).To(BeEmpty())
		},
		Entry("a relabel regex with a metacharacter", "go_.*"),
		Entry("a relabel regex anchored on a dot", ".*grpc.*"),
		Entry("a meta label name", "__meta_kubernetes_pod_phase"),
		Entry("a job name", "app"),
		Entry("a duration", "30s"),
		Entry("a clock-shaped value", "12:30"),
		Entry("a cluster label", "eu-1"),
		Entry("a metrics path", "/metrics"),
		Entry("a scrape protocol with a dotted version", "OpenMetricsText1.0.0"),
		Entry("a content type", "text/plain;version=0.0.4"),
		Entry("a semantic version", "1.18.1"),
		Entry("a stub fixture log path", "/tmp/shepherd-sim/logs/file-logs.log"),
		Entry("a capture file path", "/tmp/shepherd-sim/capture/sink.json"),
		Entry("an empty string", ""),
	)

	It("lowercases and de-duplicates, so an allowlist comparison cannot be defeated by case", func() {
		Expect(netshape.Hosts("HTTP://Evil.Example.COM/a http://evil.example.com/b")).
			To(ConsistOf("evil.example.com"))
	})

	It("reports the raw match for a URL that will not parse, rather than treating it as hostless", func() {
		hosts := netshape.Hosts("http://%zz-not-a-url/")
		Expect(hosts).NotTo(BeEmpty())
	})
})
