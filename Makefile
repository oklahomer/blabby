.PHONY: generate build test test-race test-cluster lint spec-lint docs-preview coverage docker up db-reset db-shell setup-hooks

DOCS_PORT ?= 8081
ASYNCAPI_PORT ?= 8082

# Dev database credentials used by db-shell; match the docker-compose.yml defaults.
POSTGRES_USER ?= blabby
POSTGRES_DB ?= blabby

generate:
	buf generate

build:
	go build ./cmd/backend/ ./cmd/gateway/ ./cmd/client/

# The default gate combines race coverage with the focused multi-member test.
test: test-race test-cluster

test-race:
	go test -race ./...

# Proto.Actor cannot run multiple in-process members cleanly under -race.
test-cluster:
	go test -count=1 ./internal/clusterboot -run '^TestMultiMemberDepartureAndReactivation$$'

lint:
	golangci-lint run

# Validate the API specs against their OpenAPI / AsyncAPI schema versions.
# CLIs are fetched on demand via npx, so no Node dependency is committed to
# the repo; running this target requires Node and npx on PATH. Redocly reads
# redocly.yaml + .redocly.lint-ignore.yaml automatically.
#
# The AsyncAPI CLI forces NODE_ENV=production internally, which makes its
# bundled node-config print two strict-mode warnings unrelated to the spec.
# SUPPRESS_NO_CONFIG_WARNING silences the no-config-dir notice; the grep drops
# the two residual node-config lines while preserving the validator's own
# output and exit code.
spec-lint:
	npx --yes @redocly/cli@2 lint api/openapi.yaml
	@out=$$(SUPPRESS_NO_CONFIG_WARNING=true npx --yes @asyncapi/cli@6 validate api/asyncapi.yaml 2>&1); status=$$?; \
		printf '%s\n' "$$out" | grep -vE "WARNING: NODE_ENV value of .* did not match|node-config/wiki/Strict-Mode"; \
		exit $$status

docs-preview:
	go run ./cmd/docs-preview --port $(DOCS_PORT) --asyncapi-port $(ASYNCAPI_PORT)

# Coverage measures the product/server core under internal/: domain types,
# grains, gateway, auth, supervision, and test-cluster wiring. The repository's
# headline coverage target for this scope is at least 80%.
#
# The cmd/* packages are intentionally excluded from this number. The four main
# packages (backend, gateway, client, docs-preview) are bootstrap, signal
# handling, and program.Run() orchestration that runs only in a real process and
# is inherently hard to unit-test; including them makes the headline primarily
# measure bootstrap paths and obscures product-code coverage. The cmd/* trees
# (including the well-tested cmd/client/internal/* TUI packages) are still
# compiled and exercised by `make test`; they are simply out of this headline.
coverage:
	go test -p=1 -timeout=2m -coverpkg=./internal/... -coverprofile=coverage.out ./internal/...
	go tool cover -html=coverage.out -o coverage.html

docker:
	docker build -t blabby .

up:
	docker compose up

setup-hooks:
	git config core.hooksPath .githooks

# Recreate the database from a clean volume. `docker compose down -v` removes the
# named db-data volume; bringing postgres back up re-runs the entrypoint, which
# applies internal/persistence/schema.sql. This is the canonical way to apply the
# current schema, since the init script runs only against an empty data directory.
db-reset:
	docker compose down -v
	docker compose up -d postgres

# Open an interactive psql shell against the running postgres service.
db-shell:
	docker compose exec postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB)
