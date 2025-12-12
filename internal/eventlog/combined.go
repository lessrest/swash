package eventlog

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"os"
	"time"
)

// CombinedEventLog composes an EventSink and EventSource into a full EventLog.
// This allows mixing different sink/source implementations.
type CombinedEventLog struct {
	sink   EventSink
	source EventSource
}

var _ EventLog = (*CombinedEventLog)(nil)

// NewCombinedEventLog creates an EventLog from separate sink and source.
func NewCombinedEventLog(sink EventSink, source EventSource) *CombinedEventLog {
	return &CombinedEventLog{
		sink:   sink,
		source: source,
	}
}

// Write sends a structured entry via the sink.
func (c *CombinedEventLog) Write(message string, fields map[string]string) error {
	return c.sink.Write(message, fields)
}

// WriteSync sends a structured entry and waits until readable via the source.
func (c *CombinedEventLog) WriteSync(message string, fields map[string]string) error {
	// Generate a unique nonce to identify this specific write
	nonce := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	fieldsWithNonce := make(map[string]string, len(fields)+1)
	maps.Copy(fieldsWithNonce, fields)
	fieldsWithNonce["SWASH_WRITE_NONCE"] = nonce

	if err := c.sink.Write(message, fieldsWithNonce); err != nil {
		return err
	}

	return c.waitForNonce(nonce)
}

// waitForNonce polls the source until an entry with the given nonce is visible.
func (c *CombinedEventLog) waitForNonce(nonce string) error {
	deadline := time.Now().Add(5 * time.Second)
	filters := []EventFilter{{Field: "SWASH_WRITE_NONCE", Value: nonce}}

	for time.Now().Before(deadline) {
		entries, _, err := c.source.Poll(context.Background(), filters, "")
		if err == nil && len(entries) > 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for journal entry to be readable")
}

// Poll reads entries matching filters since cursor.
func (c *CombinedEventLog) Poll(ctx context.Context, filters []EventFilter, cursor string) ([]EventRecord, string, error) {
	return c.source.Poll(ctx, filters, cursor)
}

// Follow returns an iterator over entries matching filters.
func (c *CombinedEventLog) Follow(ctx context.Context, filters []EventFilter) iter.Seq[EventRecord] {
	return c.source.Follow(ctx, filters)
}

// Close releases resources from both sink and source.
func (c *CombinedEventLog) Close() error {
	var firstErr error
	if err := c.sink.Close(); err != nil {
		firstErr = err
	}
	if err := c.source.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Sink returns the underlying EventSink.
func (c *CombinedEventLog) Sink() EventSink {
	return c.sink
}

// Source returns the underlying EventSource.
func (c *CombinedEventLog) Source() EventSource {
	return c.source
}
