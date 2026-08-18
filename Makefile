.PHONY: build build-web build-all test test-integration e2e smoke test-ui check-single-dist check-build-script check-raw-sql check-docker lint fmt generate generate-corpus helm-lint release-snapshot docker-build docker-build-local docker-build-init migrate dev dev-frontend dev-restart dev-seed dev-reset test-fullstack

PNPM ?= pnpm

# Read canonical version pins from deploy/versions.env
include deploy/versions.env
export

DOCKER_BUILD_ARGS := \
	--build-arg GO_IMAGE=$(GO_IMAGE) \
	--build-arg NODE_IMAGE=$(NODE_IMAGE) \
	--build-arg ALLOY_IMAGE=$(ALLOY_IMAGE) \
	--build-arg DISTROLESS_IMAGE=$(DISTROLESS_IMAGE) \
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
e2e:
	docker compose -f e2e/docker-compose.e2e.yaml up -d --build --wait
	ginkgo --tags=e2e --randomize-all=false ./e2e/...
	@if [ "$(E2E_KEEP)" != "1" ]; then \
		docker compose -f e2e/docker-compose.e2e.yaml down -v; \
	else \
		echo "E2E_KEEP=1: stack left running. Run 'docker compose -f e2e/docker-compose.e2e.yaml down -v' to clean up."; \
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
check-docker:
	@HARDCODED=$$(grep -n '^FROM ' deploy/Dockerfile deploy/Dockerfile.local deploy/Dockerfile.goreleaser \
	    | grep -v 'FROM \$${\|AS '); \
	if [ -n "$$HARDCODED" ]; then \
		echo "ERROR: hardcoded FROM found (should use ARG variables):"; \
		echo "$$HARDCODED"; \
		exit 1; \
	fi
	@echo "check-docker: OK"

lint: check-single-dist check-build-script check-raw-sql check-docker
	golangci-lint run ./...

# Format Go code (gci via golangci-lint fmt + standalone gofumpt to avoid gci/gofumpt cycle)
fmt:
	golangci-lint fmt ./...
	gofumpt -w $$(find . -name '*.go' ! -path './gen/*' ! -path './internal/store/sqlc/*')

# Codegen
generate:
	buf generate
	sqlc generate

# Sync visual-builder corpus from Go testdata → web fixtures.
# Run after adding or changing corpus entries.
generate-corpus:
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

# Run the fullstack Playwright suite against the dev stack.
# Boots the dev stack, runs tests, captures logs on failure, tears down.
test-fullstack:
	docker compose -f dev/docker-compose.dev.yaml up -d --build --wait
	cd web && $(PNPM) exec playwright test --config playwright.fullstack.config.ts; \
		STATUS=$$?; \
		if [ $$STATUS -ne 0 ]; then \
			docker compose -f dev/docker-compose.dev.yaml logs --no-color > /tmp/fullstack-stack.log 2>&1; \
			echo "Stack logs saved to /tmp/fullstack-stack.log"; \
		fi; \
		docker compose -f dev/docker-compose.dev.yaml down -v; \
		exit $$STATUS
