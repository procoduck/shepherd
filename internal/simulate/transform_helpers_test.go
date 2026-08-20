package simulate_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/simulate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// Everything here loads the SHIPPED artifact. Hand-written schema fixtures are
// the documented root cause of the visual builder's test blindness
// (docs/reviews/README.md): three of them invert the port model and every suite
// built on them stayed green while nothing worked. The transform is the safety
// boundary, so it is only ever tested against the schema that actually ships.

const corpusDir = "../visual/testdata/corpus"

func shippedSchema() (visual.SchemaPayload, simulate.Policy) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	merged, _, err := reg.Get(version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())

	b, err := json.Marshal(merged)
	Expect(err).NotTo(HaveOccurred())
	var payload visual.SchemaPayload
	Expect(json.Unmarshal(b, &payload)).To(Succeed())

	policy, err := simulate.LoadPolicy(merged)
	Expect(err).NotTo(HaveOccurred())

	return payload, policy
}

func corpusNames() []string {
	entries, err := filepath.Glob(filepath.Join(corpusDir, "*.graph.json"))
	Expect(err).NotTo(HaveOccurred())
	Expect(entries).NotTo(BeEmpty(), "the shared golden corpus must be present")
	names := make([]string, 0, len(entries))
	for _, path := range entries {
		names = append(names, filepath.Base(path[:len(path)-len(".graph.json")]))
	}
	return names
}

func corpusGraph(name string) visual.GraphDocument {
	data, err := os.ReadFile(filepath.Join(corpusDir, name+".graph.json"))
	Expect(err).NotTo(HaveOccurred())
	var doc visual.GraphDocument
	Expect(json.Unmarshal(data, &doc)).To(Succeed())
	return doc
}

// testHarness is deliberately not the compose default: every address here is an
// example host, and two of the specs run a second, different harness to prove
// no address is baked into the transform.
func testHarness() simulate.HarnessEndpoints {
	return simulate.HarnessEndpoints{
		CaptureBaseURL:  "http://simulator.example.com:9110",
		OTLPGRPCAddress: "simulator.example.com:4317",
		SyslogHost:      "simulator.example.com",
		SyslogPort:      5514,
		CaptureDir:      "/sim/capture",
		TargetAddress:   "simulator.example.com:9111",
		LogDir:          "/sim/logs",
	}
}

func altHarness() simulate.HarnessEndpoints {
	return simulate.HarnessEndpoints{
		CaptureBaseURL:  "http://other.example.org:8110",
		OTLPGRPCAddress: "other.example.org:14317",
		SyslogHost:      "other.example.org",
		SyslogPort:      6514,
		CaptureDir:      "/other/capture",
		TargetAddress:   "other.example.org:8111",
		LogDir:          "/other/logs",
	}
}

func transformErrors(err error) simulate.TransformErrors {
	Expect(err).To(HaveOccurred())
	errs, ok := err.(simulate.TransformErrors) //nolint:errorlint // TransformErrors is returned by value, never wrapped
	Expect(ok).To(BeTrue(), "Transform must return TransformErrors, got %T", err)
	return errs
}

func errorCodes(errs simulate.TransformErrors) []string {
	codes := make([]string, 0, len(errs))
	for _, e := range errs {
		codes = append(codes, e.Code)
	}
	return codes
}

func rewriteKinds(rs []simulate.Rewrite) []simulate.RewriteKind {
	kinds := make([]simulate.RewriteKind, 0, len(rs))
	for i := range rs {
		kinds = append(kinds, rs[i].Kind)
	}
	return kinds
}

func nodeByID(doc visual.GraphDocument, id string) visual.GraphNode {
	for _, n := range doc.Nodes {
		if n.ID == id {
			return n
		}
	}
	Expect(false).To(BeTrue(), "node %q is missing from the transformed graph", id)
	return visual.GraphNode{}
}
