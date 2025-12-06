# Makefile for busker/swash
#
# This Makefile sets up CGO_CFLAGS to use vendored systemd headers,
# eliminating the need to install libsystemd-dev.

# Vendored systemd headers location
CGO_CFLAGS := -I$(CURDIR)/cvendor

export CGO_CFLAGS

.PHONY: all build test clean

all: build

build:
	go build ./...

test:
	go test ./...

# Build specific binaries
swash:
	go build -o bin/swash ./cmd/swash/

mini-systemd:
	go build -o bin/mini-systemd ./cmd/mini-systemd/

clean:
	rm -rf bin/
