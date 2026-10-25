
.PHONY: run build test test-race fmt fmt-check vet tidy check

run:
	go run ./cmd/hotel-parser

build:
	mkdir -p bin
	go build -o bin/hotel-parser ./cmd/hotel-parser

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
