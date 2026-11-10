
.PHONY: run build test test-race test-integration test-integration-docker fmt fmt-check vet tidy check

run:
	go run ./cmd/hotel-parser

build:
	mkdir -p bin
	go build -o bin/hotel-parser ./cmd/hotel-parser
	go build -o bin/collection ./cmd/collection

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l $$(git ls-files '*.go'))" || \
	(echo "Go files are not formatted"; exit 1)

vet:
	go vet ./...

tidy:
	go mod tidy

check: fmt-check vet test-race build

# Явный локальный DSN; .env автоматически не загружается.
test-integration:
	go test -tags=integration -count=1 ./internal/storage/... ./internal/collect/... ./internal/schedule/... ./internal/vercelapp/...

# PostgreSQL существующего docker compose, без подбора credentials/портов хоста.
test-integration-docker:
	python3 scripts/test-integration-docker.py
