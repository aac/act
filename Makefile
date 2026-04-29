.PHONY: build test lint fmt tidy fuzz

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

fuzz:
	go test -fuzz=Fuzz -fuzztime=10s ./internal/fold/
