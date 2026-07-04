COMPOSE  := docker compose -f infra/docker-compose.yml
SERVICES := backend/archive-worker backend/auth backend/chunk-splitter backend/db-parser \
            backend/db-service backend/email-parser backend/llm-service backend/main-service \
            backend/ocr-service backend/office-parser backend/pdf-parser backend/vlm-service
PLATFORM := backend/platform
GOBIN    ?= $(shell if command -v go >/dev/null 2>&1; then go env GOPATH; else printf '%s/go' "$$HOME"; fi)/bin
COVER_MIN ?= 80
ENV_FILE := infra/.env
ENV_EXAMPLE := infra/.env.example

# ---- frontend (SPA) ----
FRONTEND       := frontend
BUN            := bun
# Gateway the SPA dev-server proxies /api (and the /ws socket) to. Defaults to
# the `make up` gateway; override with `make frontend-dev HTTP_PORT=9000` or
# `make frontend-dev RAG_API=http://host:port`.
HTTP_PORT      ?= 8080
RAG_API        ?= http://localhost:$(HTTP_PORT)
FRONTEND_PORT  ?= 5173
FRONTEND_STAMP := $(FRONTEND)/node_modules/.install.stamp

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## go mod tidy in the platform and every service module
	cd $(PLATFORM) && go mod tidy
	@for s in $(SERVICES); do echo "tidy $$s"; (cd $$s && go mod tidy) || exit 1; done

.PHONY: build
build: ## go build ./... for the platform and every service
	cd $(PLATFORM) && go build ./...
	@for s in $(SERVICES); do echo "build $$s"; (cd $$s && go build ./...) || exit 1; done

.PHONY: vet
vet: ## go vet every module
	cd $(PLATFORM) && go vet ./...
	@for s in $(SERVICES); do echo "vet $$s"; (cd $$s && go vet ./...) || exit 1; done

.PHONY: test
test: ## go test ./... for the platform and every service (mirrors CI)
	cd $(PLATFORM) && go test ./...
	@for s in $(SERVICES); do echo "test $$s"; (cd $$s && go test ./...) || exit 1; done

.PHONY: test-race
test-race: ## go test -race ./... for the platform and every service
	cd $(PLATFORM) && go test -race ./...
	@for s in $(SERVICES); do echo "test-race $$s"; (cd $$s && go test -race ./...) || exit 1; done

.PHONY: lint
lint: ## golangci-lint every module (sequential: the linter holds a global lock)
	cd $(PLATFORM) && $(GOBIN)/golangci-lint run ./...
	@for s in $(SERVICES); do echo "lint $$s"; (cd $$s && $(GOBIN)/golangci-lint run ./...) || exit 1; done

.PHONY: fmt
fmt: ## gofmt the whole tree
	gofmt -w $(SERVICES) $(PLATFORM)/pkg

.PHONY: prereq
prereq: ## Check host prerequisites for the Docker stack
	@command -v docker >/dev/null 2>&1 || { echo "error: docker is not installed"; exit 1; }
	@docker compose version >/dev/null 2>&1 || { echo "error: Docker Compose v2 is required"; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "error: Docker daemon is not reachable"; exit 1; }
	@docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"nvidia"' || { \
		echo "error: Docker runtime 'nvidia' is required for the GPU services."; \
		echo "       Install NVIDIA Container Toolkit, then retry 'make up'."; \
		exit 1; \
	}

.PHONY: env
env: env-bootstrap ## Create infra/.env and fill missing local secrets

.PHONY: env-bootstrap
env-bootstrap:
	@set -eu; \
	if [ ! -f "$(ENV_EXAMPLE)" ]; then \
		echo "error: $(ENV_EXAMPLE) not found"; exit 1; \
	fi; \
	created=0; changed=0; \
	if [ ! -f "$(ENV_FILE)" ]; then \
		cp "$(ENV_EXAMPLE)" "$(ENV_FILE)"; \
		created=1; changed=1; \
	fi; \
	chmod 600 "$(ENV_FILE)"; \
	rand_hex() { od -An -N"$$1" -tx1 /dev/urandom | tr -d ' \n'; }; \
	set_if_blank() { \
		key="$$1"; value="$$2"; \
		if grep -q "^$${key}=" "$(ENV_FILE)"; then \
			if grep -q "^$${key}=$$" "$(ENV_FILE)"; then \
				tmp=$$(mktemp); \
				awk -v k="$$key" -v v="$$value" 'BEGIN{done=0} $$0 ~ "^" k "=" && done == 0 { print k "=" v; done=1; next } { print }' "$(ENV_FILE)" > "$$tmp"; \
				mv "$$tmp" "$(ENV_FILE)"; \
				changed=1; \
			fi; \
		else \
			printf '\n%s=%s\n' "$$key" "$$value" >> "$(ENV_FILE)"; \
			changed=1; \
		fi; \
	}; \
	set_if_blank JWT_SECRET "$$(rand_hex 48)"; \
	set_if_blank ADMIN_PASSWORD "$$(rand_hex 18)"; \
	set_if_blank GRAFANA_ADMIN_PASSWORD "$$(rand_hex 18)"; \
	set_if_blank RABBITMQ_PASSWORD "$$(rand_hex 18)"; \
	set_if_blank POSTGRES_PASSWORD "$$(rand_hex 18)"; \
	set_if_blank S3_ACCESS_KEY "rag$$(rand_hex 8)"; \
	set_if_blank S3_SECRET_KEY "$$(rand_hex 32)"; \
	if [ "$$created" = 1 ]; then \
		echo "created $(ENV_FILE) with generated local secrets"; \
	elif [ "$$changed" = 1 ]; then \
		echo "filled missing secrets in $(ENV_FILE)"; \
	fi

.PHONY: up
up: prereq env-bootstrap ## Build images and start the full stack (including frontend on FRONTEND_PORT)
	$(COMPOSE) up -d --build
	@front_port=$$(awk -F= '/^FRONTEND_PORT=/{print $$2; exit}' "$(ENV_FILE)"); \
	api_port=$$(awk -F= '/^HTTP_PORT=/{print $$2; exit}' "$(ENV_FILE)"); \
	echo "frontend: http://localhost:$${front_port:-5173}"; \
	echo "api gateway: http://localhost:$${api_port:-8080}"; \
	echo "admin login: admin / see ADMIN_PASSWORD in $(ENV_FILE)"

.PHONY: up-logging
up-logging: prereq env-bootstrap ## Start the stack including the Vector log collector
	$(COMPOSE) --profile logging up -d --build
	@echo "admin login: admin / see ADMIN_PASSWORD in $(ENV_FILE)"

.PHONY: down
down: ## Stop the stack
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete all volumes (DATA LOSS)
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Tail logs from all containers
	$(COMPOSE) logs -f

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

.PHONY: hooks
hooks: frontend-install ## Install lefthook git hooks (pre-commit fmt+lint)
	cd $(FRONTEND) && bunx lefthook install

.PHONY: s3-drift-check
s3-drift-check: ## Check that every Postgres document row has a matching S3 object
	python scripts/tools/data/check_s3_db_drift.py

.PHONY: demo-smoke
demo-smoke: ## Check demo readiness without spending generation LLM calls
	python scripts/tools/demo/demo_smoke.py

.PHONY: demo-smoke-full
demo-smoke-full: ## Check demo readiness including one full RAG answer
	python scripts/tools/demo/demo_smoke.py --full-rag

.PHONY: demo-ui-smoke
demo-ui-smoke: ## Check the served frontend bundle and visible hypotheses board
	python scripts/tools/demo/demo_smoke.py \
		--check-frontend \
		--frontend-url http://localhost:$(FRONTEND_PORT) \
		--min-visible-hypotheses 20 \
		--check-visible-hypothesis-quality

.PHONY: demo-env-check
demo-env-check: ## Check demo env without printing secret values
	python scripts/tools/demo/demo_smoke.py \
		--check-env \
		--require-generation-env \
		--env-only

.PHONY: demo-security-check
demo-security-check: ## Strict env/secret check for non-local exposure
	python scripts/tools/demo/demo_smoke.py \
		--check-env \
		--require-generation-env \
		--strict-secrets \
		--env-only

.PHONY: demo-rotate-secrets
demo-rotate-secrets: ## Rotate local demo JWT/admin secrets and update the Postgres admin hash
	python scripts/tools/demo/rotate_demo_secrets.py

.PHONY: demo-resilience
demo-resilience: ## Check critical RAG graceful-degradation unit tests
	cd backend/llm-service && go test ./internal/application \
		-run 'TestGenerationFailureReturnsExtractiveFallbackForChat|TestGenerationFailurePropagatesForStructuredPrompt|TestP0_2_DegradedAbstainNotCached'

.PHONY: config
config: env-bootstrap ## Validate the compose file
	$(COMPOSE) config -q && echo "compose OK"

# ---------------- frontend (SPA) ----------------
# The SPA also has a compose service for `make up`; these targets are for local
# frontend development/checks outside Docker.

.PHONY: frontend-check-bun
frontend-check-bun:
	@command -v $(BUN) >/dev/null 2>&1 || { \
		echo "error: '$(BUN)' not found in PATH. Install bun: https://bun.sh"; exit 1; }

# Install only when the manifest or lockfile changes. node_modules is gitignored,
# so a stamp file (not the dir) is the dependency target.
$(FRONTEND_STAMP): $(FRONTEND)/package.json $(FRONTEND)/bun.lock | frontend-check-bun
	cd $(FRONTEND) && $(BUN) install --frozen-lockfile --registry https://registry.npmjs.org --network-concurrency 4
	@touch $@

.PHONY: frontend-install
frontend-install: $(FRONTEND_STAMP) ## Install frontend deps (bun install, only when lockfile changes)

.PHONY: frontend-dev
frontend-dev: frontend-install ## Run local Vite dev server on :5173 (proxies /api to RAG_API)
	cd $(FRONTEND) && RAG_API=$(RAG_API) $(BUN) run dev --host

.PHONY: frontend-build
frontend-build: frontend-install ## Type-check + production build of the SPA into frontend/dist
	cd $(FRONTEND) && RAG_API=$(RAG_API) $(BUN) run build

.PHONY: frontend-preview
frontend-preview: frontend-build ## Preview the production SPA build locally
	cd $(FRONTEND) && RAG_API=$(RAG_API) $(BUN) run preview --host

.PHONY: frontend-typecheck
frontend-typecheck: frontend-install ## Type-check the SPA (tsc)
	cd $(FRONTEND) && $(BUN) run typecheck

.PHONY: frontend-lint
frontend-lint: frontend-install ## Lint the SPA (oxlint + project conventions)
	cd $(FRONTEND) && $(BUN) run lint

.PHONY: frontend-fmt
frontend-fmt: frontend-install ## Format the SPA (oxfmt)
	cd $(FRONTEND) && $(BUN) run fmt

.PHONY: frontend-fmt-check
frontend-fmt-check: frontend-install ## Check SPA formatting without writing (oxfmt --check)
	cd $(FRONTEND) && $(BUN) run fmt:check

.PHONY: frontend-check
frontend-check: frontend-typecheck frontend-lint frontend-fmt-check ## Run all SPA checks (typecheck + lint + fmt:check)

.PHONY: dev
dev: ## Start backend containers, then run local Vite frontend dev server
	$(COMPOSE) up -d --build --scale frontend=0
	$(MAKE) frontend-dev
