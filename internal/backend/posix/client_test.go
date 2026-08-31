package posix

import (
	"context"
	"net/http"
	"testing"
)

func TestNewRequestPreservesQuery(t *testing.T) {
	c := &unixClient{}
	req, err := c.newRequest(context.Background(), http.MethodGet, "/tty/screen?format=ansi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/tty/screen" {
		t.Errorf("path = %q, want /tty/screen", req.URL.Path)
	}
	if got := req.URL.Query().Get("format"); got != "ansi" {
		t.Errorf("format query = %q, want ansi", got)
	}
}
