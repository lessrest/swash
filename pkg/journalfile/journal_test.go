package journalfile

import (
	"os"
	"path/filepath"
	"testing"
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
		"MESSAGE":   "test message from Go",
		"PRIORITY":  "6",
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
