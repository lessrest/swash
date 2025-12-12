// oxigraph-poc demonstrates using oxigraph compiled to WASI via wazero.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"

	"github.com/klauspost/compress/zstd"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed oxigraph.wasm.zst
var oxigraphWasmZst []byte

func decompressWasm() []byte {
	dec, err := zstd.NewReader(bytes.NewReader(oxigraphWasmZst))
	if err != nil {
		log.Fatalf("failed to create zstd reader: %v", err)
	}
	defer dec.Close()
	data, err := io.ReadAll(dec)
	if err != nil {
		log.Fatalf("failed to decompress wasm: %v", err)
	}
	return data
}

func main() {
	ctx := context.Background()

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	mod, err := r.Instantiate(ctx, decompressWasm())
	if err != nil {
		log.Fatalf("failed to instantiate wasm: %v", err)
	}

	storeInit := mod.ExportedFunction("store_init")
	storeLoadNtriples := mod.ExportedFunction("store_load_ntriples")
	storeQuery := mod.ExportedFunction("store_query")
	storeGetResultPtr := mod.ExportedFunction("store_get_result_ptr")
	storeLen := mod.ExportedFunction("store_len")
	alloc := mod.ExportedFunction("alloc")
	dealloc := mod.ExportedFunction("dealloc")

	res, err := storeInit.Call(ctx)
	if err != nil {
		log.Fatalf("store_init failed: %v", err)
	}
	if int32(res[0]) != 0 {
		log.Fatal("store_init returned error")
	}
	fmt.Println("✓ Store initialized")

	ntriples := `<http://example.org/alice> <http://xmlns.com/foaf/0.1/name> "Alice" .
<http://example.org/alice> <http://xmlns.com/foaf/0.1/knows> <http://example.org/bob> .
<http://example.org/bob> <http://xmlns.com/foaf/0.1/name> "Bob" .
<http://example.org/bob> <http://xmlns.com/foaf/0.1/age> "30"^^<http://www.w3.org/2001/XMLSchema#integer> .
`

	if err := writeAndCall(ctx, mod, alloc, dealloc, storeLoadNtriples, []byte(ntriples)); err != nil {
		log.Fatalf("load failed: %v", err)
	}
	fmt.Println("✓ Loaded N-Triples data")

	res, err = storeLen.Call(ctx)
	if err != nil {
		log.Fatalf("store_len failed: %v", err)
	}
	fmt.Printf("✓ Store contains %d triples\n", int64(res[0]))

	query := `SELECT ?name WHERE { ?person <http://xmlns.com/foaf/0.1/name> ?name }`
	resultLen, err := queryAndGetLen(ctx, mod, alloc, dealloc, storeQuery, []byte(query))
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}

	res, err = storeGetResultPtr.Call(ctx)
	if err != nil {
		log.Fatalf("get_result_ptr failed: %v", err)
	}
	resultPtr := uint32(res[0])

	mem := mod.Memory()
	resultBytes, ok := mem.Read(resultPtr, uint32(resultLen))
	if !ok {
		log.Fatal("failed to read result from memory")
	}

	fmt.Println("✓ Query result:")
	fmt.Println(string(resultBytes))
}

func writeAndCall(ctx context.Context, mod api.Module, alloc, dealloc, fn api.Function, data []byte) error {
	mem := mod.Memory()

	res, err := alloc.Call(ctx, uint64(len(data)))
	if err != nil {
		return err
	}
	ptr := uint32(res[0])
	defer dealloc.Call(ctx, uint64(ptr), uint64(len(data)))

	if !mem.Write(ptr, data) {
		return fmt.Errorf("failed to write to memory")
	}

	res, err = fn.Call(ctx, uint64(ptr), uint64(len(data)))
	if err != nil {
		return err
	}
	if int32(res[0]) != 0 {
		return fmt.Errorf("function returned error code %d", int32(res[0]))
	}
	return nil
}

func queryAndGetLen(ctx context.Context, mod api.Module, alloc, dealloc, queryFn api.Function, query []byte) (int32, error) {
	mem := mod.Memory()

	res, err := alloc.Call(ctx, uint64(len(query)))
	if err != nil {
		return 0, err
	}
	ptr := uint32(res[0])
	defer dealloc.Call(ctx, uint64(ptr), uint64(len(query)))

	if !mem.Write(ptr, query) {
		return 0, fmt.Errorf("failed to write query to memory")
	}

	res, err = queryFn.Call(ctx, uint64(ptr), uint64(len(query)))
	if err != nil {
		return 0, err
	}
	resultLen := int32(res[0])
	if resultLen < 0 {
		return 0, fmt.Errorf("query returned error code %d", resultLen)
	}
	return resultLen, nil
}
