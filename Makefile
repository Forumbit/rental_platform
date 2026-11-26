# Commands load .env, so DATABASE_URL and TEST_DATABASE_URL come from there.
CONFIG := config/config.yaml
ENV    := .env
LOAD_ENV := set -a && . ./$(ENV) && set +a

.PHONY: db migrate seed run test test-unit lint

db: ## Start PostgreSQL and wait until it is healthy
	docker compose up -d --wait postgres

migrate: ## Apply database migrations
	go run ./cmd/migrate -config $(CONFIG) -env $(ENV)

seed: ## Create the initial user
	go run ./cmd/seed-user -config $(CONFIG) -env $(ENV)

run: ## Start the API
	go run ./cmd/api -config $(CONFIG) -env $(ENV)

test: ## All tests, including PostgreSQL integration tests (needs `make db`)
	$(LOAD_ENV) && test -n "$$TEST_DATABASE_URL" && go test -count=1 -race ./...

test-unit: ## Tests that do not need PostgreSQL
	TEST_DATABASE_URL= go test -race ./...

lint: ## Formatting and go vet
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
