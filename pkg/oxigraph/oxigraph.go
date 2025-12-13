// Package oxigraph provides Go bindings for the oxigraph RDF store via WASM.
package oxigraph

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed oxigraph.wasm.zst
var oxigraphWasmZst []byte

var (
	ErrStoreClosed = errors.New("store is closed")
	ErrQueryFailed = errors.New("query failed")
)

// Cached runtime for parsing operations
var (
	parserRuntime     *Runtime
	parserRuntimeOnce sync.Once
	parserRuntimeErr  error
)

// ParseResults parses SPARQL query results from bytes into solutions.
// Uses a cached runtime internally - safe to call concurrently.
func ParseResults(data []byte, format ResultsFormat) ([]Solution, error) {
	parserRuntimeOnce.Do(func() {
		parserRuntime, parserRuntimeErr = NewRuntime(context.Background())
	})
	if parserRuntimeErr != nil {
		return nil, parserRuntimeErr
	}
	return parserRuntime.ParseResults(data, format)
}

// Runtime manages the WASM runtime and module instance.
type Runtime struct {
	ctx     context.Context
	runtime wazero.Runtime
	mod     api.Module
	mu      sync.Mutex

	// Exported functions
	storeNew          api.Function
	storeFree         api.Function
	storeLen          api.Function
	storeLoadNtriples api.Function
	storeLoadTurtle   api.Function
	queryStart        api.Function
	queryNext         api.Function
	queryResultPtr    api.Function
	queryFree         api.Function
	quadsStart        api.Function
	quadsNext         api.Function
	quadsResultPtr    api.Function
	quadsFree         api.Function
	quadsSerialize    api.Function
	serializeBufLen   api.Function
	serializeBufPtr   api.Function
	serializeBufFree  api.Function
	queryResults      api.Function
	bufferLen         api.Function
	bufferPtr         api.Function
	bufferFree        api.Function
	parseResults      api.Function
	alloc             api.Function
	dealloc           api.Function
}

// NewRuntime creates a new WASM runtime with the oxigraph module loaded.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	dec, err := zstd.NewReader(bytes.NewReader(oxigraphWasmZst))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer dec.Close()

	wasmBytes, err := io.ReadAll(dec)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress wasm: %w", err)
	}

	r := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	mod, err := r.Instantiate(ctx, wasmBytes)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate wasm: %w", err)
	}

	rt := &Runtime{
		ctx:               ctx,
		runtime:           r,
		mod:               mod,
		storeNew:          mod.ExportedFunction("store_new"),
		storeFree:         mod.ExportedFunction("store_free"),
		storeLen:          mod.ExportedFunction("store_len"),
		storeLoadNtriples: mod.ExportedFunction("store_load_ntriples"),
		storeLoadTurtle:   mod.ExportedFunction("store_load_turtle"),
		queryStart:        mod.ExportedFunction("query_start"),
		queryNext:         mod.ExportedFunction("query_next"),
		queryResultPtr:    mod.ExportedFunction("query_result_ptr"),
		queryFree:         mod.ExportedFunction("query_free"),
		quadsStart:        mod.ExportedFunction("quads_start"),
		quadsNext:         mod.ExportedFunction("quads_next"),
		quadsResultPtr:    mod.ExportedFunction("quads_result_ptr"),
		quadsFree:         mod.ExportedFunction("quads_free"),
		quadsSerialize:    mod.ExportedFunction("quads_serialize"),
		serializeBufLen:   mod.ExportedFunction("serialize_buf_len"),
		serializeBufPtr:   mod.ExportedFunction("serialize_buf_ptr"),
		serializeBufFree:  mod.ExportedFunction("serialize_buf_free"),
		queryResults:      mod.ExportedFunction("query_results"),
		bufferLen:         mod.ExportedFunction("buffer_len"),
		bufferPtr:         mod.ExportedFunction("buffer_ptr"),
		bufferFree:        mod.ExportedFunction("buffer_free"),
		parseResults:      mod.ExportedFunction("parse_results"),
		alloc:             mod.ExportedFunction("alloc"),
		dealloc:           mod.ExportedFunction("dealloc"),
	}

	return rt, nil
}

// Close releases all resources.
func (r *Runtime) Close() error {
	return r.runtime.Close(r.ctx)
}

// ParseResults parses SPARQL query results from bytes into solutions.
// This is useful for parsing responses from a SPARQL endpoint.
func (r *Runtime) ParseResults(data []byte, format ResultsFormat) ([]Solution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Allocate memory and write data
	res, err := r.alloc.Call(r.ctx, uint64(len(data)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(res[0])
	defer r.dealloc.Call(r.ctx, uint64(ptr), uint64(len(data)))

	if !r.mod.Memory().Write(ptr, data) {
		return nil, errors.New("failed to write to wasm memory")
	}

	// Call parse_results
	res, err = r.parseResults.Call(r.ctx, uint64(format), uint64(ptr), uint64(len(data)))
	if err != nil {
		return nil, err
	}
	iterHandle := int32(res[0])
	if iterHandle < 0 {
		return nil, errors.New("failed to parse results")
	}
	defer r.queryFree.Call(r.ctx, uint64(iterHandle))

	// Iterate and collect solutions
	var solutions []Solution
	for {
		res, err := r.queryNext.Call(r.ctx, uint64(iterHandle))
		if err != nil {
			return nil, err
		}
		length := int32(res[0])
		if length == 0 {
			break // Done
		}
		if length < 0 {
			return nil, errors.New("iteration error")
		}

		res, err = r.queryResultPtr.Call(r.ctx, uint64(iterHandle))
		if err != nil {
			return nil, err
		}
		resultPtr := uint32(res[0])

		resultData, ok := r.mod.Memory().Read(resultPtr, uint32(length))
		if !ok {
			return nil, errors.New("failed to read result from wasm memory")
		}

		sol, err := decodeSolution(resultData)
		if err != nil {
			return nil, err
		}
		solutions = append(solutions, sol)
	}

	return solutions, nil
}

// NewStore creates a new in-memory RDF store.
func (r *Runtime) NewStore() (*Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, err := r.storeNew.Call(r.ctx)
	if err != nil {
		return nil, err
	}
	handle := int32(res[0])
	if handle < 0 {
		return nil, errors.New("failed to create store")
	}

	return &Store{
		handle:  handle,
		runtime: r,
	}, nil
}

// Store is an in-memory RDF store backed by oxigraph WASM.
type Store struct {
	handle  int32
	runtime *Runtime
	closed  bool
}

// Close releases the store resources.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()

	_, err := s.runtime.storeFree.Call(s.runtime.ctx, uint64(s.handle))
	return err
}

// Len returns the number of quads in the store.
func (s *Store) Len() (int64, error) {
	if s.closed {
		return 0, ErrStoreClosed
	}

	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()

	res, err := s.runtime.storeLen.Call(s.runtime.ctx, uint64(s.handle))
	if err != nil {
		return 0, err
	}
	return int64(res[0]), nil
}

// Format represents an RDF serialization format.
type Format int

const (
	NTriples Format = iota
	Turtle
	NQuads
	TriG
)

// Output format constants (must match Rust FFI)
const (
	formatNQuads   = 0
	formatTriG     = 1
	formatNTriples = 2
	formatTurtle   = 3
)

// ResultsFormat represents a SPARQL results serialization format.
type ResultsFormat int

const (
	ResultsJSON ResultsFormat = 0 // application/sparql-results+json
	ResultsXML  ResultsFormat = 1 // application/sparql-results+xml
	ResultsCSV  ResultsFormat = 2 // text/csv
	ResultsTSV  ResultsFormat = 3 // text/tab-separated-values
)

// Load parses and inserts RDF data from a reader.
func (s *Store) Load(r io.Reader, format Format) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return s.LoadBytes(data, format)
}

// LoadString parses and inserts RDF data from a string.
func (s *Store) LoadString(data string, format Format) error {
	return s.LoadBytes([]byte(data), format)
}

// LoadBytes parses and inserts RDF data from bytes.
func (s *Store) LoadBytes(data []byte, format Format) error {
	if s.closed {
		return ErrStoreClosed
	}

	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()

	ptr, err := s.writeToWasm(data)
	if err != nil {
		return err
	}
	defer s.freeWasm(ptr, len(data))

	var loadFn api.Function
	switch format {
	case NTriples:
		loadFn = s.runtime.storeLoadNtriples
	case Turtle:
		loadFn = s.runtime.storeLoadTurtle
	default:
		return fmt.Errorf("unsupported format: %d", format)
	}

	res, err := loadFn.Call(s.runtime.ctx, uint64(s.handle), uint64(ptr), uint64(len(data)))
	if err != nil {
		return err
	}
	if int32(res[0]) != 0 {
		return errors.New("failed to load data")
	}
	return nil
}

func (s *Store) writeToWasm(data []byte) (uint32, error) {
	res, err := s.runtime.alloc.Call(s.runtime.ctx, uint64(len(data)))
	if err != nil {
		return 0, err
	}
	ptr := uint32(res[0])

	if !s.runtime.mod.Memory().Write(ptr, data) {
		return 0, errors.New("failed to write to wasm memory")
	}
	return ptr, nil
}

func (s *Store) freeWasm(ptr uint32, size int) {
	s.runtime.dealloc.Call(s.runtime.ctx, uint64(ptr), uint64(size))
}

// Query executes a SPARQL SELECT query and returns an iterator over solutions.
func (s *Store) Query(sparql string) iter.Seq2[Solution, error] {
	return func(yield func(Solution, error) bool) {
		if s.closed {
			yield(Solution{}, ErrStoreClosed)
			return
		}

		s.runtime.mu.Lock()
		ptr, err := s.writeToWasm([]byte(sparql))
		if err != nil {
			s.runtime.mu.Unlock()
			yield(Solution{}, err)
			return
		}

		res, err := s.runtime.queryStart.Call(s.runtime.ctx, uint64(s.handle), uint64(ptr), uint64(len(sparql)))
		s.freeWasm(ptr, len(sparql))
		if err != nil {
			s.runtime.mu.Unlock()
			yield(Solution{}, err)
			return
		}
		iterHandle := int32(res[0])
		if iterHandle < 0 {
			s.runtime.mu.Unlock()
			yield(Solution{}, ErrQueryFailed)
			return
		}
		s.runtime.mu.Unlock()

		defer func() {
			s.runtime.mu.Lock()
			s.runtime.queryFree.Call(s.runtime.ctx, uint64(iterHandle))
			s.runtime.mu.Unlock()
		}()

		for {
			s.runtime.mu.Lock()
			res, err := s.runtime.queryNext.Call(s.runtime.ctx, uint64(iterHandle))
			if err != nil {
				s.runtime.mu.Unlock()
				yield(Solution{}, err)
				return
			}
			length := int32(res[0])
			if length == 0 {
				s.runtime.mu.Unlock()
				return // Done
			}
			if length < 0 {
				s.runtime.mu.Unlock()
				yield(Solution{}, ErrQueryFailed)
				return
			}

			res, err = s.runtime.queryResultPtr.Call(s.runtime.ctx, uint64(iterHandle))
			if err != nil {
				s.runtime.mu.Unlock()
				yield(Solution{}, err)
				return
			}
			resultPtr := uint32(res[0])

			data, ok := s.runtime.mod.Memory().Read(resultPtr, uint32(length))
			s.runtime.mu.Unlock()
			if !ok {
				yield(Solution{}, errors.New("failed to read result from wasm memory"))
				return
			}

			sol, err := decodeSolution(data)
			if err != nil {
				yield(Solution{}, err)
				return
			}
			if !yield(sol, nil) {
				return
			}
		}
	}
}

// QueryResults executes a SPARQL query and returns results serialized in the given format.
// Supports SELECT and ASK queries. Returns the raw bytes of the serialized results.
func (s *Store) QueryResults(sparql string, format ResultsFormat) ([]byte, error) {
	if s.closed {
		return nil, ErrStoreClosed
	}

	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()

	ptr, err := s.writeToWasm([]byte(sparql))
	if err != nil {
		return nil, err
	}
	defer s.freeWasm(ptr, len(sparql))

	res, err := s.runtime.queryResults.Call(s.runtime.ctx,
		uint64(s.handle), uint64(ptr), uint64(len(sparql)), uint64(format))
	if err != nil {
		return nil, err
	}
	bufHandle := int32(res[0])
	if bufHandle < 0 {
		return nil, ErrQueryFailed
	}
	defer s.runtime.bufferFree.Call(s.runtime.ctx, uint64(bufHandle))

	// Get buffer length
	res, err = s.runtime.bufferLen.Call(s.runtime.ctx, uint64(bufHandle))
	if err != nil {
		return nil, err
	}
	length := int32(res[0])
	if length < 0 {
		return nil, errors.New("failed to get buffer length")
	}
	if length == 0 {
		return []byte{}, nil
	}

	// Get buffer pointer and read data
	res, err = s.runtime.bufferPtr.Call(s.runtime.ctx, uint64(bufHandle))
	if err != nil {
		return nil, err
	}
	bufPtr := uint32(res[0])

	data, ok := s.runtime.mod.Memory().Read(bufPtr, uint32(length))
	if !ok {
		return nil, errors.New("failed to read result data from wasm memory")
	}

	// Make a copy since the buffer will be freed
	result := make([]byte, len(data))
	copy(result, data)

	return result, nil
}

// Pattern specifies which quads to match. Nil means "any".
type Pattern struct {
	Subject   Term
	Predicate *NamedNode
	Object    Term
	Graph     Term
}

// Quads returns an iterator over quads matching the pattern.
// In-memory store iteration is infallible.
func (s *Store) Quads(p Pattern) iter.Seq[Quad] {
	return func(yield func(Quad) bool) {
		if s.closed {
			return
		}

		s.runtime.mu.Lock()

		var subjPtr, predPtr, objPtr, graphPtr uint32
		var subjData, predData, objData, graphData []byte

		if p.Subject != nil {
			subjData = []byte(p.Subject.String())
			var err error
			subjPtr, err = s.writeToWasm(subjData)
			if err != nil {
				s.runtime.mu.Unlock()
				return
			}
		}
		if p.Predicate != nil {
			predData = []byte(p.Predicate.String())
			var err error
			predPtr, err = s.writeToWasm(predData)
			if err != nil {
				if subjPtr != 0 {
					s.freeWasm(subjPtr, len(subjData))
				}
				s.runtime.mu.Unlock()
				return
			}
		}
		if p.Object != nil {
			objData = []byte(p.Object.String())
			var err error
			objPtr, err = s.writeToWasm(objData)
			if err != nil {
				if subjPtr != 0 {
					s.freeWasm(subjPtr, len(subjData))
				}
				if predPtr != 0 {
					s.freeWasm(predPtr, len(predData))
				}
				s.runtime.mu.Unlock()
				return
			}
		}
		if p.Graph != nil {
			graphData = []byte(p.Graph.String())
			var err error
			graphPtr, err = s.writeToWasm(graphData)
			if err != nil {
				if subjPtr != 0 {
					s.freeWasm(subjPtr, len(subjData))
				}
				if predPtr != 0 {
					s.freeWasm(predPtr, len(predData))
				}
				if objPtr != 0 {
					s.freeWasm(objPtr, len(objData))
				}
				s.runtime.mu.Unlock()
				return
			}
		}

		res, err := s.runtime.quadsStart.Call(s.runtime.ctx,
			uint64(s.handle),
			uint64(subjPtr), uint64(len(subjData)),
			uint64(predPtr), uint64(len(predData)),
			uint64(objPtr), uint64(len(objData)),
			uint64(graphPtr), uint64(len(graphData)),
		)

		if subjPtr != 0 {
			s.freeWasm(subjPtr, len(subjData))
		}
		if predPtr != 0 {
			s.freeWasm(predPtr, len(predData))
		}
		if objPtr != 0 {
			s.freeWasm(objPtr, len(objData))
		}
		if graphPtr != 0 {
			s.freeWasm(graphPtr, len(graphData))
		}

		if err != nil {
			s.runtime.mu.Unlock()
			return
		}
		iterHandle := int32(res[0])
		if iterHandle < 0 {
			s.runtime.mu.Unlock()
			return
		}
		s.runtime.mu.Unlock()

		defer func() {
			s.runtime.mu.Lock()
			s.runtime.quadsFree.Call(s.runtime.ctx, uint64(iterHandle))
			s.runtime.mu.Unlock()
		}()

		for {
			s.runtime.mu.Lock()
			res, err := s.runtime.quadsNext.Call(s.runtime.ctx, uint64(iterHandle))
			if err != nil {
				s.runtime.mu.Unlock()
				return
			}
			length := int32(res[0])
			if length == 0 {
				s.runtime.mu.Unlock()
				return // Done
			}

			res, err = s.runtime.quadsResultPtr.Call(s.runtime.ctx, uint64(iterHandle))
			if err != nil {
				s.runtime.mu.Unlock()
				return
			}
			resultPtr := uint32(res[0])

			data, ok := s.runtime.mod.Memory().Read(resultPtr, uint32(length))
			s.runtime.mu.Unlock()
			if !ok {
				return
			}

			quad, err := decodeQuad(data)
			if err != nil {
				return
			}
			if !yield(quad) {
				return
			}
		}
	}
}

// All returns an iterator over all quads.
func (s *Store) All() iter.Seq[Quad] {
	return s.Quads(Pattern{})
}

// Serialize returns quads matching the pattern serialized in the given format.
// Supported formats: NQuads, TriG, NTriples, Turtle.
func (s *Store) Serialize(p Pattern, format Format) ([]byte, error) {
	if s.closed {
		return nil, ErrStoreClosed
	}

	// Map Format to FFI format constant
	var ffiFormat int
	switch format {
	case NQuads:
		ffiFormat = formatNQuads
	case TriG:
		ffiFormat = formatTriG
	case NTriples:
		ffiFormat = formatNTriples
	case Turtle:
		ffiFormat = formatTurtle
	default:
		return nil, fmt.Errorf("unsupported serialization format: %d", format)
	}

	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()

	var subjPtr, predPtr, objPtr, graphPtr uint32
	var subjData, predData, objData, graphData []byte

	// Write pattern terms to WASM memory
	if p.Subject != nil {
		subjData = []byte(p.Subject.String())
		var err error
		subjPtr, err = s.writeToWasm(subjData)
		if err != nil {
			return nil, err
		}
		defer s.freeWasm(subjPtr, len(subjData))
	}
	if p.Predicate != nil {
		predData = []byte(p.Predicate.String())
		var err error
		predPtr, err = s.writeToWasm(predData)
		if err != nil {
			return nil, err
		}
		defer s.freeWasm(predPtr, len(predData))
	}
	if p.Object != nil {
		objData = []byte(p.Object.String())
		var err error
		objPtr, err = s.writeToWasm(objData)
		if err != nil {
			return nil, err
		}
		defer s.freeWasm(objPtr, len(objData))
	}
	if p.Graph != nil {
		graphData = []byte(p.Graph.String())
		var err error
		graphPtr, err = s.writeToWasm(graphData)
		if err != nil {
			return nil, err
		}
		defer s.freeWasm(graphPtr, len(graphData))
	}

	// Call quads_serialize
	res, err := s.runtime.quadsSerialize.Call(s.runtime.ctx,
		uint64(s.handle),
		uint64(subjPtr), uint64(len(subjData)),
		uint64(predPtr), uint64(len(predData)),
		uint64(objPtr), uint64(len(objData)),
		uint64(graphPtr), uint64(len(graphData)),
		uint64(ffiFormat),
	)
	if err != nil {
		return nil, err
	}
	bufHandle := int32(res[0])
	if bufHandle < 0 {
		return nil, errors.New("serialization failed")
	}
	defer s.runtime.serializeBufFree.Call(s.runtime.ctx, uint64(bufHandle))

	// Get buffer length
	res, err = s.runtime.serializeBufLen.Call(s.runtime.ctx, uint64(bufHandle))
	if err != nil {
		return nil, err
	}
	length := int32(res[0])
	if length < 0 {
		return nil, errors.New("failed to get buffer length")
	}
	if length == 0 {
		return []byte{}, nil
	}

	// Get buffer pointer and read data
	res, err = s.runtime.serializeBufPtr.Call(s.runtime.ctx, uint64(bufHandle))
	if err != nil {
		return nil, err
	}
	bufPtr := uint32(res[0])

	data, ok := s.runtime.mod.Memory().Read(bufPtr, uint32(length))
	if !ok {
		return nil, errors.New("failed to read serialized data from wasm memory")
	}

	// Make a copy since the buffer will be freed
	result := make([]byte, len(data))
	copy(result, data)

	return result, nil
}
