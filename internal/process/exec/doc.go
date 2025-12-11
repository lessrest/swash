// Package exec provides a future portable ProcessBackend implementation.
//
// Intended direction:
//   - Start workloads using plain OS processes (os/exec) rather than systemd units.
//   - Preserve the semantic ProcessBackend contract so higher layers (runtime/host)
//     can remain unchanged.
//
// This package is a placeholder; implementation is intentionally deferred.
package exec
