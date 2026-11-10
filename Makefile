# Build, lint and test the Go port of gulp.

GO ?= go

.PHONY: all build fmt vet test cover

all: fmt vet test

build:
	$(GO) build ./...

fmt:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
