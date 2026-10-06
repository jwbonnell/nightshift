VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install test test-hermetic vet fmt vendor

build:
	go build -ldflags "$(LDFLAGS)" -o bin/nightshift ./cmd/nightshift

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/nightshift

test:
	go test ./...

# Re-runs the suite with no HOME and no global/system git config, so it
# can't pass by quietly relying on the host's ~/.gitconfig the way a real
# sbx verify sandbox's clean HOME would expose.
test-hermetic:
	HOME=$$(mktemp -d) GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l cmd internal

vendor:
	go mod tidy && go mod vendor
