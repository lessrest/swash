use oxigraph::io::RdfFormat;
use oxigraph::sparql::{QueryResults, SparqlEvaluator};
use oxigraph::store::Store;
use std::cell::RefCell;
use std::ptr;

thread_local! {
    static STORE: RefCell<Option<Store>> = RefCell::new(None);
    static RESULT_BUF: RefCell<Vec<u8>> = RefCell::new(Vec::new());
}

/// Initialize a new in-memory store. Returns 0 on success, -1 on error.
#[no_mangle]
pub extern "C" fn store_init() -> i32 {
    STORE.with(|s| {
        match Store::new() {
            Ok(store) => {
                *s.borrow_mut() = Some(store);
                0
            }
            Err(_) => -1,
        }
    })
}

/// Load N-Triples data into the store.
/// data_ptr: pointer to UTF-8 string
/// data_len: length of string
/// Returns 0 on success, -1 on error.
#[no_mangle]
pub extern "C" fn store_load_ntriples(data_ptr: *const u8, data_len: usize) -> i32 {
    let data = unsafe {
        if data_ptr.is_null() {
            return -1;
        }
        std::slice::from_raw_parts(data_ptr, data_len)
    };

    STORE.with(|s| {
        let store_ref = s.borrow();
        match store_ref.as_ref() {
            Some(store) => {
                match store.load_from_slice(RdfFormat::NTriples, data) {
                    Ok(_) => 0,
                    Err(_) => -1,
                }
            }
            None => -1,
        }
    })
}

/// Execute a SPARQL query and store result as JSON.
/// query_ptr: pointer to UTF-8 query string
/// query_len: length of query string
/// Returns length of result on success, -1 on error.
/// Use store_get_result_ptr() to get pointer to result buffer.
#[no_mangle]
pub extern "C" fn store_query(query_ptr: *const u8, query_len: usize) -> i32 {
    let query = unsafe {
        if query_ptr.is_null() {
            return -1;
        }
        match std::str::from_utf8(std::slice::from_raw_parts(query_ptr, query_len)) {
            Ok(s) => s,
            Err(_) => return -1,
        }
    };

    STORE.with(|s| {
        let store_ref = s.borrow();
        match store_ref.as_ref() {
            Some(store) => {
                let parsed = match SparqlEvaluator::new().parse_query(query) {
                    Ok(q) => q,
                    Err(_) => return -1,
                };
                match parsed.on_store(store).execute() {
                    Ok(results) => {
                        let output = match results {
                            QueryResults::Solutions(solutions) => {
                                let mut output = String::new();
                                output.push('[');
                                let mut first = true;
                                for solution in solutions.flatten() {
                                    if !first {
                                        output.push(',');
                                    }
                                    first = false;
                                    output.push('{');
                                    let mut first_binding = true;
                                    for (var, term) in solution.iter() {
                                        if !first_binding {
                                            output.push(',');
                                        }
                                        first_binding = false;
                                        output.push('"');
                                        output.push_str(var.as_str());
                                        output.push_str("\":\"");
                                        output.push_str(&term.to_string().replace('"', "\\\""));
                                        output.push('"');
                                    }
                                    output.push('}');
                                }
                                output.push(']');
                                output
                            }
                            QueryResults::Boolean(b) => {
                                if b { "true" } else { "false" }.to_string()
                            }
                            QueryResults::Graph(triples) => {
                                let mut output = String::new();
                                for triple in triples.flatten() {
                                    output.push_str(&triple.to_string());
                                    output.push('\n');
                                }
                                output
                            }
                        };
                        RESULT_BUF.with(|buf| {
                            let mut buf = buf.borrow_mut();
                            buf.clear();
                            buf.extend_from_slice(output.as_bytes());
                            buf.len() as i32
                        })
                    }
                    Err(_) => -1,
                }
            }
            None => -1,
        }
    })
}

/// Get pointer to the result buffer (from last query).
#[no_mangle]
pub extern "C" fn store_get_result_ptr() -> *const u8 {
    RESULT_BUF.with(|buf| {
        let buf = buf.borrow();
        if buf.is_empty() {
            ptr::null()
        } else {
            buf.as_ptr()
        }
    })
}

/// Get the number of quads in the store.
#[no_mangle]
pub extern "C" fn store_len() -> i64 {
    STORE.with(|s| {
        let store_ref = s.borrow();
        match store_ref.as_ref() {
            Some(store) => store.len().unwrap_or(0) as i64,
            None => -1,
        }
    })
}

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
