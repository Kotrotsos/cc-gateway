package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// stream is a Server-Sent Events endpoint that pushes a compact notification for
// every exchange persisted after the connection opens, powering the live tail.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if s.bus == nil {
		http.Error(w, "live stream disabled", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, cancel := s.bus.Subscribe()
	defer cancel()

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}
