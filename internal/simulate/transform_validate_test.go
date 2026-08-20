package simulate_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/simulate"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// §7.4 item: the transformed config must itself pass stages 1–2 (§6.4 step 2).
// This is the only test that can catch a stub which renders but does not run —
// most concretely, anyone who "corrects" the static stub back to §6.4's literal
// discovery.static, which Alloy v1.18.1 has never shipped. Nothing else in the
// suite executes the real binary against renderer output.
//
// Since rule K became deny-by-default it carries a second load: it is the guard
// against the allowlist silently becoming deny-EVERYTHING. A keep list that
// omits an attribute Alloy requires fails here, against the real binary, rather
// than in a user's sandbox run — which is why the binary is now found via the
// docker shim internal/visual/render_test.go already uses, instead of the spec
// skipping itself whenever `alloy` is not on PATH (which was always, in CI).
var _ = Describe("Transform: the transformed config validates", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// Stage 1 is the real Alloy syntax parser compiled in, so it needs no
	// binary and runs everywhere, on every corpus entry, always.
	It("parses: every transformed corpus graph passes stage 1", func() {
		for _, name := range corpusNames() {
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "corpus entry %q", name)

			content := visual.Render(result.Graph, payload).Content
			stage1 := validate.Stage1(content)
			Expect(stage1.Valid).To(BeTrue(),
				"corpus entry %q failed stage 1: %+v\n%s", name, stage1.Diagnostics, content)
		}
	})

	// Stage 2 shells out to the pinned Alloy binary. It is labelled so CI can
	// require it; it must not be dropped from the required set, because it is
	// the only guard against a stub component the binary does not have and the
	// only guard against a keep list that drops a required attribute.
	It("runs: every transformed corpus graph passes stages 1-2 against the real binary", Label("needs-alloy-binary"), func() {
		binary := findAlloyBinary()
		if binary == "" {
			// An explicit opt-out, never a silent self-skip: a spec that
			// disappears when its dependency is missing is how this suite came
			// to have zero real-binary coverage in CI (finding 11).
			if os.Getenv("SHEPHERD_SKIP_ALLOY_VALIDATE") != "" {
				Skip("SHEPHERD_SKIP_ALLOY_VALIDATE set: skipping without an alloy binary or usable docker image " + alloyImage())
			}
			Fail("no alloy binary and no usable docker image " + alloyImage() + " to run stage 2 validate; " +
				"install/start docker (or `docker pull " + alloyImage() + "`), set SHEPHERD_VALIDATE_ALLOY_BINARY to a local " +
				"binary, or set SHEPHERD_SKIP_ALLOY_VALIDATE=1 to opt out deliberately")
		}
		validator := validate.New(&config.ValidateConfig{
			AlloyBinary: binary, StabilityLevel: "experimental", Timeout: 90 * time.Second,
		})

		for _, name := range corpusNames() {
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "corpus entry %q", name)

			content := visual.Render(result.Graph, payload).Content
			res := validator.Stages12(context.Background(), validate.WrapForValidation("sim_"+name, content))
			Expect(res.Valid).To(BeTrue(),
				"corpus entry %q failed stages 1-2: %+v\n%s", name, res.Diagnostics, content)
		}
	})
})

// alloyImage is the container image for the pinned schema version.
func alloyImage() string {
	return "grafana/alloy:" + strings.TrimPrefix(version.AlloySchemaVersion, "alloy-")
}

var (
	alloyBinaryOnce sync.Once
	alloyBinaryPath string
)

// findAlloyBinary returns a path that behaves like `alloy validate ... <file>`,
// or "" when neither a binary nor Docker is available. It mirrors
// internal/visual/render_test.go: the docker fallback is a shim script so that
// internal/validate's real Stage2 code path — temp file, exec, output parsing —
// is what runs, rather than a test-only imitation.
func findAlloyBinary() string {
	alloyBinaryOnce.Do(func() {
		for _, env := range []string{"SHEPHERD_VALIDATE_ALLOY_BINARY", "SHEPHERD_TEST_ALLOY_BINARY"} {
			if path := os.Getenv(env); path != "" {
				alloyBinaryPath = path
				return
			}
		}
		for _, candidate := range []string{"alloy", "/usr/local/bin/alloy"} {
			if p, err := exec.LookPath(candidate); err == nil {
				alloyBinaryPath = p
				return
			}
		}
		alloyBinaryPath = dockerAlloyShim()
	})
	return alloyBinaryPath
}

func dockerAlloyShim() string {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return ""
	}
	image := alloyImage()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, docker, "image", "inspect", image).Run(); err != nil {
		if err := exec.CommandContext(ctx, docker, "pull", "--quiet", image).Run(); err != nil {
			return ""
		}
	}

	dir, err := os.MkdirTemp("", "shepherd-alloy-shim-")
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "alloy")
	// The last argument is always the config path (internal/validate builds the
	// command); mount its directory read-only and run validate inside the image.
	script := fmt.Sprintf(`#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
dir=$(cd "$(dirname "$last")" && pwd -P)
exec %q run --rm -v "$dir:/work:ro" %s validate --stability.level=experimental "/work/$(basename "$last")"
`, docker, image)
	// 0o700: the file has to be executable — internal/validate exec's it.
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return ""
	}
	return path
}
