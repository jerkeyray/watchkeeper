SHELL := /bin/sh
COMPOSE := docker compose -f deploy/compose/compose.yaml

.PHONY: bootstrap fmt fmt-check test test-race vet build migrate migrate-simulator compose-up compose-down smoke recovery-smoke smoke-all demo demo-cli

bootstrap:
	go mod download

fmt:
	gofmt -w cmd internal pkg

fmt-check:
	test -z "$$(gofmt -l cmd internal pkg)"

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

build:
	go build ./cmd/...

migrate:
	DATABASE_URL="$${WK_DATABASE_URL}" go run ./cmd/migrate -dir migrations/watchkeeper

migrate-simulator:
	DATABASE_URL="$${SIM_DATABASE_URL}" go run ./cmd/migrate -dir migrations/simulator

compose-up:
	$(COMPOSE) up -d --build --wait postgres api simulator coordinator

compose-down:
	$(COMPOSE) down

smoke: compose-up
	$(COMPOSE) --profile tools run --rm worker

recovery-smoke: compose-up
	$(COMPOSE) --profile tools run --rm recovery-smoke

smoke-all: smoke recovery-smoke

.PHONY: demo
demo: compose-up
	go run ./cmd/watchkeeper-demo

.PHONY: demo-cli
demo-cli: compose-up
	go run ./cmd/demo

.PHONY: bench-smoke bench-email
bench-smoke:
	./scripts/benchmark.sh run benchmarks/smoke.json

bench-email:
	./scripts/benchmark.sh run benchmarks/email.json

.PHONY: report evidence-check
report:
	python3 scripts/build-report.py

evidence-check:
	python3 scripts/analyze-evidence.py
