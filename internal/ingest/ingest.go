// Package ingest connects the proxy hot path to the store. The proxy submits a
// finished Event on a bounded channel (never blocking); a single writer
// goroutine parses it into structured rows and persists it. Everything here is
// best-effort and panic-isolated: if ingestion stalls or fails, forwarding is
// unaffected.
package ingest

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"cc-gateway/internal/parse"
	"cc-gateway/internal/store"
)

// Event is one finished request/response exchange handed off by the proxy.
type Event struct {
	ID          uint64
	Method      string
	Path        string
	URL         string
	ReqBody     []byte
	RespBody    []byte
	Truncated   bool
	Status      int
	StatusText  string
	ContentType string
	IsSSE       bool
	StartedAt   time.Time
	EndedAt     time.Time
	UserAgent   string
	Stats       *parse.LiveStats
	Error       string
}

// Written is the compact notification broadcast to live-stream subscribers after
// a successful persist.
type Written struct {
	Type       string  `json:"type"`
	SessionID  int64   `json:"session_id"`
	RequestID  int64   `json:"request_id"`
	Seq        int     `json:"seq"`
	SessionKey string  `json:"session_key"`
	TsStart    int64   `json:"ts_start"`
	DurationMs int64   `json:"duration_ms"`
	Status     int     `json:"status"`
	Model      string  `json:"model"`
	InTokens   int     `json:"in_tokens"`
	OutTokens  int     `json:"out_tokens"`
	NumTools   int     `json:"num_tools"`
	EstCost    float64 `json:"est_cost"`
	Error      string  `json:"error"`
}

// Sink owns the capture channel, the writer goroutine and the live broadcaster.
type Sink struct {
	store   *store.Store
	ch      chan *Event
	dropped uint64
	wg      sync.WaitGroup

	bus *Broadcaster
}

// NewSink creates a sink with a bounded buffer and starts its writer goroutine.
func NewSink(st *store.Store, buffer int) *Sink {
	if buffer <= 0 {
		buffer = 1024
	}
	s := &Sink{store: st, ch: make(chan *Event, buffer), bus: NewBroadcaster()}
	s.wg.Add(1)
	go s.run()
	return s
}

// Submit enqueues an event without ever blocking the caller. If the buffer is
// full the event is dropped and counted; the proxy keeps forwarding.
func (s *Sink) Submit(ev *Event) {
	select {
	case s.ch <- ev:
	default:
		atomic.AddUint64(&s.dropped, 1)
	}
}

// Dropped reports how many events were dropped due to a full buffer.
func (s *Sink) Dropped() uint64 { return atomic.LoadUint64(&s.dropped) }

// Bus returns the live-write broadcaster for the API to subscribe to.
func (s *Sink) Bus() *Broadcaster { return s.bus }

// Close stops accepting events and waits for the writer to drain.
func (s *Sink) Close() {
	close(s.ch)
	s.wg.Wait()
}

func (s *Sink) run() {
	defer s.wg.Done()
	for ev := range s.ch {
		s.handle(ev)
	}
}

// handle persists one event, recovering from any panic so a single malformed
// exchange can never take down ingestion.
func (s *Sink) handle(ev *Event) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ingest: recovered from panic: %v", r)
		}
	}()
	rec, w := build(ev)
	sessionID, requestID, seq, err := s.store.WriteExchange(rec)
	if err != nil {
		log.Printf("ingest: write failed: %v", err)
		return
	}
	w.SessionID = sessionID
	w.RequestID = requestID
	w.Seq = seq
	s.bus.Publish(w)
}
