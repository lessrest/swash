package integration

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestGraph(t *testing.T) {
	e := getEnv(t)

	// Skip on posix - graph service needs systemd backend
	if e.mode == "posix" {
		t.Skip("graph tests require systemd backend")
	}

	t.Run("StatusNotRunning", func(t *testing.T) {
		stdout, _, _ := e.runSwash("graph", "status")
		if !strings.Contains(stdout, "not running") {
			t.Logf("status output: %s", stdout)
		}
	})

	t.Run("StartAndQuery", func(t *testing.T) {
		// Start graph service
		stdout, stderr, err := e.runSwash("graph", "start")
		if err != nil {
			t.Fatalf("graph start failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}
		t.Logf("start output: %s", stdout)

		// Give it time to initialize
		time.Sleep(500 * time.Millisecond)

		// Check status
		stdout, _, _ = e.runSwash("graph", "status")
		t.Logf("status: %s", stdout)

		// Load some test data
		rdfData := `<http://example.org/alice> <http://xmlns.com/foaf/0.1/name> "Alice" .
<http://example.org/bob> <http://xmlns.com/foaf/0.1/name> "Bob" .
`
		tmpFile := t.TempDir() + "/test.nt"
		os.WriteFile(tmpFile, []byte(rdfData), 0644)

		stdout, stderr, err = e.runSwash("graph", "load", "-f", "ntriples", tmpFile)
		if err != nil {
			t.Fatalf("graph load failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}
		t.Logf("load output: %s", stdout)

		// Query for names
		sparql := `SELECT ?name WHERE { ?s <http://xmlns.com/foaf/0.1/name> ?name }`
		stdout, stderr, err = e.runSwash("graph", "query", sparql)
		if err != nil {
			t.Fatalf("graph query failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}
		t.Logf("query output: %s", stdout)

		if !strings.Contains(stdout, "Alice") || !strings.Contains(stdout, "Bob") {
			t.Errorf("expected Alice and Bob in results, got: %s", stdout)
		}

		// Stop graph service
		stdout, _, _ = e.runSwash("graph", "stop")
		t.Logf("stop output: %s", stdout)
	})
}
