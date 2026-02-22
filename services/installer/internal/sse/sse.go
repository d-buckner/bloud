package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Writer wraps an http.ResponseWriter and sends Server-Sent Events.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewWriter sets SSE response headers and returns a Writer.
// Returns an error if the underlying ResponseWriter does not support flushing.
func NewWriter(w http.ResponseWriter) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return &Writer{w: w, flusher: flusher}, nil
}

// Send marshals data to JSON and writes a single SSE event.
func (sw *Writer) Send(data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling SSE data: %w", err)
	}
	fmt.Fprintf(sw.w, "data: %s\n\n", b)
	sw.flusher.Flush()
	return nil
}
