# Makefile for swash
#
# This Makefile sets up CGO_CFLAGS to use vendored systemd headers,
# eliminating the need to install libsystemd-dev.

# Vendored systemd headers location
CGO_CFLAGS := -I$(CURDIR)/cvendor

export CGO_CFLAGS

# Detect platform for test defaults
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
  # macOS: use posix backend and native journal reader
  SWASH_TEST_MODE ?= posix
  SWASH_TEST_JOURNAL_READER ?= native
else
  # Linux: use mini-systemd (or real systemd) with journalctl
  SWASH_TEST_MODE ?= mini
  SWASH_TEST_JOURNAL_READER ?= journalctl
endif

export SWASH_TEST_MODE
export SWASH_TEST_JOURNAL_READER

.PHONY: all build test test-unit test-integration install clean generate oxigraph-wasm

all: build

generate:
	go generate ./cmd/swash/templates/

build: generate bin/swash

bin/swash: $(shell find . -name '*.go' -not -path './test/*')
	go build -o $@ ./cmd/swash/

install:
	go install ./cmd/swash/

test: test-unit test-integration

test-unit:
	go test ./pkg/... ./internal/...

test-integration: build
	@echo "Test mode: $(SWASH_TEST_MODE), journal reader: $(SWASH_TEST_JOURNAL_READER)"
	go test ./integration/... -v -timeout 120s

clean:
	rm -rf bin/

# Build oxigraph WASI module (compressed blob is embedded and decompressed at runtime)
OXIGRAPH_WASM_ZST := cmd/oxigraph-poc/oxigraph.wasm.zst
OXIGRAPH_SOURCES := oxigraph-wasi-ffi/src/lib.rs oxigraph-wasi-ffi/Cargo.toml

oxigraph-wasm: $(OXIGRAPH_WASM_ZST)

$(OXIGRAPH_WASM_ZST): $(OXIGRAPH_SOURCES)
	cd oxigraph-wasi-ffi && cargo build --target wasm32-wasip1 --release
	zstd -f oxigraph-wasi-ffi/target/wasm32-wasip1/release/oxigraph_wasi_ffi.wasm -o $@
