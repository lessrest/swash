// Package file provides a portable EventLog implementation backed by a journal file.
//
// This implementation uses pkg/journalfile for both reading and writing
// systemd-compatible *.journal files. The reader is pure Go with no CGO
// dependencies, making this package portable to macOS, BSD, and any platform
// where Go runs.
//
// The journal files are compatible with journalctl --file=...
package file
