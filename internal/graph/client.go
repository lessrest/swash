package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"swa.sh/pkg/oxigraph"
)

// Client is a client for the graph service.
type Client struct {
	socketPath string
	httpClient *http.Client
}

// NewClient creates a new client that connects to the graph service.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 30 * time.Second,
		},
	}
}

// SocketPath returns the socket path.
func (c *Client) SocketPath() string {
	return c.socketPath
}

// Health checks if the service is healthy.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://unix/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

// Stats returns service statistics.
func (c *Client) Stats(ctx context.Context) (StatsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://unix/stats", nil)
	if err != nil {
		return StatsResponse{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var stats StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return StatsResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return stats, nil
}

// StatsResponse is the response from the stats endpoint.
type StatsResponse struct {
	Quads int64 `json:"quads"`
}

// Query executes a SPARQL SELECT query and returns parsed solutions.
func (c *Client) Query(ctx context.Context, sparql string) ([]oxigraph.Solution, error) {
	data, err := c.QueryRaw(ctx, sparql)
	if err != nil {
		return nil, err
	}
	return oxigraph.ParseResults(data, oxigraph.ResultsJSON)
}

// QueryRaw executes a SPARQL query and returns the raw JSON response.
func (c *Client) QueryRaw(ctx context.Context, sparql string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unix/sparql", bytes.NewReader([]byte(sparql)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/sparql-query")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("query failed: %s", string(body))
	}

	return io.ReadAll(resp.Body)
}

// Quads returns quads matching a pattern as serialized RDF (TriG by default).
func (c *Client) Quads(ctx context.Context, pattern oxigraph.Pattern, format string) ([]byte, error) {
	u := url.URL{
		Scheme: "http",
		Host:   "unix",
		Path:   "/quads",
	}
	q := u.Query()
	if pattern.Subject != nil {
		q.Set("s", pattern.Subject.String())
	}
	if pattern.Predicate != nil {
		q.Set("p", pattern.Predicate.String())
	}
	if pattern.Object != nil {
		q.Set("o", pattern.Object.String())
	}
	if pattern.Graph != nil {
		q.Set("g", pattern.Graph.String())
	}
	if format != "" {
		q.Set("format", format)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("quads failed: %s", string(body))
	}

	return io.ReadAll(resp.Body)
}

// Load loads RDF data into the store.
func (c *Client) Load(ctx context.Context, data []byte, format oxigraph.Format) error {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unix/load", bytes.NewReader(data))
	if err != nil {
		return err
	}

	switch format {
	case oxigraph.NTriples:
		req.Header.Set("Content-Type", "application/n-triples")
	case oxigraph.Turtle:
		req.Header.Set("Content-Type", "text/turtle")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("load failed: %s", string(body))
	}
	return nil
}
