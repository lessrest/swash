# oxigraph-wasi-ffi

WASI FFI wrapper for [oxigraph](https://github.com/oxigraph/oxigraph)'s in-memory RDF store.

This builds oxigraph as a WASI module that can be embedded in Go via [wazero](https://wazero.io/).

## Building

```bash
cargo build --target wasm32-wasip1 --release
```

Output: `target/wasm32-wasip1/release/oxigraph_wasi_ffi.wasm` (~3.2MB)

## Exported Functions

| Function | Signature | Description |
|----------|-----------|-------------|
| `store_init` | `() -> i32` | Initialize store (0=ok, -1=error) |
| `store_load_ntriples` | `(ptr, len) -> i32` | Load N-Triples data |
| `store_query` | `(ptr, len) -> i32` | Execute SPARQL, returns result length |
| `store_get_result_ptr` | `() -> ptr` | Get pointer to query result buffer |
| `store_len` | `() -> i64` | Get number of quads in store |
| `alloc` | `(size) -> ptr` | Allocate memory |
| `dealloc` | `(ptr, size)` | Free memory |

## Usage from Go

See `cmd/oxigraph-poc/main.go` for a complete example.
