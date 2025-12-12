# Oxigraph Go Bindings Design

## Goals

- Idiomatic Go API (not a 1:1 mapping of Rust)
- Multiple store instances (not thread-local singletons)
- Go 1.23 `iter.Seq` for query results
- Efficient memory handling across WASM boundary
- Type-safe RDF term representation

## Core Types

### RDF Terms

```go
// Term represents an RDF term (IRI, blank node, or literal)
type Term interface {
    TermType() TermType
    Value() string
    String() string  // N-Triples serialization
}

type TermType int

const (
    TermNamedNode TermType = iota
    TermBlankNode
    TermLiteral
)

type NamedNode struct {
    IRI string
}

type BlankNode struct {
    ID string
}

type Literal struct {
    Value    string
    Language string   // empty if typed
    Datatype string   // IRI, defaults to xsd:string
}

// Quad represents an RDF quad (triple + optional graph)
type Quad struct {
    Subject   Term
    Predicate NamedNode
    Object    Term
    Graph     Term  // nil = default graph
}
```

### Store

```go
// Store is an in-memory RDF store backed by oxigraph WASM
type Store struct {
    handle uint32  // opaque handle into WASM memory
    mod    api.Module
}

// New creates a new in-memory store
func New(ctx context.Context) (*Store, error)

// Close releases the store resources
func (s *Store) Close() error

// Len returns the number of quads
func (s *Store) Len() (int64, error)
```

### Loading Data

```go
// Load parses and inserts RDF data
func (s *Store) Load(r io.Reader, format Format) error

// LoadString is a convenience for loading from a string
func (s *Store) LoadString(data string, format Format) error

type Format int

const (
    NTriples Format = iota
    Turtle
    RDFXml
    NQuads
    TriG
)

// Insert adds a single quad
func (s *Store) Insert(q Quad) error

// Delete removes a single quad  
func (s *Store) Delete(q Quad) error
```

### Querying

```go
// QueryResults represents SPARQL query results
type QueryResults struct {
    Type      ResultType
    Variables []string  // for SELECT
}

type ResultType int

const (
    ResultSolutions ResultType = iota
    ResultBoolean
    ResultGraph
)

// Solutions returns an iterator over query solutions (SELECT queries)
// Uses Go 1.23 iter.Seq for lazy evaluation
func (r *QueryResults) Solutions() iter.Seq2[Solution, error]

// Boolean returns the boolean result (ASK queries)
func (r *QueryResults) Boolean() (bool, error)

// Triples returns an iterator over triples (CONSTRUCT/DESCRIBE queries)
func (r *QueryResults) Triples() iter.Seq2[Triple, error]

// Solution is a single row of bindings
type Solution struct {
    bindings map[string]Term
}

func (s Solution) Get(variable string) (Term, bool)
func (s Solution) Variables() []string

// Query executes a SPARQL query
func (s *Store) Query(ctx context.Context, sparql string) (*QueryResults, error)
```

### Example Usage

```go
ctx := context.Background()

// Create store
store, err := oxigraph.New(ctx)
if err != nil {
    log.Fatal(err)
}
defer store.Close()

// Load data
err = store.LoadString(`
    <http://example.org/alice> <http://xmlns.com/foaf/0.1/name> "Alice" .
    <http://example.org/alice> <http://xmlns.com/foaf/0.1/knows> <http://example.org/bob> .
`, oxigraph.NTriples)

// Query with iterator
results, err := store.Query(ctx, `SELECT ?name WHERE { ?person foaf:name ?name }`)
if err != nil {
    log.Fatal(err)
}

for solution, err := range results.Solutions() {
    if err != nil {
        log.Fatal(err)
    }
    if name, ok := solution.Get("name"); ok {
        fmt.Println(name.Value())
    }
}
```

## WASM FFI Design

### Handle-based API

Instead of thread-local singletons, use opaque handles:

```rust
// Rust side
static STORES: Mutex<Slab<Store>> = ...;

#[no_mangle]
pub extern "C" fn store_new() -> i32  // returns handle or -1

#[no_mangle]
pub extern "C" fn store_free(handle: i32)

#[no_mangle]
pub extern "C" fn store_query(handle: i32, query_ptr: *const u8, query_len: usize) -> i32
```

### Result Streaming

For large result sets, stream results rather than buffering all in WASM memory:

```rust
// Start query, returns iterator handle
#[no_mangle]
pub extern "C" fn query_start(store: i32, query_ptr: *const u8, query_len: usize) -> i32

// Get next solution, returns 0 if done, -1 on error, >0 = result length
#[no_mangle]
pub extern "C" fn query_next(iter: i32) -> i32

// Get result buffer pointer
#[no_mangle]
pub extern "C" fn query_result_ptr(iter: i32) -> *const u8

// Free iterator
#[no_mangle]
pub extern "C" fn query_free(iter: i32)
```

### Serialization Format

Use a compact binary format for crossing the WASM boundary:

```
Solution = varint(num_bindings) (Binding)*
Binding = varint(var_name_len) var_name Term
Term = tag:u8 payload

tag 0x01 = NamedNode: varint(len) iri_bytes
tag 0x02 = BlankNode: varint(len) id_bytes  
tag 0x03 = SimpleLiteral: varint(len) value_bytes
tag 0x04 = LangLiteral: varint(value_len) value varint(lang_len) lang
tag 0x05 = TypedLiteral: varint(value_len) value varint(dt_len) datatype_iri
```

This avoids JSON parsing overhead for tight loops.

## Decisions

1. **Single WASM instance, multiple stores**: One loaded module, stores identified by handles.
   Good for test isolation without spinning up separate runtimes.

2. **Skip for now**: Compilation caching, transactions.

3. **SPARQL Update**: Yes, support `INSERT DATA`, `DELETE DATA`, etc.

## Pattern Matching

Expose `Store::quads_for_pattern` as an iterator:

```go
// Pattern components - nil means "any"
type Pattern struct {
    Subject   Term      // nil = any
    Predicate *NamedNode // nil = any  
    Object    Term      // nil = any
    Graph     Term      // nil = any (not default graph!)
}

// Quads returns an iterator over quads matching the pattern
// Note: in-memory store iteration is infallible
func (s *Store) Quads(p Pattern) iter.Seq[Quad]

// All returns an iterator over all quads
func (s *Store) All() iter.Seq[Quad]
```

### Example

```go
// Find all triples where Alice is the subject
for quad := range store.Quads(oxigraph.Pattern{
    Subject: oxigraph.IRI("http://example.org/alice"),
}) {
    fmt.Printf("%s %s %s\n", quad.Subject, quad.Predicate, quad.Object)
}
```

## FFI for Pattern Matching

```rust
// Start quad iteration with pattern
// Each component: ptr=0 means "any", otherwise ptr+len is the N-Triples serialization
#[no_mangle]
pub extern "C" fn quads_start(
    store: i32,
    subj_ptr: *const u8, subj_len: usize,
    pred_ptr: *const u8, pred_len: usize,
    obj_ptr: *const u8, obj_len: usize,
    graph_ptr: *const u8, graph_len: usize,
) -> i32  // returns iterator handle

#[no_mangle]
pub extern "C" fn quads_next(iter: i32) -> i32  // 0=done, >0=quad length (infallible for in-memory)

#[no_mangle]  
pub extern "C" fn quads_result_ptr(iter: i32) -> *const u8

#[no_mangle]
pub extern "C" fn quads_free(iter: i32)
```
