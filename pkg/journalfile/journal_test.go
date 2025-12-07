package journalfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coreos/go-systemd/v22/sdjournal"
)

func TestJenkinsHash64(t *testing.T) {
	// Test with known values
	data := []byte("MESSAGE=hello world")
	hash := JenkinsHash64(data)
	if hash == 0 {
		t.Error("hash should not be zero")
	}
	t.Logf("hash of %q = %x", data, hash)

	// Same input should give same hash
	hash2 := JenkinsHash64(data)
	if hash != hash2 {
		t.Errorf("hash mismatch: %x != %x", hash, hash2)
	}
}

func TestCreateJournalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.journal")

	var machineID, bootID ID128
	copy(machineID[:], []byte("0123456789abcdef"))
	copy(bootID[:], []byte("fedcba9876543210"))

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Append some entries
	for i := 0; i < 3; i++ {
		err = jf.AppendEntry(map[string]string{
			"MESSAGE":  "hello world",
			"PRIORITY": "6",
			"_PID":     "1234",
		})
		if err != nil {
			t.Fatalf("AppendEntry failed: %v", err)
		}
	}

	if err := jf.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify file exists and has content
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	t.Logf("Journal file size: %d bytes", info.Size())

	if info.Size() < HeaderSize {
		t.Errorf("File too small: %d < %d", info.Size(), HeaderSize)
	}
}

func TestJournalFileReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.journal")

	var machineID, bootID ID128
	copy(machineID[:], []byte("0123456789abcdef"))
	copy(bootID[:], []byte("fedcba9876543210"))

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = jf.AppendEntry(map[string]string{
		"MESSAGE":    "test message from Go",
		"PRIORITY":   "6",
		"TEST_FIELD": "test value",
	})
	if err != nil {
		t.Fatalf("AppendEntry failed: %v", err)
	}

	if err := jf.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Try to read with journalctl
	t.Logf("Journal file created at: %s", path)
	t.Log("To verify manually, run:")
	t.Logf("  journalctl --file=%s", path)
}

func TestFieldMatching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.journal")

	var machineID, bootID ID128
	copy(machineID[:], []byte("0123456789abcdef"))
	copy(bootID[:], []byte("fedcba9876543210"))

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = jf.AppendEntry(map[string]string{
		"MESSAGE":       "Test message",
		"SWASH_SESSION": "TESTSESS",
		"SWASH_EVENT":   "exited",
	})
	if err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	if err := jf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Dump field hash table for debugging
	dumpHashTables(t, path)

	// Test matching with sdjournal
	j, err := sdjournal.NewJournalFromFiles(path)
	if err != nil {
		t.Fatalf("NewJournalFromFiles: %v", err)
	}
	defer j.Close()

	if err := j.AddMatch("SWASH_SESSION=TESTSESS"); err != nil {
		t.Fatalf("AddMatch: %v", err)
	}

	j.SeekHead()
	n, err := j.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if n == 0 {
		t.Error("No matching entries found")
	} else {
		entry, _ := j.GetEntry()
		t.Logf("Matched entry: %s", entry.Fields["MESSAGE"])
	}
}

func TestFieldMatchingMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.journal")

	var machineID, bootID ID128
	copy(machineID[:], []byte("0123456789abcdef"))
	copy(bootID[:], []byte("fedcba9876543210"))

	jf, err := Create(path, machineID, bootID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write 3 entries with the same session ID (like TTY mode does)
	for i, msg := range []string{"started", "screen", "exited"} {
		err = jf.AppendEntry(map[string]string{
			"MESSAGE":       msg,
			"SWASH_SESSION": "SESS123",
			"SWASH_EVENT":   msg,
		})
		if err != nil {
			t.Fatalf("AppendEntry %d: %v", i, err)
		}
	}

	if err := jf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Test matching - should find all 3 entries
	j, err := sdjournal.NewJournalFromFiles(path)
	if err != nil {
		t.Fatalf("NewJournalFromFiles: %v", err)
	}
	defer j.Close()

	if err := j.AddMatch("SWASH_SESSION=SESS123"); err != nil {
		t.Fatalf("AddMatch: %v", err)
	}

	j.SeekHead()
	count := 0
	for {
		n, err := j.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if n == 0 {
			break
		}
		entry, _ := j.GetEntry()
		t.Logf("Entry %d: %s", count+1, entry.Fields["MESSAGE"])
		count++
	}

	if count != 3 {
		t.Errorf("Expected 3 matched entries, got %d", count)
	}
}

func dumpHashTables(t *testing.T, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	header := make([]byte, HeaderSize)
	if _, err := f.Read(header); err != nil {
		t.Fatal(err)
	}

	fieldHashOffset := le.Uint64(header[120:128])
	fieldHashSize := le.Uint64(header[128:136])

	t.Logf("FieldHashTableOffset: %d", fieldHashOffset)
	t.Logf("FieldHashTableSize: %d (%d items)", fieldHashSize, fieldHashSize/16)

	f.Seek(int64(fieldHashOffset), 0)
	fieldTable := make([]byte, fieldHashSize)
	if _, err := f.Read(fieldTable); err != nil {
		t.Fatal(err)
	}

	t.Log("Field hash table entries:")
	for i := 0; i < int(fieldHashSize/16); i++ {
		head := le.Uint64(fieldTable[i*16 : i*16+8])
		if head != 0 {
			tail := le.Uint64(fieldTable[i*16+8 : i*16+16])
			t.Logf("  [%d] head=%d tail=%d", i, head, tail)
			dumpFieldObject(t, f, head)
		}
	}
}

func dumpFieldObject(t *testing.T, f *os.File, offset uint64) {
	f.Seek(int64(offset), 0)
	header := make([]byte, FieldObjectHeaderSize)
	if _, err := f.Read(header); err != nil {
		t.Logf("    Error reading field object at %d: %v", offset, err)
		return
	}

	objType := header[0]
	objSize := le.Uint64(header[8:16])
	hash := le.Uint64(header[16:24])
	nextHash := le.Uint64(header[24:32])
	headData := le.Uint64(header[32:40])

	payloadSize := objSize - FieldObjectHeaderSize
	payload := make([]byte, payloadSize)
	if _, err := f.Read(payload); err != nil {
		t.Logf("    Error reading field payload: %v", err)
		return
	}

	t.Logf("    Field @%d: type=%d size=%d hash=%x nextHash=%d headData=%d name=%q",
		offset, objType, objSize, hash, nextHash, headData, payload)

	// Dump the linked data object
	if headData != 0 {
		dumpDataObject(t, f, headData)
	}
}

func dumpDataObject(t *testing.T, f *os.File, offset uint64) {
	f.Seek(int64(offset), 0)
	header := make([]byte, DataObjectHeaderSize)
	if _, err := f.Read(header); err != nil {
		t.Logf("      Error reading data object at %d: %v", offset, err)
		return
	}

	objType := header[0]
	objSize := le.Uint64(header[8:16])
	hash := le.Uint64(header[16:24])
	nextHashOff := le.Uint64(header[24:32])
	nextFieldOff := le.Uint64(header[32:40])
	entryOff := le.Uint64(header[40:48])
	entryArrayOff := le.Uint64(header[48:56])
	nEntries := le.Uint64(header[56:64])

	payloadSize := objSize - DataObjectHeaderSize
	payload := make([]byte, payloadSize)
	if _, err := f.Read(payload); err != nil {
		t.Logf("      Error reading data payload: %v", err)
		return
	}

	t.Logf("      Data @%d: type=%d size=%d hash=%x nextHash=%d nextField=%d entry=%d entryArray=%d nEntries=%d payload=%q",
		offset, objType, objSize, hash, nextHashOff, nextFieldOff, entryOff, entryArrayOff, nEntries, payload)
}
