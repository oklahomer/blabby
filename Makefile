.PHONY: generate build test lint coverage docker up setup-hooks

generate:
	buf generate

build:
	go build ./cmd/server/ ./cmd/client/

test:
	go test -race ./...

lint:
	golangci-lint run

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

docker:
	docker build -t blabby .

up:
	docker compose up

setup-hooks:
	git config core.hooksPath .githooks
