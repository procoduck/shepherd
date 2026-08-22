.PHONY: web-ci check-gateway-pin check-chartvalues-pin chart-verify preflight-docker help build build-web build-all test e2e e2e-k8s e2e-k8s-clean e2e-sim e2e-egress smoke test-ui check-single-dist check-dist-consistency check-build-script check-raw-sql check-docker check-no-route-mocks lint fmt generate gen-alloy-version generate-corpus schema schema-verify helm-lint release-snapshot docker-build docker-build-local docker-build-init docker-build-simulator dev dev-sim dev-frontend dev-restart dev-seed dev-reset test-fullstack clean clean-docker tools preflight-ginkgo preflight-k8s

# Several recipes are bash-idiomatic (the smoke here-string, trap chains);
# /bin/sh is dash on Debian/Ubuntu and rejects them.
SHELL := /usr/bin/env bash

.DEFAULT_GOAL := help

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

# Extra flags for the image builds. Empty locally (the daemon's own layer
# cache does the job); CI sets --cache-from/--cache-to so runs don't rebuild
# the multi-stage images cold every time (see the buildx-cache steps in
# .github/workflows).
DOCKER_CACHE_FLAGS ?=

# Fail fast (<1s) on a missing CLI instead of discovering it after minutes of
# docker builds or stack boots. $(1) = binary, $(2) = how to get it.
define preflight
@command -v $(1) >/dev/null 2>&1 || { echo "ERROR: '$(1)' not found. $(2)"; exit 1; }
endef

# The e2e compose suites shell out to the ginkgo CLI after building images and
# booting the stack; check first so the failure costs a second, not minutes,
# and cannot strand a running stack.
preflight-ginkgo:
	$(call preflight,ginkgo,Run 'make tools'.)

# The kind suite shells out to kind, helm, and kubectl (see
# e2e/k8s/fixtures_test.go); none is go-installable via 'make tools'.
preflight-k8s:
	$(call preflight,kind,Install kind (e.g. brew install kind).)
	$(call preflight,helm,Install helm (e.g. brew install helm).)
	$(call preflight,kubectl,Install kubectl (e.g. brew install kubectl).)

# schema/schema-verify run the extractor in a linux container (run.sh) so the
# artifact matches the linux fleet regardless of host OS.
preflight-docker:
	$(call preflight,docker,Install Docker Desktop or the docker engine.)

# The '##' after a target is the one-liner `make help` surfaces; the longer
# design-note comment blocks above targets are the full documentation.
help: ## List targets and the env knobs the test suites honor
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[A-Za-z0-9_-]+:.*## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Env knobs:"
	@echo "  E2E_KEEP=1              leave the compose stack running after e2e / e2e-sim / e2e-egress"
	@echo "  E2E_K8S_KEEP=1          leave the kind cluster up after e2e-k8s"
	@echo "  E2E_K8S_NODE_IMAGE=...  pin a different Kubernetes version for e2e-k8s"
	@echo "  E2E_K8S_ARTIFACTS=dir   write per-feature failure logs from e2e-k8s"
	@echo "  E2E_K8S_ALLOW_STALE_IMAGES=1  skip e2e-k8s's image-freshness hint"

# Versions: ginkgo and the protoc plugins track go.mod so the CLIs always match
# the libraries; sqlc and buf match .github/workflows/ci.yml's generated-drift
# job. protoc-gen-es comes from web/node_modules (buf.gen.yaml points there), so
# `make generate` also needs a `pnpm install` in web/ — scripts/build-web.sh or
# the web job's install both provide it.
tools: ## Install the Go-installable CLIs the targets here shell out to
	go install github.com/onsi/ginkgo/v2/ginkgo@$$(go list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)
	go install mvdan.cc/gofumpt@v0.11.0
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/bufbuild/buf/cmd/buf@v1.72.0
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@$$(go list -m -f '{{.Version}}' connectrpc.com/connect)

clean: ## Remove build outputs (bin/, goreleaser dist/)
	rm -rf bin/ dist/

clean-docker: ## Remove the local shepherd:* / shepherd-simulator:* image tags
	@imgs=$$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^(shepherd|shepherd-simulator):' || true); \
	if [ -n "$$imgs" ]; then docker rmi $$imgs; else echo "clean-docker: no shepherd images to remove"; fi

build: build-web ## Build the shepherd binary (embeds the SPA built by build-web)
	go build -ldflags="-s -w" -o ./bin/shepherd ./cmd/shepherd

build-web: ## Build the React SPA into internal/spa/dist
	./scripts/build-web.sh

# Alias kept because docs reference it; `build` already depends on build-web.
build-all: build ## Alias of build

# Requires Docker: internal/testutil spins up real Postgres via testcontainers.
test: ## Run all Go tests (requires Docker)
	go test ./...

# E2E suite (requires Docker Compose; ~10 min)
# Set E2E_KEEP=1 to leave the stack running after the suite (for debugging).
e2e: preflight-ginkgo docker-build-local docker-build-init ## Compose e2e suite (~10 min)
	@# Reset first: a previous failed run leaves volumes behind, and re-seeding then
	@# dies on the agent token's duplicate primary key before any spec runs.
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	docker compose -f e2e/docker-compose.e2e.yaml up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --label-filter='!sandbox-sim' ./e2e
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
e2e-sim: preflight-ginkgo docker-build-local docker-build-init docker-build-simulator ## S3 sandbox e2e: containment probes + run lifecycle
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter=sandbox-egress ./e2e
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter='sandbox-sim && !sandbox-egress' ./e2e
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
e2e-egress: preflight-ginkgo docker-build-local docker-build-init docker-build-simulator ## Sandbox egress containment probes only (fast local check)
	docker compose -f e2e/docker-compose.e2e.yaml down -v
	SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false --fail-on-empty --label-filter=sandbox-egress ./e2e
	@if [ "$(E2E_KEEP)" != "1" ]; then \
		docker compose -f e2e/docker-compose.e2e.yaml --profile sim down -v; \
	else \
		echo "E2E_KEEP=1: stack left running."; \
	fi

# Container smoke test — runs without the full e2e stack, < 60s.
# Verifies: image builds, migrate up runs, serve starts and /healthz+/readyz return 200,
# SIGTERM triggers clean shutdown, invalid SHEPHERD_LOG_LEVEL fails fast.
# Prerequisite: Docker daemon running (OrbStack / Docker Desktop).
# Containers are named shepherd-smoke-* and removed up front, so a SIGKILLed
# run cannot leave anonymous containers squatting on ports 18080/18081.
smoke: ## Container smoke test (< 60s, Docker only)
	@docker rm -f shepherd-smoke-pg shepherd-smoke-srv shepherd-smoke-la >/dev/null 2>&1 || true
	@echo "==> Building production image for smoke test..."
	docker build $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile.local -t shepherd:smoke .
	@echo "==> Building init image for migrate..."
	docker build $(DOCKER_BUILD_ARGS) -f deploy/Dockerfile.init -t shepherd:smoke-init .
	@echo "==> Starting postgres..."
	@SMOKE_PG=$$(docker run -d --name shepherd-smoke-pg \
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
	SMOKE_SRV=$$(docker run -d --name shepherd-smoke-srv \
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
	SMOKE_LA=$$(docker run -d --name shepherd-smoke-la \
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

# Reproduces CI's `web` job exactly (typecheck, unit tests, biome CHECK — lint
# AND format — then the production build). Exists because `pnpm lint` alone
# runs only the lint half, so a formatting difference passes locally and fails
# in CI; a check that cannot go red on your machine is not a check.
web-ci: ## Run CI's web job locally (typecheck + tests + biome check + build)
	$(call preflight,$(PNPM),Install pnpm (https://pnpm.io/installation).)
	cd web && $(PNPM) run ci

test-ui: ## Mocked Playwright visual suite (no backend required)
	cd web && $(PNPM) exec playwright install --with-deps chromium
	./scripts/build-web.sh
	@# Kill any stale preview holding the Playwright port (4173, strictPort in
	@# web/playwright.config.ts) before starting a fresh one — scoped to the port,
	@# not pkill-by-name, so previews from other repos on this machine survive.
	@lsof -ti tcp:4173 2>/dev/null | xargs kill 2>/dev/null || true
	cd web && $(PNPM) exec playwright test

# Guard: exactly one SPA dist directory (internal/spa/dist); no stray copies.
# Two directories are pruned rather than counted because neither is a stray SPA
# build: the repo-root ./dist is goreleaser's gitignored output (left by
# `make release-snapshot`), and .claude/worktrees/ holds agent git worktrees,
# each a full checkout that necessarily contains its own internal/spa/dist.
# Counting those made the guard fail for the whole repo whenever an agent
# worktree existed — a false positive that says nothing about stray builds.
check-single-dist: ## Guard: exactly one dist directory
	@count=$$(find . -path ./web/node_modules -prune -o -path ./.git -prune -o -path ./dist -prune -o -path ./.claude -prune -o -name 'dist' -type d -print | grep -v '^\./$$' | wc -l | tr -d ' '); \
	if [ "$$count" != "1" ]; then \
		echo "ERROR: expected 1 dist directory, found $$count:"; \
		find . -path ./web/node_modules -prune -o -path ./.git -prune -o -path ./dist -prune -o -path ./.claude -prune -o -name 'dist' -type d -print | grep -v '^\./$$'; \
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
check-dist-consistency: ## Guard: index.html's assets exist and are tracked
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
check-build-script: ## Guard: pnpm build/install only in scripts/build-web.sh
	@if grep -rn "pnpm.*build\|pnpm install" Makefile deploy/ .goreleaser.yaml 2>/dev/null | \
		grep -v "scripts/build-web.sh\|# dev-exempt\|pnpm exec playwright\|check-build-script\|Makefile:[0-9]*:.*grep -rn"; then \
		echo "ERROR: pnpm build/install found outside scripts/build-web.sh (see above)"; \
		exit 1; \
	fi
	@echo "check-build-script: OK"

# Guard: raw SQL calls in Go source outside internal/store must carry a RAW-SQL-OK comment.
check-raw-sql: ## Guard: raw SQL outside internal/store carries RAW-SQL-OK
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

# Guard 1: no hardcoded FROM (must use ARG variables).
# Guard 2: every ARG default in the three app Dockerfiles must agree with
# deploy/versions.env — catches the class of drift where `make` passes the
# right --build-arg but a stale ARG default (used by anyone building the
# Dockerfile directly, e.g. `docker build` without our Makefile) silently
# ships a different image.
check-docker: ## Guard: Dockerfile FROMs/ARG defaults agree with versions.env
	@HARDCODED=$$(grep -n '^FROM ' deploy/Dockerfile.local deploy/Dockerfile.init deploy/Dockerfile.goreleaser deploy/Dockerfile.goreleaser-simulator deploy/Dockerfile.simulator \
	    | grep -v 'FROM \$${\|AS '); \
	if [ -n "$$HARDCODED" ]; then \
		echo "ERROR: hardcoded FROM found (should use ARG variables):"; \
		echo "$$HARDCODED"; \
		exit 1; \
	fi
	@FAIL=0; \
	for f in deploy/Dockerfile.local deploy/Dockerfile.init deploy/Dockerfile.goreleaser deploy/Dockerfile.goreleaser-simulator deploy/Dockerfile.simulator; do \
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

# Guard: no route interception in the fullstack Playwright suite. The fullstack
# layer exists to exercise the REAL server (docs/frontend-testing.md); a
# page.route() mock quietly turns a fullstack spec back into a mocked one.
# Comment lines are excluded — the suite's own "never use page.route()" notice
# must not trip the guard that enforces it.
check-no-route-mocks: ## Guard: no page.route() in web/tests/fullstack
	@if grep -rn "page.route(" web/tests/fullstack/ 2>/dev/null | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*|/\*)'; then \
		echo "ERROR: page.route() found in web/tests/fullstack/ — fullstack specs must hit the real server"; \
		exit 1; \
	fi
	@echo "check-no-route-mocks: OK"

# Guard: the Gateway API pin is the floor Shepherd claims to support, so every
# place that names a version must agree with deploy/versions.env — otherwise the
# kind suite proves conformance against one version while the code refuses
# another, and the claim in docs/gateway-tier-plan.md D1 means nothing.
check-gateway-pin: ## Guard: Gateway API version/channel agree with versions.env
	@fail=0; \
	want="$(GATEWAY_API_VERSION)"; wantch="$(GATEWAY_API_CHANNEL)"; \
	if [ -z "$$want" ] || [ -z "$$wantch" ]; then \
		echo "ERROR: GATEWAY_API_VERSION/GATEWAY_API_CHANNEL missing from deploy/versions.env"; exit 1; \
	fi; \
	minor=$$(echo "$$want" | sed -E 's/^v([0-9]+\.[0-9]+).*/\1/'); \
	got=$$(sed -n 's/^const MinBundleVersion = "\(.*\)"/\1/p' internal/gateway/version.go); \
	if [ "$$got" != "$$minor" ]; then \
		echo "ERROR: internal/gateway.MinBundleVersion is '$$got' but versions.env pins $$want (minor $$minor)"; fail=1; \
	fi; \
	gotch=$$(sed -n 's/^const RequiredChannel = "\(.*\)"/\1/p' internal/gateway/version.go); \
	if [ "$$gotch" != "$$wantch" ]; then \
		echo "ERROR: internal/gateway.RequiredChannel is '$$gotch' but versions.env pins '$$wantch'"; fail=1; \
	fi; \
	[ "$$fail" = "0" ] || exit 1
	@echo "check-gateway-pin: OK ($(GATEWAY_API_VERSION), $(GATEWAY_API_CHANNEL))"

# Guard: the k8s-monitoring chart pin (docs/gateway-tier-plan.md W9, G8) must
# agree across deploy/versions.env, internal/chartvalues.PinnedChartVersion,
# and the provenance record vendored alongside testdata/values.schema.json —
# otherwise internal/chartvalues could generate values proven against a
# schema for a DIFFERENT chart release than the one it claims to target.
# Offline and fast (no network): the online half — does the vendored schema
# still match what upstream actually publishes for this version — is
# `chart-verify`, below.
check-chartvalues-pin: ## Guard: k8s-monitoring chart version agrees with versions.env
	@fail=0; \
	want="$(K8S_MONITORING_CHART_VERSION)"; \
	if [ -z "$$want" ]; then \
		echo "ERROR: K8S_MONITORING_CHART_VERSION missing from deploy/versions.env"; exit 1; \
	fi; \
	got=$$(sed -n 's/^const PinnedChartVersion = "\(.*\)"/\1/p' internal/chartvalues/version.go); \
	if [ "$$got" != "$$want" ]; then \
		echo "ERROR: internal/chartvalues.PinnedChartVersion is '$$got' but versions.env pins '$$want'"; fail=1; \
	fi; \
	gotmeta=$$(sed -n 's/.*"chart_version": *"\([^"]*\)".*/\1/p' internal/chartvalues/testdata/values.schema.meta.json); \
	if [ "$$gotmeta" != "$$want" ]; then \
		echo "ERROR: internal/chartvalues/testdata/values.schema.meta.json chart_version is '$$gotmeta' but versions.env pins '$$want'"; fail=1; \
	fi; \
	[ "$$fail" = "0" ] || exit 1
	@echo "check-chartvalues-pin: OK ($(K8S_MONITORING_CHART_VERSION))"

# Verify the vendored values.schema.json still matches what upstream actually
# publishes for K8S_MONITORING_CHART_VERSION — the online half check-chartvalues-pin
# (above) cannot do. Re-fetches the exact release URL recorded in
# testdata/values.schema.meta.json's source_url and diffs it byte-for-byte
# against the committed copy; unlike schema-verify's JSON artifact, this file
# has no generated-at timestamp field to strip, so a plain diff is already
# exact. Then runs the package's own test suite, which is where G9's
# stronger checks live (every emitted key proven against the schema, every
# golden validated against it, and a real `helm template` per feature
# combination — see internal/chartvalues/schema_test.go and
# helmtemplate_test.go).
# Prerequisites: network access. Not part of `make lint` — like
# schema-verify, this belongs on a schedule (weekly, or on PRs touching
# deploy/versions.env), not on every local `make lint` run.
chart-verify: check-chartvalues-pin ## Verify the vendored chart schema matches upstream (network; occasional)
	@set -eu; \
	url=$$(sed -n 's/.*"source_url": *"\([^"]*\)".*/\1/p' internal/chartvalues/testdata/values.schema.meta.json); \
	if [ -z "$$url" ]; then echo "ERROR: no source_url in values.schema.meta.json"; exit 1; fi; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	if ! curl -sfL "$$url" -o "$$tmp/fresh.json"; then \
		echo "chart-verify: FAIL — could not fetch $$url"; exit 1; \
	fi; \
	if diff -u internal/chartvalues/testdata/values.schema.json "$$tmp/fresh.json"; then \
		echo "chart-verify: OK (vendored schema matches $$url)"; \
	else \
		echo "chart-verify: FAIL — the vendored schema no longer matches upstream for K8S_MONITORING_CHART_VERSION=$(K8S_MONITORING_CHART_VERSION)."; \
		echo "Re-fetch it, update values.schema.meta.json's sha256/fetched_at, and commit both."; \
		exit 1; \
	fi
	@go test ./internal/chartvalues/ -count=1
	@echo "chart-verify: OK (G9 golden/schema/helm-template checks passed)"

lint: check-single-dist check-dist-consistency check-build-script check-raw-sql check-docker check-no-route-mocks check-gateway-pin check-chartvalues-pin ## Repo guards + golangci-lint
	$(call preflight,golangci-lint,Install golangci-lint v2 (https://golangci-lint.run).)
	@# `golangci-lint run` accepts unknown keys in .golangci.yml without
	@# complaint, so a misplaced or misspelled setting silently does nothing
	@# — including one meant to ENABLE a check. Verifying the config against
	@# its schema first turns that into a loud failure. Found the hard way on
	@# 2026-08-22: a `run.exclude-dirs` key (valid in v1, moved in v2) was
	@# accepted and ignored by `run`, and only `config verify` reported it.
	golangci-lint config verify
	golangci-lint run ./...

# gci via golangci-lint fmt + standalone gofumpt to avoid gci/gofumpt cycle.
fmt: ## Format Go code
	$(call preflight,golangci-lint,Install golangci-lint v2 (https://golangci-lint.run).)
	$(call preflight,gofumpt,Run 'make tools'.)
	golangci-lint fmt ./...
	gofumpt -w $$(find . -name '*.go' ! -path './gen/*' ! -path './internal/store/sqlc/*')

generate: gen-alloy-version ## Regenerate buf + sqlc code (and the Alloy version constant)
	$(call preflight,buf,Run 'make tools'.)
	$(call preflight,sqlc,Run 'make tools'.)
	buf generate
	sqlc generate

# Regenerate internal/version/alloy_gen.go from the single source of truth,
# deploy/versions.env's ALLOY_VERSION. Hermetic (no network) — safe to run
# on every `make generate` and `make schema`. Never hand-edit the output.
gen-alloy-version: ## Regenerate internal/version/alloy_gen.go from versions.env
	@printf '// Code generated by "make gen-alloy-version" from deploy/versions.env. DO NOT EDIT.\n\n// Package version exposes build-time version constants.\npackage version\n\n// AlloySchemaVersion is the pinned Alloy schema version this build serves.\n// Source of truth: ALLOY_VERSION in deploy/versions.env.\nconst AlloySchemaVersion = "alloy-$(ALLOY_VERSION)"\n' > internal/version/alloy_gen.go
	@echo "gen-alloy-version: wrote internal/version/alloy_gen.go (AlloySchemaVersion = alloy-$(ALLOY_VERSION))"

# Regenerate the Alloy component schema artifact (internal/schema/artifacts/alloy-v<X>.json)
# by cloning grafana/alloy at the ALLOY_VERSION pinned in deploy/versions.env and reconciling
# internal/schema/artifacts/overlay.json against the result.
# Prerequisites: network access (clones grafana/alloy), git, and docker — the
# extractor runs in a linux container so the artifact matches the linux fleet
# regardless of host OS. NOT part of `make build` —
# app builds stay hermetic; this is a deliberate, occasional maintenance step.
# Bump procedure: edit ALLOY_VERSION in deploy/versions.env -> make schema ->
# review overlay entries marked "needs_review": true -> commit.
schema: gen-alloy-version preflight-docker ## Regenerate the Alloy schema artifact (network + docker; occasional)
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
# Prerequisites: network access, git, go, jq. CI wiring:
# .github/workflows/schema-verify.yml runs this weekly, on manual dispatch, and
# on PRs touching deploy/versions.env.
schema-verify: gen-alloy-version preflight-docker ## Verify the committed schema artifact matches the pinned Alloy
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
generate-corpus: ## Regenerate visual-builder goldens and sync to web fixtures
	@echo "==> Regenerating goldens from the shipped schema artifact..."
	GEN_GOLDENS=1 go test ./internal/visual/ -run TestGenGoldens
	@echo "==> Syncing visual builder corpus..."
	@mkdir -p web/src/visual/__fixtures__/corpus
	@cp internal/visual/testdata/corpus/*.graph.json web/src/visual/__fixtures__/corpus/
	@cp internal/visual/testdata/corpus/*.golden.alloy web/src/visual/__fixtures__/corpus/
	@echo "==> Done. $(shell ls internal/visual/testdata/corpus/*.graph.json | wc -l | tr -d ' ') corpus entries synced."

# Kubernetes e2e suite (kind). Creates its own cluster, installs Calico,
# runs the specs, and destroys the cluster — including on failure and panic.
#
# The suite opens with a negative control that PROVES the CNI enforces
# NetworkPolicy before any containment assertion is trusted. If the CNI does
# not enforce, every spec fails loudly rather than passing for the wrong
# reason. See docs/kind-test-environment-plan.md.
#
#   E2E_K8S_KEEP=1        leave the cluster up for debugging
#   E2E_K8S_NODE_IMAGE=   pin a different Kubernetes version
#   E2E_K8S_ARTIFACTS=dir export cluster logs on failure
#
# -count=1 defeats the test cache: a cached PASS from a previous cluster would
# be worthless here, since the whole point is what a live cluster does.
# Prerequisites match every other e2e target (e2e, e2e-sim, e2e-egress,
# test-fullstack): the images the cluster loads must be built from THIS working
# tree, not whatever the daemon happens to be holding. The suite also checks
# this itself, for anyone running `go test -tags e2ek8s` directly.
e2e-k8s: preflight-k8s docker-build-local docker-build-simulator ## Kubernetes e2e suite on a fresh kind cluster (~3-5 min; 45m timeout budget)
	go test -tags e2ek8s -count=1 -timeout 45m -v ./e2e/k8s/...

# Removes clusters a killed run (SIGKILL) left behind — the one case the
# suite's own teardown cannot cover.
e2e-k8s-clean: ## Delete kind clusters a SIGKILLed e2e-k8s run left behind
	@for c in $$(kind get clusters 2>/dev/null | grep '^shepherd-e2e-' || true); do \
		echo "deleting leftover cluster $$c"; kind delete cluster --name "$$c"; \
	done; echo "e2e-k8s-clean: done"

helm-lint: ## Lint + template the Helm chart against both ci value files
	$(call preflight,helm,Install helm (e.g. brew install helm).)
	helm lint deploy/helm/shepherd
	helm template shepherd deploy/helm/shepherd -f deploy/helm/shepherd/ci/default-values.yaml > /dev/null
	helm template shepherd deploy/helm/shepherd -f deploy/helm/shepherd/ci/full-values.yaml > /dev/null

# Local defaults for the docker image templates; CI overrides both.
IMAGE_REGISTRY ?= ghcr.io/procoduck
SOURCE_URL ?= https://github.com/procoduck/sheperd
release-snapshot: ## GoReleaser dry run
	IMAGE_REGISTRY=$(IMAGE_REGISTRY) SOURCE_URL=$(SOURCE_URL) goreleaser release --snapshot --clean --skip=publish

# Build the app image for e2e and dev stacks (use docker-build-init for the
# init/CLI image). Native platform — CI's amd64 runners and local arm64
# machines both build what they run. --load so the image reaches the daemon
# even under a docker-container buildx builder; without it buildx exits 0 with
# the image only in the build cache and compose/kind silently reuse the
# previous shepherd:local. No build-web prerequisite: the Dockerfile rebuilds
# the SPA in-stage and overwrites whatever the host tree holds, so a host
# build would be discarded (and would dirty the tracked internal/spa/dist).
# The shepherd image bundles the alloy binary so Stage 2 of the validation
# gate (`alloy validate`) can run. alloy is DYNAMICALLY linked, so the image
# has to carry a dynamic loader — distroless/base, not distroless/static.
# Copying it into a static base silently produces an image where the file is
# present and unrunnable, and Stage 2 then fails on every pipeline save with
# an error that names the loader rather than the cause. Found by a browser
# walkthrough of a real Helm deployment, not by any test: the compose and dev
# stacks leave alloy_binary empty, which SKIPS Stage 2 instead of failing it.
#
# $(1) = image tag to check.
define check-alloy-runnable
@docker run --rm --entrypoint /usr/local/bin/alloy $(1) --version >/dev/null 2>&1 || { \
	echo "ERROR: $(1) cannot execute /usr/local/bin/alloy."; \
	echo "       Stage 2 (alloy validate) would fail on every pipeline save."; \
	echo "       alloy is dynamically linked: the final stage needs"; \
	echo "       DISTROLESS_BASE_IMAGE (distroless/base), not distroless/static."; \
	exit 1; }
@echo "check-alloy-runnable: OK ($(1) can run alloy validate)"
endef

docker-build-local: ## Build shepherd:local from deploy/Dockerfile.local
	docker buildx build --load $(DOCKER_BUILD_ARGS) $(DOCKER_CACHE_FLAGS) -f deploy/Dockerfile.local -t shepherd:local .
	$(call check-alloy-runnable,shepherd:local)

# Deprecated alias. The docker-build/docker-build-local split (two Dockerfiles,
# same tag, conflicting platforms) is gone; deploy/Dockerfile.local is the one
# local Dockerfile.
docker-build: docker-build-local

docker-build-init: ## Build shepherd:local-init (init/CLI image for migrate + seed)
	docker buildx build --load $(DOCKER_BUILD_ARGS) $(DOCKER_CACHE_FLAGS) -f deploy/Dockerfile.init -t shepherd:local-init .

# Build the S3 sandbox simulator image (VB-1 §6.4). Same DOCKER_BUILD_ARGS as the
# app image so the sandbox runs the Alloy build pinned in deploy/versions.env —
# a sandbox on a different Alloy would make S3 results lie about the fleet.
docker-build-simulator: ## Build the S3 sandbox simulator image
	docker buildx build --load $(DOCKER_BUILD_ARGS) $(DOCKER_CACHE_FLAGS) -f deploy/Dockerfile.simulator -t shepherd-simulator:local .
	docker tag shepherd-simulator:local shepherd-simulator:dev

# Start the local dev stack (postgres, shepherd, mockmsft, gitea, and the three
# Alloy agents — those start by default; only oidc and sim are profile-gated).
# The image prerequisites rebuild shepherd:local and shepherd:local-init from
# this tree; compose's --build cannot, because those services reference an
# image with no build section.
dev: docker-build-local docker-build-init ## Start the local dev stack (login admin/admin at :8080)
	docker compose -f dev/docker-compose.dev.yaml up -d --build --wait
	@echo ""
	@echo "=========================================================="
	@echo "  Shepherd dev stack is running!"
	@echo "  URL:      http://localhost:8080"
	@echo "  Login:    admin / admin"
	@echo "  DB:       postgres://shepherd:shepherd@localhost:15432/shepherd_dev"
	@echo "  Reset:    make dev-reset && make dev"
	@echo "=========================================================="

# Requires: make dev (backend must be healthy at localhost:8080).
dev-frontend: ## Start the Vite dev server (HMR) against the running dev backend
	@curl -sf http://localhost:8080/healthz > /dev/null || (echo "ERROR: shepherd is not running. Run 'make dev' first."; exit 1)
	cd web && $(PNPM) dev

dev-restart: build-web ## Rebuild the shepherd image and restart its container (5-10s with layer cache)
	docker compose -f dev/docker-compose.dev.yaml build shepherd
	docker compose -f dev/docker-compose.dev.yaml up -d shepherd

dev-seed: ## Re-run the dev seed (idempotent — safe on a running stack)
	docker compose -f dev/docker-compose.dev.yaml run --rm shepherd-seed

dev-reset: ## Stop the dev stack and wipe all data (named volumes)
	docker compose -f dev/docker-compose.dev.yaml down -v

# Start the dev stack with the S3 sandbox simulator (VB-1 §6.4).
# Without SHEPHERD_SIM_ENABLED the shepherd container comes up with the feature
# off, which is what every ordinary `make dev` exercises.
dev-sim: docker-build-simulator ## Start the dev stack with the S3 sandbox simulator
	SHEPHERD_SIM_ENABLED=true docker compose -f dev/docker-compose.dev.yaml --profile sim up -d --build --wait

# Run the fullstack Playwright suite against the dev stack.
# Boots the dev stack, runs tests, captures logs on failure, tears down.
test-fullstack: docker-build-local docker-build-init ## Playwright fullstack suite against the dev stack
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
