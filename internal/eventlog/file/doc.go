// Package file provides a portable EventLog implementation backed by a journal file.
//
// This implementation uses:
//   - pkg/journalfile to write a systemd-compatible *.journal file
//   - go-systemd/sdjournal to read/follow entries from that file
//
// This keeps swash's EventLog interface stable and lets non-systemd platforms
// persist events without a running journald daemon (reads operate on the file).
package file
