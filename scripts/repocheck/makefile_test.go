package repocheck_test

import (
	"os"
	"path/filepath"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Red run, 2026-09-10: `docs` was missing from .PHONY and a docs/ directory
// exists at the repo root, so `make -n docs` printed "make: 'docs' is up to
// date." and never invoked scripts/build-docs.py. check-docs-drift's error
// message told people to run a target that did nothing.
var _ = Describe("the Makefile", func() {
	It("declares the docs targets phony so a docs/ directory cannot satisfy them", func() {
		mk := readRepoFile("Makefile")
		for _, t := range []string{"docs", "check-docs-drift", "check-docs-version"} {
			Expect(mk).To(MatchRegexp(`(?m)^\.PHONY:.*\b`+t+`\b`), t)
		}
	})

	It("invokes the site generator from make docs even though docs/ exists", func() {
		out, err := runMake("-n", "docs")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("scripts/build-docs.py"))
		Expect(out).NotTo(ContainSubstring("is up to date"))
		Expect(makeRecipe("docs")).To(ContainSubstring("build-docs.py"))
	})
})

// Red run, 2026-09-10: `shepherd hash-password` is not a registered CLI
// subcommand (internal/cli/*.go registers serve, migrate, validate,
// healthcheck, version, dev, token only) and SHEPHERD_AUTH_LOCAL_ADMIN_ENABLED
// / _PASSWORD_HASH are read nowhere in the binary. The real bootstrap path
// (internal/auth/localusers.go BootstrapAdmin, called from server.go at
// startup) reads SHEPHERD_BOOTSTRAP_ADMIN_LOGIN / SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD
// instead, so `make smoke` was invoking a CLI subcommand and env vars that no
// longer exist and its local-admin-login step could never have passed.
var _ = Describe("the smoke target", func() {
	It("uses the bootstrap admin env vars, not the removed hash-password CLI", func() {
		recipe := makeRecipe("smoke")
		Expect(recipe).NotTo(ContainSubstring("hash-password"))
		Expect(recipe).NotTo(ContainSubstring("SHEPHERD_AUTH_LOCAL_ADMIN"))
		Expect(recipe).To(ContainSubstring("SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD"))
		Expect(recipe).To(ContainSubstring("SHEPHERD_BOOTSTRAP_ADMIN_LOGIN"))
	})

	It("reuses the local and init images instead of building shepherd:smoke tags", func() {
		recipe := makeRecipe("smoke")
		Expect(recipe).NotTo(ContainSubstring("shepherd:smoke"))
		Expect(recipe).To(ContainSubstring("shepherd:local"))
		Expect(recipe).To(ContainSubstring("shepherd:local-init"))
		Expect(mkTargetLine("smoke")).To(SatisfyAll(
			ContainSubstring("docker-build-local"),
			ContainSubstring("docker-build-init"),
		), "smoke should depend on the docker-build-local/docker-build-init targets to build its images")
	})
})

// Red run, 2026-09-15 (docs audit): `make dev-restart` ran
// `docker compose build shepherd` — but the shepherd service has no `build:`
// section (`image: shepherd:local`), so compose reported nothing to build and
// a Go change never reached the container. Its only side effect was the
// `build-web` prerequisite rewriting the tracked internal/spa/dist on the
// host, which deploy/Dockerfile.local discards anyway.
var _ = Describe("the dev-restart target", func() {
	It("rebuilds shepherd:local the way make dev does, instead of a compose build that has nothing to build", func() {
		Expect(mkTargetLine("dev-restart")).To(ContainSubstring("docker-build-local"),
			"dev-restart must depend on docker-build-local; the compose service has no build: section")
		Expect(mkTargetLine("dev-restart")).NotTo(ContainSubstring("build-web"),
			"a host build-web is discarded by Dockerfile.local and dirties the tracked dist")
		recipe := makeRecipe("dev-restart")
		Expect(recipe).NotTo(MatchRegexp(`docker compose .*\bbuild\b`),
			"compose build is a no-op for a service without build:")
		Expect(recipe).To(MatchRegexp(`docker compose .* up -d .*--force-recreate .*shepherd`),
			"the container must be recreated on the new image")
	})

	It("is a real gap in compose, not a stale spec: the shepherd service has image: and no build:", func() {
		compose := readRepoFile("dev/docker-compose.dev.yaml")
		svc := regexp.MustCompile(`(?s)\n  shepherd:\n(.*?)\n  [a-z]`).FindStringSubmatch(compose)
		Expect(svc).NotTo(BeNil())
		Expect(svc[1]).To(ContainSubstring("image: shepherd:local"))
		Expect(svc[1]).NotTo(MatchRegexp(`(?m)^    build:`))
	})
})

// Red run, 2026-09-10: `make tools` installs the standalone gofumpt binary,
// but `make fmt` runs `golangci-lint fmt ./...` (its gofumpt formatter,
// module-aware) and the comment at Makefile:540-541 explains that the
// standalone binary mis-groups the dot-less `shepherd` module path and must
// NOT be used — so `make tools` installs a CLI that reformats the repo into
// a state `make lint` then refuses.
var _ = Describe("the tools target", func() {
	It("does not install the standalone gofumpt make fmt must not use", func() {
		Expect(makeRecipe("tools")).NotTo(ContainSubstring("gofumpt"))
	})
})

// Red run, 2026-09-10: check-raw-sql's pattern is `Pool\(\)\.(Exec|Query|QueryRow)\(`,
// so a raw SQL call issued on a bare conn/tx/db variable (anything that isn't
// a direct `.Pool()` chain) slips past unmarked. internal/server/server.go:507
// (`db.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations ...")`) is
// exactly such a site, and it is the orchestrator's cross-cutting fix (a
// RAW-SQL-OK comment) — this workstream widens the pattern, not that file.
var _ = Describe("the check-raw-sql guard", func() {
	It("catches raw SQL on a pooled conn, not only on a direct Pool() chain", func() {
		root := GinkgoT().TempDir()
		probeDir := filepath.Join(root, "probe")
		Expect(os.MkdirAll(probeDir, 0o755)).To(Succeed())
		probe := "package probe\n\nimport \"context\"\n\ntype conner interface {\n" +
			"\tQueryRow(ctx context.Context, sql string, args ...any) int\n}\n\n" +
			"func f(conn conner, ctx context.Context) int {\n" +
			"\treturn conn.QueryRow(ctx, \"SELECT 1\")\n}\n"
		Expect(os.WriteFile(filepath.Join(probeDir, "probe.go"), []byte(probe), 0o644)).To(Succeed())

		out, err := runMake("check-raw-sql", "RAW_SQL_ROOT="+root)
		Expect(err).To(HaveOccurred(), "expected check-raw-sql to fail on unmarked raw SQL in the fixture tree:\n%s", out)
		Expect(out).To(ContainSubstring("probe.go"))
	})
})

// The ten guards `make lint` currently runs inline. A `guards:` target
// gathering them in one place is the single source of truth ci.yml's guards
// job resolves through (scripts/repocheck/ci_test.go, A2 territory) — this
// spec only pins the Makefile half.
var tenGuards = []string{
	"check-single-dist", "check-dist-consistency", "check-build-script",
	"check-raw-sql", "check-docker", "check-no-route-mocks",
	"check-gateway-pin", "check-chartvalues-pin", "check-docs-version",
	"check-docs-drift",
}

// Red run, 2026-09-10: `make lint` lists all ten guards as its own direct
// prerequisites; there is no `guards:` target for CI's guards job (which
// only runs six of the ten, per the workstream brief) to resolve through as
// a single source of truth.
var _ = Describe("the guards target", func() {
	It("gathers exactly the ten checks make lint used to list inline", func() {
		line := mkTargetLine("guards")
		for _, g := range tenGuards {
			Expect(line).To(MatchRegexp(`\b`+g+`\b`), g)
		}
	})

	It("is lint's prerequisite, not the ten checks listed inline on lint itself", func() {
		lintLine := mkTargetLine("lint")
		Expect(lintLine).To(MatchRegexp(`^lint:.*\bguards\b`))
		for _, g := range tenGuards {
			Expect(lintLine).NotTo(MatchRegexp(`\b`+g+`\b`),
				"lint: should depend on guards, not list %q directly (double indirection)", g)
		}
	})
})

// Red run, 2026-09-10: SECURITY.md:43 names govulncheck "the arbiter" for
// reachable vulnerabilities, but no Makefile target runs it anywhere. The
// ci.yml wiring and the scheduled govulncheck.yml workflow are A2 territory
// (scripts/repocheck/ci_test.go); this spec only pins the Makefile target.
var _ = Describe("the vulncheck target", func() {
	It("runs govulncheck via go run so it never touches go.mod", func() {
		recipe := makeRecipe("vulncheck")
		Expect(recipe).To(ContainSubstring("go run golang.org/x/vuln/cmd/govulncheck@"))
	})
})

// Red run, 2026-09-10: `make test-cover` does not exist, so nothing local or
// in CI emits a Go coverage profile. The ci.yml wiring (step summary +
// artifact upload) and that half of the red run are A2 territory
// (scripts/repocheck/ci_test.go); this spec only pins the Makefile target.
var _ = Describe("the test-cover target", func() {
	It("produces a coverprofile and prints the go tool cover summary", func() {
		recipe := makeRecipe("test-cover")
		Expect(recipe).To(ContainSubstring("-coverprofile"))
		Expect(recipe).To(ContainSubstring("go tool cover"))
	})
})
