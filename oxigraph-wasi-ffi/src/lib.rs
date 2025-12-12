use oxigraph::io::RdfFormat;
use oxigraph::model::{NamedNodeRef, NamedOrBlankNode, Quad, Term};
use oxigraph::sparql::{QueryResults, QuerySolution, SparqlEvaluator};
use oxigraph::store::Store;
use std::sync::Mutex;

// Handle-based storage using simple Vec with Option slots (slab-like)
static STORES: Mutex<Vec<Option<Store>>> = Mutex::new(Vec::new());
static QUERY_ITERS: Mutex<Vec<Option<QueryIterator>>> = Mutex::new(Vec::new());
static QUAD_ITERS: Mutex<Vec<Option<QuadIterator>>> = Mutex::new(Vec::new());
static SERIALIZE_BUFS: Mutex<Vec<Option<Vec<u8>>>> = Mutex::new(Vec::new());

struct QueryIterator {
    solutions: Vec<QuerySolution>,
    index: usize,
    result_buf: Vec<u8>,
}

struct QuadIterator {
    quads: Vec<Quad>,
    index: usize,
    result_buf: Vec<u8>,
}

fn alloc_handle<T>(storage: &Mutex<Vec<Option<T>>>, item: T) -> i32 {
    let mut vec = storage.lock().unwrap();
    // Find an empty slot
    for (i, slot) in vec.iter_mut().enumerate() {
        if slot.is_none() {
            *slot = Some(item);
            return i as i32;
        }
    }
    // No empty slot, push new
    let handle = vec.len() as i32;
    vec.push(Some(item));
    handle
}

fn free_handle<T>(storage: &Mutex<Vec<Option<T>>>, handle: i32) {
    if handle < 0 {
        return;
    }
    let mut vec = storage.lock().unwrap();
    if let Some(slot) = vec.get_mut(handle as usize) {
        *slot = None;
    }
}

fn with_handle<T, R, F: FnOnce(&T) -> R>(
    storage: &Mutex<Vec<Option<T>>>,
    handle: i32,
    f: F,
) -> Option<R> {
    if handle < 0 {
        return None;
    }
    let vec = storage.lock().unwrap();
    vec.get(handle as usize)?.as_ref().map(f)
}

fn with_handle_mut<T, R, F: FnOnce(&mut T) -> R>(
    storage: &Mutex<Vec<Option<T>>>,
    handle: i32,
    f: F,
) -> Option<R> {
    if handle < 0 {
        return None;
    }
    let mut vec = storage.lock().unwrap();
    vec.get_mut(handle as usize)?.as_mut().map(f)
}

// Binary serialization for terms
fn write_varint(buf: &mut Vec<u8>, mut val: usize) {
    loop {
        let mut byte = (val & 0x7f) as u8;
        val >>= 7;
        if val != 0 {
            byte |= 0x80;
        }
        buf.push(byte);
        if val == 0 {
            break;
        }
    }
}

fn serialize_term(buf: &mut Vec<u8>, term: &Term) {
    match term {
        Term::NamedNode(nn) => {
            buf.push(0x01);
            let iri = nn.as_str().as_bytes();
            write_varint(buf, iri.len());
            buf.extend_from_slice(iri);
        }
        Term::BlankNode(bn) => {
            buf.push(0x02);
            let id = bn.as_str().as_bytes();
            write_varint(buf, id.len());
            buf.extend_from_slice(id);
        }
        Term::Literal(lit) => {
            if let Some(lang) = lit.language() {
                buf.push(0x04);
                let value = lit.value().as_bytes();
                write_varint(buf, value.len());
                buf.extend_from_slice(value);
                let lang_bytes = lang.as_bytes();
                write_varint(buf, lang_bytes.len());
                buf.extend_from_slice(lang_bytes);
            } else {
                let dt = lit.datatype();
                if dt.as_str() == "http://www.w3.org/2001/XMLSchema#string" {
                    buf.push(0x03);
                    let value = lit.value().as_bytes();
                    write_varint(buf, value.len());
                    buf.extend_from_slice(value);
                } else {
                    buf.push(0x05);
                    let value = lit.value().as_bytes();
                    write_varint(buf, value.len());
                    buf.extend_from_slice(value);
                    let dt_bytes = dt.as_str().as_bytes();
                    write_varint(buf, dt_bytes.len());
                    buf.extend_from_slice(dt_bytes);
                }
            }
        }

    }
}

fn serialize_solution(buf: &mut Vec<u8>, solution: &QuerySolution) {
    let bindings: Vec<_> = solution.iter().collect();
    write_varint(buf, bindings.len());
    for (var, term) in bindings {
        let var_name = var.as_str().as_bytes();
        write_varint(buf, var_name.len());
        buf.extend_from_slice(var_name);
        serialize_term(buf, term);
    }
}

fn serialize_quad(buf: &mut Vec<u8>, quad: &Quad) {
    // Subject (NamedNode or BlankNode)
    match &quad.subject {
        NamedOrBlankNode::NamedNode(nn) => {
            buf.push(0x01);
            let iri = nn.as_str().as_bytes();
            write_varint(buf, iri.len());
            buf.extend_from_slice(iri);
        }
        NamedOrBlankNode::BlankNode(bn) => {
            buf.push(0x02);
            let id = bn.as_str().as_bytes();
            write_varint(buf, id.len());
            buf.extend_from_slice(id);
        }
    }
    // Predicate (always NamedNode)
    buf.push(0x01);
    let pred_iri = quad.predicate.as_str().as_bytes();
    write_varint(buf, pred_iri.len());
    buf.extend_from_slice(pred_iri);
    // Object
    serialize_term(buf, &quad.object);
    // Graph (None = default graph)
    match &quad.graph_name {
        oxigraph::model::GraphName::NamedNode(nn) => {
            buf.push(0x01);
            let iri = nn.as_str().as_bytes();
            write_varint(buf, iri.len());
            buf.extend_from_slice(iri);
        }
        oxigraph::model::GraphName::BlankNode(bn) => {
            buf.push(0x02);
            let id = bn.as_str().as_bytes();
            write_varint(buf, id.len());
            buf.extend_from_slice(id);
        }
        oxigraph::model::GraphName::DefaultGraph => {
            buf.push(0x00); // Special tag for default graph
        }
    }
}

// ============================================================================
// Store Management FFI
// ============================================================================

/// Create a new in-memory store. Returns handle >= 0 on success, -1 on error.
#[no_mangle]
pub extern "C" fn store_new() -> i32 {
    match Store::new() {
        Ok(store) => alloc_handle(&STORES, store),
        Err(_) => -1,
    }
}

/// Free a store by handle.
#[no_mangle]
pub extern "C" fn store_free(handle: i32) {
    free_handle(&STORES, handle);
}

/// Get the number of quads in a store. Returns -1 on error.
#[no_mangle]
pub extern "C" fn store_len(handle: i32) -> i64 {
    with_handle(&STORES, handle, |store| store.len().unwrap_or(0) as i64).unwrap_or(-1)
}

/// Load N-Triples data into a store. Returns 0 on success, -1 on error.
#[no_mangle]
pub extern "C" fn store_load_ntriples(handle: i32, data_ptr: *const u8, data_len: usize) -> i32 {
    let data = unsafe {
        if data_ptr.is_null() {
            return -1;
        }
        std::slice::from_raw_parts(data_ptr, data_len)
    };
    with_handle(&STORES, handle, |store| {
        store.load_from_slice(RdfFormat::NTriples, data).is_ok()
    })
    .map(|ok| if ok { 0 } else { -1 })
    .unwrap_or(-1)
}

/// Load Turtle data into a store. Returns 0 on success, -1 on error.
#[no_mangle]
pub extern "C" fn store_load_turtle(handle: i32, data_ptr: *const u8, data_len: usize) -> i32 {
    let data = unsafe {
        if data_ptr.is_null() {
            return -1;
        }
        std::slice::from_raw_parts(data_ptr, data_len)
    };
    with_handle(&STORES, handle, |store| {
        store.load_from_slice(RdfFormat::Turtle, data).is_ok()
    })
    .map(|ok| if ok { 0 } else { -1 })
    .unwrap_or(-1)
}

// ============================================================================
// Query Iterator FFI
// ============================================================================

/// Start a SPARQL SELECT query. Returns iterator handle >= 0, or -1 on error.
#[no_mangle]
pub extern "C" fn query_start(handle: i32, query_ptr: *const u8, query_len: usize) -> i32 {
    let query = unsafe {
        if query_ptr.is_null() {
            return -1;
        }
        match std::str::from_utf8(std::slice::from_raw_parts(query_ptr, query_len)) {
            Ok(s) => s,
            Err(_) => return -1,
        }
    };

    with_handle(&STORES, handle, |store| {
        let parsed = match SparqlEvaluator::new().parse_query(query) {
            Ok(q) => q,
            Err(_) => return -1,
        };
        match parsed.on_store(store).execute() {
            Ok(QueryResults::Solutions(solutions)) => {
                let solutions_vec: Vec<_> = solutions.flatten().collect();
                alloc_handle(
                    &QUERY_ITERS,
                    QueryIterator {
                        solutions: solutions_vec,
                        index: 0,
                        result_buf: Vec::new(),
                    },
                )
            }
            _ => -1, // Only SELECT supported via this API
        }
    })
    .unwrap_or(-1)
}

/// Get next solution. Returns >0 (length) if data ready, 0 if done, -1 on error.
#[no_mangle]
pub extern "C" fn query_next(iter_handle: i32) -> i32 {
    with_handle_mut(&QUERY_ITERS, iter_handle, |iter| {
        if iter.index >= iter.solutions.len() {
            return 0; // Done
        }
        iter.result_buf.clear();
        serialize_solution(&mut iter.result_buf, &iter.solutions[iter.index]);
        iter.index += 1;
        iter.result_buf.len() as i32
    })
    .unwrap_or(-1)
}

/// Get pointer to result buffer for current solution.
#[no_mangle]
pub extern "C" fn query_result_ptr(iter_handle: i32) -> *const u8 {
    with_handle(&QUERY_ITERS, iter_handle, |iter| {
        if iter.result_buf.is_empty() {
            std::ptr::null()
        } else {
            iter.result_buf.as_ptr()
        }
    })
    .unwrap_or(std::ptr::null())
}

/// Free a query iterator.
#[no_mangle]
pub extern "C" fn query_free(iter_handle: i32) {
    free_handle(&QUERY_ITERS, iter_handle);
}

// ============================================================================
// Quad Iterator FFI
// ============================================================================

fn parse_term_from_ntriples(data: &[u8]) -> Option<Term> {
    let s = std::str::from_utf8(data).ok()?;
    // Simple N-Triples parsing
    if s.starts_with('<') && s.ends_with('>') {
        let iri = &s[1..s.len() - 1];
        Some(Term::from(oxigraph::model::NamedNode::new(iri).ok()?))
    } else if s.starts_with("_:") {
        let id = &s[2..];
        Some(Term::from(oxigraph::model::BlankNode::new(id).ok()?))
    } else if s.starts_with('"') {
        // Literal - simplified parsing
        if let Some(lang_start) = s.rfind("@") {
            let value = &s[1..lang_start - 1];
            let lang = &s[lang_start + 1..];
            Some(Term::from(
                oxigraph::model::Literal::new_language_tagged_literal(value, lang).ok()?,
            ))
        } else if let Some(dt_start) = s.rfind("^^<") {
            let value = &s[1..dt_start - 1];
            let dt = &s[dt_start + 3..s.len() - 1];
            Some(Term::from(oxigraph::model::Literal::new_typed_literal(
                value,
                NamedNodeRef::new(dt).ok()?,
            )))
        } else {
            let value = &s[1..s.len() - 1];
            Some(Term::from(oxigraph::model::Literal::new_simple_literal(
                value,
            )))
        }
    } else {
        None
    }
}

fn parse_subject_from_ntriples(data: &[u8]) -> Option<NamedOrBlankNode> {
    let s = std::str::from_utf8(data).ok()?;
    if s.starts_with('<') && s.ends_with('>') {
        let iri = &s[1..s.len() - 1];
        Some(NamedOrBlankNode::from(
            oxigraph::model::NamedNode::new(iri).ok()?,
        ))
    } else if s.starts_with("_:") {
        let id = &s[2..];
        Some(NamedOrBlankNode::from(
            oxigraph::model::BlankNode::new(id).ok()?,
        ))
    } else {
        None
    }
}

fn parse_named_node_from_ntriples(data: &[u8]) -> Option<oxigraph::model::NamedNode> {
    let s = std::str::from_utf8(data).ok()?;
    if s.starts_with('<') && s.ends_with('>') {
        let iri = &s[1..s.len() - 1];
        oxigraph::model::NamedNode::new(iri).ok()
    } else {
        None
    }
}

fn parse_graph_from_ntriples(data: &[u8]) -> Option<oxigraph::model::GraphName> {
    let s = std::str::from_utf8(data).ok()?;
    if s.starts_with('<') && s.ends_with('>') {
        let iri = &s[1..s.len() - 1];
        Some(oxigraph::model::GraphName::from(
            oxigraph::model::NamedNode::new(iri).ok()?,
        ))
    } else if s.starts_with("_:") {
        let id = &s[2..];
        Some(oxigraph::model::GraphName::from(
            oxigraph::model::BlankNode::new(id).ok()?,
        ))
    } else {
        None
    }
}

/// Start quad iteration with pattern. ptr=0 means "any" for that component.
/// Components are N-Triples serialized terms.
/// Returns iterator handle >= 0, or -1 on error.
#[no_mangle]
pub extern "C" fn quads_start(
    store_handle: i32,
    subj_ptr: *const u8,
    subj_len: usize,
    pred_ptr: *const u8,
    pred_len: usize,
    obj_ptr: *const u8,
    obj_len: usize,
    graph_ptr: *const u8,
    graph_len: usize,
) -> i32 {
    let subject = if subj_ptr.is_null() || subj_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(subj_ptr, subj_len) };
        parse_subject_from_ntriples(data)
    };

    let predicate = if pred_ptr.is_null() || pred_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(pred_ptr, pred_len) };
        parse_named_node_from_ntriples(data)
    };

    let object = if obj_ptr.is_null() || obj_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(obj_ptr, obj_len) };
        parse_term_from_ntriples(data)
    };

    let graph = if graph_ptr.is_null() || graph_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(graph_ptr, graph_len) };
        parse_graph_from_ntriples(data)
    };

    with_handle(&STORES, store_handle, |store| {
        let quads: Vec<Quad> = store
            .quads_for_pattern(
                subject.as_ref().map(|s| s.into()),
                predicate.as_ref().map(|p| p.into()),
                object.as_ref().map(|o| o.into()),
                graph.as_ref().map(|g| g.into()),
            )
            .flatten()
            .collect();
        alloc_handle(
            &QUAD_ITERS,
            QuadIterator {
                quads,
                index: 0,
                result_buf: Vec::new(),
            },
        )
    })
    .unwrap_or(-1)
}

/// Get next quad. Returns >0 (length) if data ready, 0 if done.
#[no_mangle]
pub extern "C" fn quads_next(iter_handle: i32) -> i32 {
    with_handle_mut(&QUAD_ITERS, iter_handle, |iter| {
        if iter.index >= iter.quads.len() {
            return 0; // Done
        }
        iter.result_buf.clear();
        serialize_quad(&mut iter.result_buf, &iter.quads[iter.index]);
        iter.index += 1;
        iter.result_buf.len() as i32
    })
    .unwrap_or(0)
}

/// Get pointer to result buffer for current quad.
#[no_mangle]
pub extern "C" fn quads_result_ptr(iter_handle: i32) -> *const u8 {
    with_handle(&QUAD_ITERS, iter_handle, |iter| {
        if iter.result_buf.is_empty() {
            std::ptr::null()
        } else {
            iter.result_buf.as_ptr()
        }
    })
    .unwrap_or(std::ptr::null())
}

/// Free a quad iterator.
#[no_mangle]
pub extern "C" fn quads_free(iter_handle: i32) {
    free_handle(&QUAD_ITERS, iter_handle);
}

// ============================================================================
// Serialization FFI
// ============================================================================

/// Format constants for serialization
pub const FORMAT_NQUADS: i32 = 0;
pub const FORMAT_TRIG: i32 = 1;
pub const FORMAT_NTRIPLES: i32 = 2;
pub const FORMAT_TURTLE: i32 = 3;

/// Serialize quads matching a pattern to RDF format.
/// Returns a buffer handle >= 0 on success, -1 on error.
/// format: 0 = N-Quads, 1 = TriG, 2 = N-Triples, 3 = Turtle
#[no_mangle]
pub extern "C" fn quads_serialize(
    store_handle: i32,
    subj_ptr: *const u8,
    subj_len: usize,
    pred_ptr: *const u8,
    pred_len: usize,
    obj_ptr: *const u8,
    obj_len: usize,
    graph_ptr: *const u8,
    graph_len: usize,
    format: i32,
) -> i32 {
    let subject = if subj_ptr.is_null() || subj_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(subj_ptr, subj_len) };
        parse_subject_from_ntriples(data)
    };

    let predicate = if pred_ptr.is_null() || pred_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(pred_ptr, pred_len) };
        parse_named_node_from_ntriples(data)
    };

    let object = if obj_ptr.is_null() || obj_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(obj_ptr, obj_len) };
        parse_term_from_ntriples(data)
    };

    let graph = if graph_ptr.is_null() || graph_len == 0 {
        None
    } else {
        let data = unsafe { std::slice::from_raw_parts(graph_ptr, graph_len) };
        parse_graph_from_ntriples(data)
    };

    let rdf_format = match format {
        FORMAT_NQUADS => RdfFormat::NQuads,
        FORMAT_TRIG => RdfFormat::TriG,
        FORMAT_NTRIPLES => RdfFormat::NTriples,
        FORMAT_TURTLE => RdfFormat::Turtle,
        _ => return -1,
    };

    with_handle(&STORES, store_handle, |store| {
        let quads: Vec<Quad> = store
            .quads_for_pattern(
                subject.as_ref().map(|s| s.into()),
                predicate.as_ref().map(|p| p.into()),
                object.as_ref().map(|o| o.into()),
                graph.as_ref().map(|g| g.into()),
            )
            .flatten()
            .collect();

        let mut buf = Vec::new();
        let mut serializer = oxigraph::io::RdfSerializer::from_format(rdf_format)
            .for_writer(&mut buf);
        
        for quad in &quads {
            if serializer.serialize_quad(quad.as_ref()).is_err() {
                return -1;
            }
        }
        
        if serializer.finish().is_err() {
            return -1;
        }

        alloc_handle(&SERIALIZE_BUFS, buf)
    })
    .unwrap_or(-1)
}

/// Get the length of a serialization buffer.
#[no_mangle]
pub extern "C" fn serialize_buf_len(handle: i32) -> i32 {
    with_handle(&SERIALIZE_BUFS, handle, |buf| buf.len() as i32).unwrap_or(-1)
}

/// Get pointer to a serialization buffer.
#[no_mangle]
pub extern "C" fn serialize_buf_ptr(handle: i32) -> *const u8 {
    with_handle(&SERIALIZE_BUFS, handle, |buf| {
        if buf.is_empty() {
            std::ptr::null()
        } else {
            buf.as_ptr()
        }
    })
    .unwrap_or(std::ptr::null())
}

/// Free a serialization buffer.
#[no_mangle]
pub extern "C" fn serialize_buf_free(handle: i32) {
    free_handle(&SERIALIZE_BUFS, handle);
}

// ============================================================================
// Memory Management FFI
// ============================================================================

/// Allocate memory for the host to write into.
#[no_mangle]
pub extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    std::mem::forget(buf);
    ptr
}

/// Free allocated memory.
#[no_mangle]
pub extern "C" fn dealloc(ptr: *mut u8, size: usize) {
    unsafe {
        drop(Vec::from_raw_parts(ptr, 0, size));
    }
}
