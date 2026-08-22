// Package wizardtest holds test-only helpers shared by every wizard catalog
// package under internal/wizard/**. It exists so each wizard package's
// "validate every golden against the real pinned Alloy binary" test
// (mirroring internal/receiver/alloy_validate_test.go and
// internal/wizard/appobservability/wizard_alloy_test.go, the pattern this
// package generalizes) does not re-implement the same binary-or-docker-shim
// discovery logic five times over. A change to how the pinned binary is
// located — a new fallback, a different pinned tag — lands once here
// instead of once per wizard.
package wizardtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"shepherd/internal/version"
)

var (
	alloyOnce sync.Once
	alloyPath string
)

// AlloyBinary returns a path to something that behaves like the pinned
// `alloy` binary's `validate` subcommand: a real binary on PATH, an
// operator-supplied override, or a generated shim script that runs the
// pinned `grafana/alloy` image via docker. It returns "" only when none of
// those are available, which the caller MUST treat as a hard test failure
// (Fail), never a skip — see this function's callers' doc comments for why
// silently skipping is exactly the failure class that let a deprecated
// `env(...)` call ship undetected once already (appobservability's
// wizard_alloy_test.go).
func AlloyBinary() string {
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

// dockerAlloyShim pulls the pinned grafana/alloy image (same derivation as
// internal/visual/render_test.go and appobservability/wizard_alloy_test.go:
// one image tag, stripped of the "alloy-" prefix version.AlloySchemaVersion
// carries) and writes a small script that runs `alloy validate` inside a
// throwaway container, mounting the config's directory read-only. Returns
// "" if docker itself is unavailable or the pull fails — AlloyBinary()'s
// caller decides what "" means, this function only reports availability.
func dockerAlloyShim() string {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return ""
	}
	image := "grafana/alloy:" + strings.TrimPrefix(version.AlloySchemaVersion, "alloy-")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	//nolint:gosec // docker's path comes from exec.LookPath, not user input;
	// image is grafana/alloy:<pinned version constant>, never operator text.
	if err := exec.CommandContext(ctx, docker, "image", "inspect", image).Run(); err != nil {
		//nolint:gosec // same docker/image provenance as the inspect call above.
		if err := exec.CommandContext(ctx, docker, "pull", "--quiet", image).Run(); err != nil {
			return ""
		}
	}
	dir, err := os.MkdirTemp("", "shepherd-wizard-alloy-")
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
	//nolint:gosec // must be executable to run as a shim binary below;
	// owner-only (0700) in a MkdirTemp'd directory, never group/world-writable.
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return ""
	}
	return path
}
