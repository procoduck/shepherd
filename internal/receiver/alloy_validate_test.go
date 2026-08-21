package receiver_test

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
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// This suite is the "alloy validate is not alloy run" anchor for this
// package, the same shape as internal/visual/render_test.go's "Corpus goldens
// are real Alloy" suite and internal/wizard/appobservability's
// wizard_alloy_test.go. It deliberately Fails (never Skips) when neither a
// local alloy binary nor a usable Docker image is available — a missing
// binary must not silently turn off the only check that proves rendered
// output is config the pinned Alloy actually accepts, rather than text that
// merely looks like Alloy syntax.
var _ = Describe("Rendered output against the real Alloy binary", func() {
	It("validates every committed golden, declare-wrapped exactly as served", func() {
		bin := alloyBinary()
		if bin == "" {
			if os.Getenv("SHEPHERD_SKIP_ALLOY_VALIDATE") != "" {
				Skip("SHEPHERD_SKIP_ALLOY_VALIDATE set: skipping without an alloy binary or usable docker image " + alloyImage())
			}
			Fail("no alloy binary and no usable docker image " + alloyImage() + " to run stage 2 validate; " +
				"install/start docker (or `docker pull " + alloyImage() + "`), set SHEPHERD_TEST_ALLOY_BINARY to a local " +
				"alloy binary, or set SHEPHERD_SKIP_ALLOY_VALIDATE=1 to explicitly opt out")
		}
		v := validate.New(&config.ValidateConfig{
			AlloyBinary:    bin,
			StabilityLevel: "experimental",
			Timeout:        2 * time.Minute,
		})

		goldens, err := filepath.Glob(filepath.Join("testdata", "*.golden.alloy"))
		Expect(err).NotTo(HaveOccurred())
		Expect(goldens).NotTo(BeEmpty(), "no goldens found — the guard would pass vacuously")

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		for _, g := range goldens {
			content, readErr := os.ReadFile(g)
			Expect(readErr).NotTo(HaveOccurred())

			// Validate exactly what a collector is served: the
			// declare-wrapped form, same as internal/merge produces.
			wrapped := validate.WrapForValidation("receiver-"+strings.TrimSuffix(filepath.Base(g), ".golden.alloy"), string(content))
			res := v.Stages12(ctx, wrapped)
			Expect(res.Valid).To(BeTrue(),
				"%s is not accepted by alloy %s:\n%s\n--- config ---\n%s",
				g, version.AlloySchemaVersion, res.Diagnostics, wrapped)
		}
	})
})

var (
	alloyOnce sync.Once
	alloyPath string
)

// alloyBinary prefers a real binary and falls back to a shim that runs the
// pinned image, so CI without Alloy installed still exercises this.
func alloyBinary() string {
	alloyOnce.Do(func() {
		if env := os.Getenv("SHEPHERD_TEST_ALLOY_BINARY"); env != "" {
			alloyPath = env
			return
		}
		if p, err := exec.LookPath("alloy"); err == nil {
			alloyPath = p
			return
		}
		alloyPath = dockerAlloyShim()
	})
	return alloyPath
}

// alloyImage is the container image for the pinned schema version:
// version.AlloySchemaVersion is "alloy-v1.18.1", the image tag is "v1.18.1".
func alloyImage() string {
	return "grafana/alloy:" + strings.TrimPrefix(version.AlloySchemaVersion, "alloy-")
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
	dir, err := os.MkdirTemp("", "shepherd-receiver-alloy-")
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "alloy")
	// internal/validate always passes the config path last; mount its
	// directory read-only and validate inside the image.
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
