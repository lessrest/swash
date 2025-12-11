# Makefile for swash
#
# This Makefile sets up CGO_CFLAGS to use vendored systemd headers,
# eliminating the need to install libsystemd-dev.

# Vendored systemd headers location
CGO_CFLAGS := -I$(CURDIR)/cvendor

export CGO_CFLAGS

.PHONY: all build test test-unit test-integration install clean generate

all: build

generate:
	go generate ./cmd/swash/templates/

build: generate bin/swash bin/mini-systemd

bin/swash: $(shell find . -name '*.go' -not -path './test/*')
	go build -o $@ ./cmd/swash/

bin/mini-systemd: $(shell find . -name '*.go' -not -path './test/*')
	go build -o $@ ./cmd/mini-systemd/

install:
	go install ./cmd/swash/

test: test-unit test-integration

test-unit:
	go test ./pkg/... ./internal/...

test-integration: build
	go test ./integration/... -v -timeout 120s

clean:
	rm -rf bin/
