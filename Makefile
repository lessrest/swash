# Makefile for busker/swash
#
# This Makefile sets up CGO_CFLAGS to use vendored systemd headers,
# eliminating the need to install libsystemd-dev.

# Vendored systemd headers location
CGO_CFLAGS := -I$(CURDIR)/cvendor

export CGO_CFLAGS

.PHONY: all build test test-unit test-integration clean

all: build

build: bin/swash bin/mini-systemd

bin/swash: $(shell find . -name '*.go' -not -path './test/*')
	go build -o $@ ./cmd/swash/

bin/mini-systemd: $(shell find . -name '*.go' -not -path './test/*')
	go build -o $@ ./cmd/mini-systemd/

test: test-unit test-integration

test-unit:
	go test ./pkg/... ./internal/...

test-integration:
	./test/integration.sh

clean:
	rm -rf bin/
