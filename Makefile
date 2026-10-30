MODULE   := github.com/fiankasepmantea/go-gin-tenant-template
# Application mode: development (default) or production. Example: make run ENV=production
ENV      ?= development
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
    -X $(MODULE)/internal/version.Version=$(VERSION) \
    -X $(MODULE)/internal/version.Commit=$(COMMIT) \
    -X $(MODULE)/internal/version.BuildDate=$(DATE)

.DEFAULT_GOAL := help
.PHONY: help env-init hooks run build test test-race test-integration cover fmt vet lint vuln tidy keygen \
        docker-up docker-down docker-build proto migrate-new migrate-up migrate-down

help: ## Show the list of commands
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n",$$1,$$2}'

env-init: ## Create .env.$(ENV) from the example + a new PASETO_KEY (never overwrites)
	@scripts/init-env.sh $(ENV)

hooks: ## Enable the git hook that rejects commits of env files
	git config core.hooksPath scripts/hooks

run: ## Run the REST server for ENV (needs .env.$(ENV))
	APP_ENV=$(ENV) go run ./cmd/server

build: ## Build all binaries into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tenantctl ./cmd/cli

test: ## Unit tests
	go test ./... -count=1

test-race: ## Unit testss with the race detector
	go test ./... -count=1 -race

test-integration: ## Tests against a real PostgreSQL (reads DB_* from .env.$(ENV), or TEST_DATABASE_URL)
	@set -a; [ ! -f .env.$(ENV) ] || . ./.env.$(ENV); set +a; \
	go test -tags integration ./test/integration/... -count=1

cover: ## Coverage report (coverage.html)
	go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

fmt: ## Format code
	gofmt -s -w .

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (v2)
	golangci-lint run

vuln: ## Check dependencies for vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy: ## Tidy go.mod/go.sum
	go mod tidy

keygen: ## Generate a PASETO_KEY value
	@go run ./cmd/cli keygen

docker-build: ## Build the application image
	docker build -f deployments/docker/Dockerfile -t go-gin-tenant-template:$(VERSION) --build-arg VERSION=$(VERSION) .

docker-up: ## Start Postgres + Redis (add PROFILES="kafka rabbitmq app" if needed)
	docker compose --env-file .env.$(ENV) -f deployments/docker/docker-compose.yml $(foreach p,$(PROFILES),--profile $(p)) up -d

docker-down: ## Stop all containers
	docker compose --env-file .env.$(ENV) -f deployments/docker/docker-compose.yml --profile kafka --profile rabbitmq --profile app down

proto: ## Generate gRPC code from api/proto (needs buf)
	buf generate

migrate-new: ## Create a new migration: make migrate-new name=create_tenants
	migrate create -ext sql -dir migrations -seq $(name)

migrate-up: ## Apply all migrations (needs DATABASE_URL)
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the last migration (needs DATABASE_URL)
	migrate -path migrations -database "$(DATABASE_URL)" down 1
