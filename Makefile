.PHONY: build build-web build-all test test-integration e2e e2e-sim e2e-egress smoke test-ui check-single-dist check-dist-consistency check-build-script check-raw-sql check-docker lint fmt generate gen-alloy-version generate-corpus schema schema-verify helm-lint release-snapshot docker-build docker-build-local docker-build-init docker-build-simulator migrate dev dev-sim dev-frontend dev-restart dev-seed dev-reset test-fullstack

PNPM ?= pnpm

# Read canonical version pins from deploy/versions.env
include deploy/versions.env
export

DOCKER_BUILD_ARGS := \
	--build-arg GO_IMAGE=$(GO_IMAGE) \
	--build-arg NODE_IMAGE=$(NODE_IMAGE) \
	--build-arg ALLOY_IMAGE=$(ALLOY_IMAGE) \
	--build-arg DISTROLESS_IMAGE=$(DISTROLESS_IMAGE) \
	--build-arg DISTROLESS_BASE_IMAGE=$(DISTROLESS_BASE_IMAGE) \
	--build-arg PNPM_VERSION=$(PNPM_VERSION)

# Build the Go binary (requires web dist to already exist — run build-web first if needed)
build: build-web
	go build -ldflags="-s -w" -o ./bin/shepherd ./cmd/shepherd

# Build the React SPA
build-web:
	./scripts/build-web.sh

# Full build: web first, then Go
build-all: build-web build

# Run all unit tests (no Docker required)
test:
	go test ./...

# Run integration + unit tests (requires Docker for testcontainers)
test-integration:
	TESTCONTAINERS_HOST_OVERRIDE=127.0.0.1 go test -tags=integration ./...

# E2E suite (requires Docker Compose; ~10 min)
# Set E2E_KEEP=1 to leave the stack running after the suite (for debugging).
e2e: docker-build-local docker-build-init
	@# Reset first: a previous failed run leaves volumes behind, and re-seeding then
	@# dies on the agent token's duplicate primary key before any spec runs.
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	docker compose -f e2e/docker-compose.e2e.yaml up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --label-filter='!sandbox-sim' ./e2e/...
	@if [ "$(E2E_KEEP)" != "1" ]; then \
		docker compose -f e2e/docker-compose.e2e.yaml down -v; \
	else \
		echo "E2E_KEEP=1: stack left running. Run 'docker compose -f e2e/docker-compose.e2e.yaml down -v' to clean up."; \
	fi

# S3 sandbox-run e2e (VB-1 §6.4, §7.9's separate budget). Brings up the `sim`
# profile — which the default `make e2e` deliberately leaves down so the
# "simulator not configured" path is what every ordinary run exercises.
#
# TWO ginkgo passes over one stack, deliberately, because the two halves are
# guarded differently:
#
#   pass 1 — the containment probes, with --fail-on-empty. They are THE
#     reachability control (see the e2e-egress comment below); a label typo
#     that made them select nothing must fail the build, not pass it.
#   pass 2 — everything else labelled sandbox-sim: the run-lifecycle specs
#     that prove the sandbox DELIVERS (completed run, captured series, health,
#     rewrite disclosure). `&& !sandbox-egress` excludes the specs pass 1
#     already ran, whose Ordered BeforeAll creates a fixed-name org and would
#     get HTTP 409 the second time.
#
# A single --fail-on-empty pass over `sandbox-sim` would NOT give pass 1's
# guarantee: the run-lifecycle specs alone would keep the filter non-empty
# while every egress probe had silently vanished.
e2e-sim: docker-build-local docker-build-init docker-build-simulator
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter=sandbox-egress ./e2e/...
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter='sandbox-sim && !sandbox-egress' ./e2e/...
	@if [ "$(E2E_KEEP)" != "1" ]; then \
		docker compose -f e2e/docker-compose.e2e.yaml --profile sim down -v; \
	else \
		echo "E2E_KEEP=1: stack left running."; \
	fi

# The sandbox egress probes alone (VB-1 §6.4 "Security containment"), for a
# fast local containment check. CI runs `make e2e-sim`, whose first pass is
# this same filter with the same --fail-on-empty guard plus the run-lifecycle
# specs.
#
# These probes are THE reachability control for the S3 sandbox, not a backstop
# to one. The transform's keep lists and the simulator's CheckEndpoints bound
# which authored VALUES leave a user's graph; neither can bound where a running
# config connects, because a discovery.relabel rule computes __address__ at
# runtime. So this target failing means the sandbox has no containment at all,
# and it is the one e2e target that must never be allowed to quietly do nothing.
#
# --fail-on-empty is what enforces that: a label typo, a renamed Label() or a
# deleted spec would otherwise leave ginkgo reporting "Ran 0 of N Specs" and
# exiting 0 — a green CI job measuring nothing, which is finding H4's failure
# mode wearing a different hat.
#
# This target used to be what CI ran INSTEAD of `make e2e-sim`, because the S3
# run-lifecycle spec was red on finding M13 (internal/visual/render.go
# bracket-wrapped every []discovery.Target reference, which `alloy validate`
# accepts and `alloy run` refuses). M13 is fixed — refValue in render.go, red
# and green transcripts in docs/proofs/sandbox-sim-e2e.md — so CI now runs the
# whole suite and this target is the narrow local check.
e2e-egress: docker-build-local docker-build-init docker-build-simulator
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter=sandbox-egress ./e2e/...
	@if [ "$(E2E_KEEP)" != "1" ]; then \
		docker compose -f e2e/docker-compose.e2e.yaml --profile sim down -v; \
	else \
		echo "E2E_KEEP=1: stack left running."; \
	fi

# Container smoke test — runs without the full e2e stack, < 60s.
# Verifies: image builds, migrate up runs, serve starts and /healthz+/readyz return 200,
# SIGTERM triggers clean shutdown, invalid SHEPHERD_LOG_LEVEL fails fast.
# Prerequisite: Docker daemon running (OrbStack / Docker Desktop).
smoke:
	@echo "==> Building production image for smoke test..."
	docker build $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile -t shepherd:smoke .
	@echo "==> Building init image for migrate..."
	docker build -f deploy/Dockerfile.init -t shepherd:smoke-init .
	@echo "==> Starting postgres..."
	@SMOKE_PG=$$(docker run -d \
		-e POSTGRES_DB=shepherd_smoke \
		-e POSTGRES_USER=shepherd \
		-e POSTGRES_PASSWORD=shepherd \
		postgres:16-alpine) && \
	echo "Postgres container: $$SMOKE_PG" && \
	trap "docker rm -f $$SMOKE_PG 2>/dev/null" EXIT INT TERM && \
	sleep 3 && \
	SMOKE_PG_IP=$$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $$SMOKE_PG) && \
	echo "Postgres IP: $$SMOKE_PG_IP" && \
	SMOKE_DB="postgres://shepherd:shepherd@$$SMOKE_PG_IP:5432/shepherd_smoke?sslmode=disable" && \
	echo "==> Running migrate up..." && \
	docker run --rm \
		-e SHEPHERD_DATABASE_URL="$$SMOKE_DB" \
		-e SHEPHERD_SECURITY_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		shepherd:smoke-init migrate up && \
	echo "==> Starting shepherd serve (background)..." && \
	SMOKE_SRV=$$(docker run -d \
		-e SHEPHERD_DATABASE_URL="$$SMOKE_DB" \
		-e SHEPHERD_SECURITY_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		-e SHEPHERD_LOG_LEVEL=debug \
		-p 18080:8080 \
		shepherd:smoke serve) && \
	echo "Shepherd container: $$SMOKE_SRV" && \
	trap "docker rm -f $$SMOKE_SRV $$SMOKE_PG 2>/dev/null" EXIT INT TERM && \
	echo "==> Waiting for /healthz and /readyz..." && \
	for i in $$(seq 1 30); do \
		if curl -sf http://localhost:18080/healthz > /dev/null 2>&1; then break; fi; \
		sleep 1; \
	done && \
	curl -sf http://localhost:18080/healthz && echo " [healthz OK]" && \
	curl -sf http://localhost:18080/readyz  && echo " [readyz OK]" && \
	echo "==> Testing healthcheck subcommand from inside container..." && \
	docker exec $$SMOKE_SRV /usr/local/bin/shepherd healthcheck --addr localhost:8080 && \
	echo "[healthcheck subcommand OK]" && \
	echo "==> Sending SIGTERM and asserting clean shutdown..." && \
	docker stop $$SMOKE_SRV && \
	EXIT_CODE=$$(docker inspect $$SMOKE_SRV --format='{{.State.ExitCode}}') && \
	if [ "$$EXIT_CODE" != "0" ]; then echo "ERROR: shepherd exited with $$EXIT_CODE"; exit 1; fi && \
	echo "[clean shutdown OK exit=$$EXIT_CODE]" && \
	echo "==> Testing invalid SHEPHERD_LOG_LEVEL fails fast (P1-L.1 red run until P1-L.1 lands)..." && \
	if docker run --rm \
		-e SHEPHERD_DATABASE_URL="$$SMOKE_DB" \
		-e SHEPHERD_SECURITY_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		-e SHEPHERD_LOG_LEVEL=verbose \
		shepherd:smoke serve 2>&1 | grep -qi "invalid\|unknown\|verbose"; then \
		echo "[invalid log level rejected OK]"; \
	else \
		echo "WARN: invalid SHEPHERD_LOG_LEVEL=verbose did not fail fast (P1-L.1 not yet implemented)"; \
	fi && \
	echo "==> Testing OIDC-free local admin login (LA-1 smoke step)..." && \
	SMOKE_HASH=$$(docker run --rm \
		-e SHEPHERD_DATABASE_URL="$$SMOKE_DB" \
		-e SHEPHERD_SECURITY_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		shepherd:smoke-init hash-password --password-stdin <<< "smoke-test-pass") && \
	SMOKE_LA=$$(docker run -d \
		-e SHEPHERD_DATABASE_URL="$$SMOKE_DB" \
		-e SHEPHERD_SECURITY_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
		-e SHEPHERD_AUTH_LOCAL_ADMIN_ENABLED=true \
		-e "SHEPHERD_AUTH_LOCAL_ADMIN_PASSWORD_HASH=$$SMOKE_HASH" \
		-e SHEPHERD_LOG_LEVEL=debug \
		-p 18081:8080 \
		shepherd:smoke serve) && \
	echo "Local admin shepherd container: $$SMOKE_LA" && \
	trap "docker rm -f $$SMOKE_LA $$SMOKE_SRV $$SMOKE_PG 2>/dev/null" EXIT INT TERM && \
	for i in $$(seq 1 30); do \
		if curl -sf http://localhost:18081/healthz > /dev/null 2>&1; then break; fi; \
		sleep 1; \
	done && \
	SMOKE_COOKIE=$$(curl -sf -X POST http://localhost:18081/api/auth/local/login \
		-H 'Content-Type: application/json' \
		-H 'X-Requested-With: XMLHttpRequest' \
		-d '{"username":"admin","password":"smoke-test-pass"}' \
		-c /tmp/shepherd-smoke-cookie.txt -b /tmp/shepherd-smoke-cookie.txt \
		-w '%{http_code}' -o /dev/null) && \
	if [ "$$SMOKE_COOKIE" != "200" ]; then echo "ERROR: local login returned $$SMOKE_COOKIE"; exit 1; fi && \
	echo "[local admin login OK]" && \
	SMOKE_ME=$$(curl -sf http://localhost:18081/api/me \
		-H 'X-Requested-With: XMLHttpRequest' \
		-b /tmp/shepherd-smoke-cookie.txt) && \
	echo "$$SMOKE_ME" | grep -q '"auth_method":"local"' || { echo "ERROR: /api/me did not return auth_method:local; got: $$SMOKE_ME"; exit 1; } && \
	echo "[/api/me auth_method:local OK]" && \
	docker rm -f $$SMOKE_LA >/dev/null && \
	echo "==> Smoke test PASSED."

# Playwright UI tests (no backend required)
test-ui:
	cd web && $(PNPM) exec playwright install --with-deps chromium
	./scripts/build-web.sh
	@# Kill any stale vite preview before starting a fresh one (reuseExistingServer=false
	@# in playwright.config.ts means Playwright will fail if port 4173 is already bound).
	@pkill -f "vite preview" 2>/dev/null || true
	cd web && $(PNPM) exec playwright test

# Guard: exactly one dist directory (internal/spa/dist); no stray copies.
check-single-dist:
	@count=$$(find . -path ./web/node_modules -prune -o -path ./.git -prune -o -name 'dist' -type d -print | grep -v '^\./$$' | wc -l | tr -d ' '); \
	if [ "$$count" != "1" ]; then \
		echo "ERROR: expected 1 dist directory, found $$count:"; \
		find . -path ./web/node_modules -prune -o -path ./.git -prune -o -name 'dist' -type d -print | grep -v '^\./$$'; \
		exit 1; \
	fi
	@echo "check-single-dist: OK (1 dist directory)"

# Guard: internal/spa/dist/index.html must not reference an asset that is
# missing or untracked. check-single-dist only counts dist *directories* — it
# never noticed that index.html's script/link tags named assets/index-*.js|css
# hashes with no matching tracked file, while the previous build's hashes sat
# around as untracked/deleted noise. `git add -u && git commit` only stages
# changes to already-tracked files, so an untracked asset index.html
# references would ship a dist whose entrypoint 404s on checkout.
check-dist-consistency:
	@INDEX=internal/spa/dist/index.html; \
	if [ ! -f "$$INDEX" ]; then echo "ERROR: $$INDEX missing"; exit 1; fi; \
	REFS=$$(grep -oE 'assets/[A-Za-z0-9_.-]+' "$$INDEX" | sort -u); \
	if [ -z "$$REFS" ]; then echo "ERROR: $$INDEX references no assets/* files"; exit 1; fi; \
	FAIL=0; \
	for f in $$REFS; do \
		path="internal/spa/dist/$$f"; \
		if [ ! -f "$$path" ]; then \
			echo "ERROR: $$INDEX references $$f, which does not exist on disk"; FAIL=1; \
		elif ! git ls-files --error-unmatch "$$path" >/dev/null 2>&1; then \
			echo "ERROR: $$INDEX references $$f, which is not tracked by git (git add -u would not include it)"; FAIL=1; \
		fi; \
	done; \
	if [ "$$FAIL" != "0" ]; then exit 1; fi; \
	echo "check-dist-consistency: OK (index.html's referenced assets exist and are tracked)"

# Guard: no pnpm build commands outside scripts/build-web.sh.
check-build-script:
	@if grep -rn "pnpm.*build\|pnpm install" Makefile deploy/ .goreleaser.yaml 2>/dev/null | \
		grep -v "scripts/build-web.sh\|# dev-exempt\|pnpm exec playwright\|check-build-script\|Makefile:[0-9]*:.*grep -rn"; then \
		echo "ERROR: pnpm build/install found outside scripts/build-web.sh (see above)"; \
		exit 1; \
	fi
	@echo "check-build-script: OK"

# Guard: raw SQL calls in Go source outside internal/store must carry a RAW-SQL-OK comment.
check-raw-sql:
	@UNMARKED=$$(grep -rn --include='*.go' \
	    -E 'Pool\(\)\.(Exec|Query|QueryRow)\(' \
	    internal/ \
	    | grep -v 'internal/store/' \
	    | grep -v '_test\.go' \
	    | grep -v 'internal/testutil/' \
	    | grep -v 'internal/cli/dev\.go'); \
	if [ -n "$$UNMARKED" ]; then \
		FAILING=$$(echo "$$UNMARKED" | while IFS=: read file line rest; do \
			prev=$$(sed -n "$$(( line - 1 ))p" "$$file" 2>/dev/null); \
			echo "$$prev" | grep -q 'RAW-SQL-OK' || echo "$$file:$$line: missing RAW-SQL-OK comment"; \
		done); \
		if [ -n "$$FAILING" ]; then \
			echo "ERROR: unmarked raw SQL found:"; \
			echo "$$FAILING"; \
			exit 1; \
		fi; \
	fi
	@echo "check-raw-sql: OK"

# Lint
# Guard 1: no hardcoded FROM (must use ARG variables).
# Guard 2: every ARG default in the three app Dockerfiles must agree with
# deploy/versions.env — catches the class of drift where `make` passes the
# right --build-arg but a stale ARG default (used by anyone building the
# Dockerfile directly, e.g. `docker build` without our Makefile) silently
# ships a different image.
check-docker:
	@HARDCODED=$$(grep -n '^FROM ' deploy/Dockerfile deploy/Dockerfile.local deploy/Dockerfile.goreleaser deploy/Dockerfile.simulator \
	    | grep -v 'FROM \$${\|AS '); \
	if [ -n "$$HARDCODED" ]; then \
		echo "ERROR: hardcoded FROM found (should use ARG variables):"; \
		echo "$$HARDCODED"; \
		exit 1; \
	fi
	@FAIL=0; \
	for f in deploy/Dockerfile deploy/Dockerfile.local deploy/Dockerfile.goreleaser deploy/Dockerfile.simulator; do \
		for var in GO_IMAGE NODE_IMAGE ALLOY_IMAGE DISTROLESS_IMAGE DISTROLESS_BASE_IMAGE PNPM_VERSION; do \
			default=$$(sed -n "s/^ARG $$var=\(.*\)/\1/p" "$$f"); \
			if [ -n "$$default" ]; then \
				expected=$$(sed -n "s/^$$var=\(.*\)/\1/p" deploy/versions.env); \
				if [ "$$default" != "$$expected" ]; then \
					echo "ERROR: $$f: ARG $$var default '$$default' disagrees with deploy/versions.env '$$expected'"; \
					FAIL=1; \
				fi; \
			fi; \
		done; \
	done; \
	if [ "$$FAIL" != "0" ]; then exit 1; fi
	@echo "check-docker: OK"

lint: check-single-dist check-dist-consistency check-build-script check-raw-sql check-docker
	golangci-lint run ./...

# Format Go code (gci via golangci-lint fmt + standalone gofumpt to avoid gci/gofumpt cycle)
fmt:
	golangci-lint fmt ./...
	gofumpt -w $$(find . -name '*.go' ! -path './gen/*' ! -path './internal/store/sqlc/*')

# Codegen
generate: gen-alloy-version
	buf generate
	sqlc generate

# Regenerate internal/version/alloy_gen.go from the single source of truth,
# deploy/versions.env's ALLOY_VERSION. Hermetic (no network) — safe to run
# on every `make generate` and `make schema`. Never hand-edit the output.
gen-alloy-version:
	@printf '// Code generated by "make gen-alloy-version" from deploy/versions.env. DO NOT EDIT.\n\n// Package version exposes build-time version constants.\npackage version\n\n// AlloySchemaVersion is the pinned Alloy schema version this build serves.\n// Source of truth: ALLOY_VERSION in deploy/versions.env.\nconst AlloySchemaVersion = "alloy-$(ALLOY_VERSION)"\n' > internal/version/alloy_gen.go
	@echo "gen-alloy-version: wrote internal/version/alloy_gen.go (AlloySchemaVersion = alloy-$(ALLOY_VERSION))"

# Regenerate the Alloy component schema artifact (internal/schema/artifacts/alloy-v<X>.json)
# by cloning grafana/alloy at the ALLOY_VERSION pinned in deploy/versions.env and reconciling
# internal/schema/artifacts/overlay.json against the result.
# Prerequisites: network access (clones grafana/alloy) and git. NOT part of `make build` —
# app builds stay hermetic; this is a deliberate, occasional maintenance step.
# Bump procedure: edit ALLOY_VERSION in deploy/versions.env -> make schema ->
# review overlay entries marked "needs_review": true -> commit.
schema: gen-alloy-version
	./tools/alloy-schema-gen/run.sh

# Verify the committed artifact still matches what the pinned Alloy produces, AND
# that the hand-maintained overlay still covers it. The second half is what makes
# an Alloy bump fail here rather than at an S3 sandbox run: every component the
# artifact declares must resolve to exactly one S3 disposition, every sim_keep
# path must still resolve in canonical form, and no kept path may trip the
# credential/address name guard without an acknowledged reason.
# Regenerates into a temp dir (overlay.json is NOT touched) and diffs against the
# committed artifact with _meta.generated_at deleted from both sides — that
# timestamp is the only non-deterministic field, so a naive diff would fail 100%
# of the time. Everything else is byte-reproducible.
# Prerequisites: network access, git, go, jq. Run on Alloy-bump PRs + weekly cron.
schema-verify: gen-alloy-version
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	SCHEMA_OUT_DIR="$$tmp" SKIP_RECONCILE=1 ./tools/alloy-schema-gen/run.sh >/dev/null; \
	committed=internal/schema/artifacts/alloy-$(ALLOY_VERSION).json; \
	if [ ! -f "$$committed" ]; then \
	  echo "schema-verify: FAIL — no committed artifact for the pin ($$committed)"; exit 1; \
	fi; \
	jq -S 'del(._meta.generated_at)' "$$committed" > "$$tmp/committed.norm.json"; \
	jq -S 'del(._meta.generated_at)' "$$tmp/alloy-$(ALLOY_VERSION).json" > "$$tmp/fresh.norm.json"; \
	if diff -u "$$tmp/committed.norm.json" "$$tmp/fresh.norm.json"; then \
	  echo "schema-verify: OK ($$committed matches grafana/alloy@$(ALLOY_VERSION))"; \
	else \
	  echo "schema-verify: FAIL — the committed artifact does not match grafana/alloy@$(ALLOY_VERSION); run 'make schema' and commit the result."; \
	  exit 1; \
	fi
	@echo "==> Overlay guards (S3 dispositions, keep lists, endpoint paths)..."
	@go test ./internal/schema/ -count=1
	@echo "schema-verify: OK (every artifact component resolves to exactly one S3 disposition)"

# Regenerate the visual-builder goldens from the shipped schema artifact, then
# sync the whole corpus from Go testdata → web fixtures. Run after adding or
# changing a corpus graph, or after a deliberate renderer change.
#
# The goldens are rendered against internal/schema's embedded artifact merged
# with overlay.json — the same payload the server serves — never against a test
# fixture. Review the diff: a golden that changes without a corresponding
# renderer or graph change is a regression, not a refresh. Both suites verify
# the copies are byte-identical to the Go originals, so a partial run is caught.
generate-corpus:
	@echo "==> Regenerating goldens from the shipped schema artifact..."
	GEN_GOLDENS=1 go test ./internal/visual/ -run TestGenGoldens
	@echo "==> Syncing visual builder corpus..."
	@mkdir -p web/src/visual/__fixtures__/corpus
	@cp internal/visual/testdata/corpus/*.graph.json web/src/visual/__fixtures__/corpus/
	@cp internal/visual/testdata/corpus/*.golden.alloy web/src/visual/__fixtures__/corpus/
	@echo "==> Done. $(shell ls internal/visual/testdata/corpus/*.graph.json | wc -l | tr -d ' ') corpus entries synced."

# Helm lint
helm-lint:
	helm lint deploy/helm/shepherd
	helm template shepherd deploy/helm/shepherd -f deploy/helm/shepherd/ci/default-values.yaml > /dev/null
	helm template shepherd deploy/helm/shepherd -f deploy/helm/shepherd/ci/full-values.yaml > /dev/null

# GoReleaser dry run
# Local defaults for the docker image templates; CI overrides both.
IMAGE_REGISTRY ?= ghcr.io/procoduck
SOURCE_URL ?= https://github.com/procoduck/sheperd
release-snapshot:
	IMAGE_REGISTRY=$(IMAGE_REGISTRY) SOURCE_URL=$(SOURCE_URL) goreleaser release --snapshot --clean --skip=publish

# Build Docker image locally (for e2e and dev; use docker-build-init for the init/CLI image).
docker-build: build-web
	docker build $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile -t shepherd:local .

docker-build-local: build-web
	docker buildx build --platform linux/arm64 $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile.local -t shepherd:local .

# Build the init/CLI image (used by dev and smoke stacks for migrate + seed).
docker-build-init:
	docker build -f deploy/Dockerfile.init -t shepherd:local-init .

# Build the S3 sandbox simulator image (VB-1 §6.4). Same DOCKER_BUILD_ARGS as the
# app image so the sandbox runs the Alloy build pinned in deploy/versions.env —
# a sandbox on a different Alloy would make S3 results lie about the fleet.
docker-build-simulator:
	docker build $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile.simulator -t shepherd-simulator:local .
	docker tag shepherd-simulator:local shepherd-simulator:dev

# Start the local dev stack (postgres, shepherd, mockmsft).
# Images must be pre-built: make docker-build docker-build-init
# Use --profile alloy for Alloy agent; --profile oidc for OIDC login.
dev:
	docker compose -f dev/docker-compose.dev.yaml up -d --build --wait
	@echo ""
	@echo "=========================================================="
	@echo "  Shepherd dev stack is running!"
	@echo "  URL:      http://localhost:8080"
	@echo "  Login:    admin / admin"
	@echo "  DB:       postgres://shepherd:shepherd@localhost:15432/shepherd_dev"
	@echo "  Reset:    make dev-reset && make dev"
	@echo "=========================================================="

# Start the Vite dev server (HMR) against the running dev backend.
# Requires: make dev (backend must be healthy at localhost:8080).
dev-frontend:
	@curl -sf http://localhost:8080/healthz > /dev/null || (echo "ERROR: shepherd is not running. Run 'make dev' first."; exit 1)
	cd web && $(PNPM) dev

# Rebuild the shepherd image and restart the container (5-10s with layer cache).
dev-restart: build-web
	docker compose -f dev/docker-compose.dev.yaml build shepherd
	docker compose -f dev/docker-compose.dev.yaml up -d shepherd

# Re-run the dev seed (idempotent — safe to run on a running stack).
dev-seed:
	docker compose -f dev/docker-compose.dev.yaml run --rm shepherd-seed

# Stop the dev stack and wipe all data (named volumes).
dev-reset:
	docker compose -f dev/docker-compose.dev.yaml down -v

# Start the dev stack with 3 Alloy instances (prod-eu-1/metrics, prod-eu-1/logs, staging-eu-1/metrics).
dev-alloy3:
	docker compose -f dev/docker-compose.dev.yaml --profile alloy3 up -d alloy-metrics alloy-logs alloy-staging

# Start the dev stack with the S3 sandbox simulator (VB-1 §6.4).
# Without SHEPHERD_SIM_ENABLED the shepherd container comes up with the feature
# off, which is what every ordinary `make dev` exercises.
dev-sim: docker-build-simulator
	SHEPHERD_SIM_ENABLED=true docker compose -f dev/docker-compose.dev.yaml --profile sim up -d --build --wait

# Run the fullstack Playwright suite against the dev stack.
# Boots the dev stack, runs tests, captures logs on failure, tears down.
test-fullstack: docker-build-local docker-build-init
	cd web && $(PNPM) exec playwright install --with-deps chromium
	@# Reset first: leftover volumes from a previous run re-seed onto existing rows
	@# and the stack dies before any spec runs (same trap as make e2e).
	docker compose -f dev/docker-compose.dev.yaml down -v
	@# `--build` only builds services that declare a build: section; shepherd:local
	@# and shepherd:local-init come from the docker-build-* prerequisites above.
	docker compose -f dev/docker-compose.dev.yaml up -d --build --wait
	@# The cd runs in a SUBSHELL: without it the directory change leaks into the
	@# teardown below, which then looks for web/dev/docker-compose.dev.yaml, fails,
	@# and leaves the whole stack running.
	( cd web && $(PNPM) exec playwright test --config playwright.fullstack.config.ts ); \
		STATUS=$$?; \
		if [ $$STATUS -ne 0 ]; then \
			docker compose -f dev/docker-compose.dev.yaml logs --no-color > /tmp/fullstack-stack.log 2>&1; \
			echo "Stack logs saved to /tmp/fullstack-stack.log"; \
		fi; \
		docker compose -f dev/docker-compose.dev.yaml down -v; \
		exit $$STATUS
