.PHONY: generate build test test-race test-cluster lint coverage docker up setup-hooks

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

coverage:
	go test -p=1 -timeout=2m -coverpkg=./cmd/...,./internal/... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

docker:
	docker build -t blabby .

up:
	docker compose up

setup-hooks:
	git config core.hooksPath .githooks
