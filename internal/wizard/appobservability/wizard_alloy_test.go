package appobservability_test

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

// The wizard's own goldens are a byte comparison against text this package
// produced, so they agree with whatever it emits — including things the real
// binary refuses. That is how `env("...")` shipped: deprecated in Alloy
// v1.18.1, rejected by `alloy validate`, and invisible to a golden diff and to
// the wizard's own "No problems" preview alike.
//
// This runs the generated config through the real binary instead. It is the
// same shape as internal/visual/render_test.go's shim, and deliberately NOT
// self-skipping when the binary is missing: it pulls the pinned image.
var _ = Describe("Wizard output against the real Alloy binary", func() {
	It("validates every committed golden", func() {
		bin := alloyBinary()
		if bin == "" {
			Fail("no alloy binary and no docker to run the pinned image — this guard must not silently pass")
		}
		v := validate.New(&config.ValidateConfig{
			AlloyBinary:    bin,
			StabilityLevel: "experimental",
			Timeout:        2 * time.Minute,
		})

		goldens, err := filepath.Glob(filepath.Join("testdata", "*.golden.alloy"))
		Expect(err).NotTo(HaveOccurred())
		Expect(goldens).NotTo(BeEmpty(), "no goldens found — the guard would pass vacuously")

		for _, g := range goldens {
			content, readErr := os.ReadFile(g)
			Expect(readErr).NotTo(HaveOccurred())

			res := v.Stages12(context.Background(), string(content))
			Expect(res.Valid).To(BeTrue(),
				"%s is not accepted by alloy %s:\n%s\n--- config ---\n%s",
				g, version.AlloySchemaVersion, res.Diagnostics, content)
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

func dockerAlloyShim() string {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return ""
	}
	// Same derivation as internal/visual/render_test.go: one pinned version.
	image := "grafana/alloy:" + strings.TrimPrefix(version.AlloySchemaVersion, "alloy-")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, docker, "image", "inspect", image).Run(); err != nil {
		if err := exec.CommandContext(ctx, docker, "pull", "--quiet", image).Run(); err != nil {
			return ""
		}
	}
	dir, err := os.MkdirTemp("", "shepherd-wizard-alloy-")
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "alloy")
	// internal/validate always passes the config path last; mount its directory
	// read-only and validate inside the image.
	script := fmt.Sprintf(`#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
dir=$(cd "$(dirname "$last")" && pwd -P)
exec %q run --rm -v "$dir:/work:ro" %s validate --stability.level=experimental "/work/$(basename "$last")"
`, docker, image)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return ""
	}
	return path
}
