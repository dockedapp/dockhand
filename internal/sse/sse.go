// Package sse provides a minimal Server-Sent Events writer.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Writer wraps an http.ResponseWriter for SSE output.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// New sets SSE headers and returns a Writer. Reports false if the client
// does not support streaming.
func New(w http.ResponseWriter) (*Writer, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &Writer{w: w, flusher: flusher}, true
}

// Send emits a named event with a plain-text data payload.
func (s *Writer) Send(event, data string) {
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	s.flusher.Flush()
}

// SendJSON emits a named event with a JSON-encoded payload.
func (s *Writer) SendJSON(event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, string(b))
	s.flusher.Flush()
}

// Line emits a "log" event with a single line of text.
func (s *Writer) Line(line string) {
	s.Send("log", line)
}

// Error emits an "error" event.
func (s *Writer) Error(msg string) {
	s.Send("error", msg)
}

// Done emits a "done" event with an exit code payload.
func (s *Writer) Done(exitCode int) {
	s.SendJSON("done", map[string]int{"exitCode": exitCode})
}
