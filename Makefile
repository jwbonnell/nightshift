VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install test vet fmt vendor

build:
	go build -ldflags "$(LDFLAGS)" -o bin/nightshift ./cmd/nightshift

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/nightshift

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l cmd internal

vendor:
	go mod tidy && go mod vendor
