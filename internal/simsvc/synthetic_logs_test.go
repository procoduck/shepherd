package simsvc

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fixturePath is the only place a fixture name becomes a file path, so the
// confinement lives there rather than in every caller. NewLogEmitter already
// rejects names outside the fixture library; these specs pin the second
// line of defence on its own (CodeQL go/path-injection): a name that would
// resolve outside the run's log dir is refused even if it were ever
// admitted upstream. Red run: replacing fixturePath's body with a bare
// filepath.Join fails every "refuses" case below.
var _ = Describe("LogEmitter fixture paths", func() {
	dir := filepath.Join("run", "logs")

	It("resolves a library fixture to <dir>/<name>.log", func() {
		p, err := fixturePath(dir, "docker-logs")
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(filepath.Join(dir, "docker-logs.log")))
	})

	DescribeTable("refuses a name that is not a bare file name inside the dir",
		func(name string) {
			_, err := fixturePath(dir, name)
			Expect(err).To(HaveOccurred(), "fixture %q must not become a path", name)
		},
		Entry("parent traversal", "../escape"),
		Entry("nested traversal", "a/../../escape"),
		Entry("subdirectory", "sub/docker-logs"),
		Entry("absolute", "/etc/passwd"),
		Entry("dot", "."),
		Entry("dot-dot", ".."),
	)

	It("NewLogEmitter refuses a name outside the fixture library before any path exists", func() {
		_, err := NewLogEmitter(dir, []string{"../escape"}, 0)
		Expect(err).To(MatchError(ContainSubstring("unknown log fixture")))
	})
})
