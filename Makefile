.PHONY: run build test fmt vet tidy

run:
	go run ./cmd/hotel-parser

build:
	mkdir -p bin
	go build -o bin/hotel-parser ./cmd/hotel-parser

test:
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

check: fmt vet test