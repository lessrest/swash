// Package exec provides a portable ProcessBackend implementation.
//
// It starts workloads using plain OS processes (os/exec) rather than systemd units,
// preserving the semantic process.ProcessBackend contract so higher layers can
// remain unchanged.
package exec
