package streaming

import (
	"fmt"
	"net/http"
)

// Writer wraps an http.ResponseWriter for SSE streaming.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewWriter creates a new SSE writer.
// Returns nil if the response writer doesn't support flushing.
func NewWriter(w http.ResponseWriter) *Writer {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	return &Writer{
		w:       w,
		flusher: flusher,
	}
}

// WriteHeaders sets the required SSE headers.
func (s *Writer) WriteHeaders() {
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering
}

// WriteData writes a data line and flushes.
func (s *Writer) WriteData(data string) error {
	_, err := fmt.Fprintf(s.w, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteDone writes the [DONE] terminator.
func (s *Writer) WriteDone() error {
	return s.WriteData("[DONE]")
}

// WriteEvent writes a named event with data.
func (s *Writer) WriteEvent(event, data string) error {
	_, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteError writes an error as an SSE event.
func (s *Writer) WriteError(errMsg string) error {
	return s.WriteEvent("error", errMsg)
}

// Flush manually flushes the response.
func (s *Writer) Flush() {
	s.flusher.Flush()
}
