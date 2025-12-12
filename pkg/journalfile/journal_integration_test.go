//go:build linux

package journalfile

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	"golang.org/x/sys/unix"
)

// Stress-read the file with sdjournal while we append/sync to ensure readers
// never see transient ENODATA-style errors.
func TestReadWhileWritingIsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stress.journal")

	var machineID, bootID ID128
	if _, err := rand.Read(machineID[:]); err != nil {
		t.Fatalf("generate machine ID: %v", err)
	}
	if _, err := rand.Read(bootID[:]); err != nil {
		t.Fatalf("generate boot ID: %v", err)
	}

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	defer jf.Close()

	const writes = 200
	maxSeen := 0

	for i := range writes {
		fields := map[string]string{
			"MESSAGE":     fmt.Sprintf("entry-%d", i),
			"_BOOT_ID":    fmt.Sprintf("%x", bootID),
			"_MACHINE_ID": fmt.Sprintf("%x", machineID),
		}
		if err := jf.AppendEntry(fields); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if err := jf.Sync(); err != nil {
			t.Fatalf("sync after %d: %v", i, err)
		}
		if err := readJournalFile(path, &maxSeen); err != nil {
			t.Fatalf("read after %d: %v", i, err)
		}
	}

	if maxSeen < writes {
		t.Fatalf("expected to read %d entries, saw %d", writes, maxSeen)
	}
}

// Concurrent read test: while we append and sync, opening the journal should never fail with ENODATA.
func TestConcurrentOpenWhileWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.journal")

	var machineID, bootID ID128
	if _, err := rand.Read(machineID[:]); err != nil {
		t.Fatalf("generate machine ID: %v", err)
	}
	if _, err := rand.Read(bootID[:]); err != nil {
		t.Fatalf("generate boot ID: %v", err)
	}

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	defer jf.Close()

	const writes = 50
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range writes {
			fields := map[string]string{
				"MESSAGE":     fmt.Sprintf("entry-%d", i),
				"_BOOT_ID":    fmt.Sprintf("%x", bootID),
				"_MACHINE_ID": fmt.Sprintf("%x", machineID),
			}
			if err := jf.AppendEntry(fields); err != nil {
				errCh <- fmt.Errorf("append %d: %w", i, err)
				return
			}
			if err := jf.Sync(); err != nil {
				errCh <- fmt.Errorf("sync %d: %w", i, err)
				return
			}
		}
		close(done)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}

			if err := withLockedJournal(path, func(j *sdjournal.Journal) error {
				for {
					n, err := j.Next()
					if err != nil {
						return fmt.Errorf("sdjournal next: %w", err)
					}
					if n == 0 {
						break
					}
				}
				return nil
			}); err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "no data available") {
					dst := filepath.Join(os.TempDir(), fmt.Sprintf("fail-concurrent-%d.journal", time.Now().UnixNano()))
					if copyErr := copyFile(path, dst); copyErr == nil {
						errCh <- fmt.Errorf("sdjournal open ENODATA (copied to %s)", dst)
					} else {
						errCh <- fmt.Errorf("sdjournal open ENODATA (copy failed: %v)", copyErr)
					}
					return
				}
				errCh <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var testErr error
	select {
	case err := <-errCh:
		testErr = err
	case <-done:
	}

	wg.Wait()

	if testErr != nil {
		t.Fatalf("concurrent open failed: %v", testErr)
	}
}

func readJournalFile(path string, maxSeen *int) error {
	return withLockedJournal(path, func(j *sdjournal.Journal) error {
		seen := 0
		for {
			n, err := j.Next()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			seen++
		}
		if seen > *maxSeen {
			*maxSeen = seen
		}
		return nil
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// withLockedJournal acquires a shared flock so readers never race with writers.
// This mirrors journald's own file locking strategy around journal rotations.
func withLockedJournal(path string, fn func(j *sdjournal.Journal) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	j, err := sdjournal.NewJournalFromFiles(path)
	if err != nil {
		return err
	}
	defer j.Close()

	return fn(j)
}
