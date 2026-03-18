// Package sse provides a minimal Server-Sent Events writer.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Writer wraps an http.ResponseWriter for SSE output.
type Writer struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// New sets SSE headers and returns a Writer.
func New(w http.ResponseWriter) *Writer {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc.Flush()
	return &Writer{w: w, rc: rc}
}

// Send emits a named event with a plain-text data payload.
// Multi-line data is split so each line gets its own "data:" field per the SSE spec.
func (s *Writer) Send(event, data string) {
	fmt.Fprintf(s.w, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(s.w, "data: %s\n", line)
	}
	fmt.Fprint(s.w, "\n")
	s.rc.Flush()
}

// SendJSON emits a named event with a JSON-encoded payload.
func (s *Writer) SendJSON(event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, string(b))
	s.rc.Flush()
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
