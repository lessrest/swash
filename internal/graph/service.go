// Package graph provides an RDF knowledge graph service backed by oxigraph.
// It loads data from the event log and provides SPARQL and pattern-based queries.
package graph

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/mbrock/swash/internal/dirs"
	"github.com/mbrock/swash/pkg/oxigraph"
)

// Service is the graph service that manages an in-memory RDF store.
type Service struct {
	mu      sync.RWMutex
	runtime *oxigraph.Runtime
	store   *oxigraph.Store
	ctx     context.Context
	cancel  context.CancelFunc
}

// Config holds configuration for the graph service.
type Config struct {
	// SocketPath is the Unix socket path for the HTTP server.
	// Default: $XDG_RUNTIME_DIR/swash/graph.sock
	SocketPath string
}

// DefaultConfig returns the default service configuration.
func DefaultConfig() Config {
	return Config{
		SocketPath: filepath.Join(dirs.RuntimeDir(), "graph.sock"),
	}
}

// New creates a new graph service.
func New(ctx context.Context) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)

	runtime, err := oxigraph.NewRuntime(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create oxigraph runtime: %w", err)
	}

	store, err := runtime.NewStore()
	if err != nil {
		runtime.Close()
		cancel()
		return nil, fmt.Errorf("create oxigraph store: %w", err)
	}

	return &Service{
		runtime: runtime,
		store:   store,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// Close releases all resources.
func (s *Service) Close() error {
	s.cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		s.store.Close()
		s.store = nil
	}
	if s.runtime != nil {
		s.runtime.Close()
		s.runtime = nil
	}
	return nil
}

// Store returns the underlying oxigraph store for direct access.
// Callers should use RLock/RUnlock for read operations.
func (s *Service) Store() *oxigraph.Store {
	return s.store
}

// RLock acquires a read lock on the store.
func (s *Service) RLock() {
	s.mu.RLock()
}

// RUnlock releases the read lock.
func (s *Service) RUnlock() {
	s.mu.RUnlock()
}

// Lock acquires a write lock on the store.
func (s *Service) Lock() {
	s.mu.Lock()
}

// Unlock releases the write lock.
func (s *Service) Unlock() {
	s.mu.Unlock()
}

// Load loads RDF data into the store.
func (s *Service) Load(data []byte, format oxigraph.Format) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.LoadBytes(data, format)
}

// Len returns the number of quads in the store.
func (s *Service) Len() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.Len()
}

// Query executes a SPARQL SELECT query.
func (s *Service) Query(sparql string) ([]oxigraph.Solution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []oxigraph.Solution
	for sol, err := range s.store.Query(sparql) {
		if err != nil {
			return nil, err
		}
		results = append(results, sol)
	}
	return results, nil
}

// Quads returns quads matching the given pattern.
func (s *Service) Quads(pattern oxigraph.Pattern) []oxigraph.Quad {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []oxigraph.Quad
	for quad := range s.store.Quads(pattern) {
		results = append(results, quad)
	}
	return results
}

// Serialize returns quads matching the pattern serialized in the given format.
func (s *Service) Serialize(pattern oxigraph.Pattern, format oxigraph.Format) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.Serialize(pattern, format)
}
