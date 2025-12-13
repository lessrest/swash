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
  # Linux: use posix backend (isolated) with journalctl
  SWASH_TEST_MODE ?= posix
  SWASH_TEST_JOURNAL_READER ?= journalctl
endif

export SWASH_TEST_MODE
export SWASH_TEST_JOURNAL_READER

.PHONY: all build test test-unit test-integration test-all-backends install clean generate oxigraph-wasm coverage

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

test-all-backends: build
	@echo "=== Testing with posix backend ==="
	SWASH_TEST_MODE=posix go test ./integration/... -v -timeout 120s
	@echo ""
	@echo "=== Testing with real systemd backend ==="
	SWASH_TEST_MODE=real go test ./integration/... -v -timeout 120s
	@echo ""
	@echo "=== All backends passed! ==="

clean:
	rm -rf bin/ coverage/

# Coverage report from unit + integration tests (runs all backends)
COVERAGE_DIR := $(CURDIR)/coverage
coverage: generate
	@rm -rf $(COVERAGE_DIR)
	@mkdir -p $(COVERAGE_DIR)/unit $(COVERAGE_DIR)/integration $(COVERAGE_DIR)/merged
	go build -cover -o bin/swash ./cmd/swash/
	@echo "=== Coverage: unit tests ==="
	GOCOVERDIR=$(COVERAGE_DIR)/unit go test ./pkg/... ./internal/... -cover -timeout 120s
	@echo "=== Coverage: integration (posix) ==="
	GOCOVERDIR=$(COVERAGE_DIR)/integration SWASH_TEST_MODE=posix go test ./integration/... -timeout 120s
	@echo "=== Coverage: integration (real systemd) ==="
	GOCOVERDIR=$(COVERAGE_DIR)/integration SWASH_TEST_MODE=real go test ./integration/... -timeout 120s
	@echo "=== Merging coverage ==="
	go tool covdata merge -i=$(COVERAGE_DIR)/unit,$(COVERAGE_DIR)/integration -o=$(COVERAGE_DIR)/merged -pcombine
	go tool covdata textfmt -i=$(COVERAGE_DIR)/merged -o=$(COVERAGE_DIR)/coverage.out
	go tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@go tool cover -func=$(COVERAGE_DIR)/coverage.out | tail -1
	@echo "HTML report: $(COVERAGE_DIR)/coverage.html"
	@echo ""
	@$(MAKE) --no-print-directory coverage-report

# Show per-file coverage percentages (requires running 'make coverage' first)
coverage-report:
	@if [ ! -f $(COVERAGE_DIR)/coverage.html ]; then echo "Run 'make coverage' first"; exit 1; fi
	@grep -oP 'option value="file\d+"[^>]*>\K[^<]+' $(COVERAGE_DIR)/coverage.html | sed 's/(\(.*\))/\1/' | awk '{pct=$$NF; $$NF=""; printf "%7s  %s\n", pct, $$0}' | sort -rn

# Build oxigraph WASI module (compressed blob is embedded in pkg/oxigraph)
OXIGRAPH_WASM_ZST := pkg/oxigraph/oxigraph.wasm.zst
OXIGRAPH_SOURCES := oxigraph-wasi-ffi/src/lib.rs oxigraph-wasi-ffi/Cargo.toml

oxigraph-wasm: $(OXIGRAPH_WASM_ZST)

$(OXIGRAPH_WASM_ZST): $(OXIGRAPH_SOURCES)
	cd oxigraph-wasi-ffi && cargo build --target wasm32-wasip1 --release
	zstd -f oxigraph-wasi-ffi/target/wasm32-wasip1/release/oxigraph_wasi_ffi.wasm -o $@
