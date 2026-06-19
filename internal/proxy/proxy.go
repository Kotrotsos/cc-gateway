// Package proxy is the transparent forwarding core: it replays each Claude Code
// request to the upstream Anthropic API byte-for-byte and streams the response
// straight back, tapping the bytes read-only for the terminal summary and the
// (best-effort, off-hot-path) capture sink. Nothing on the wire is altered.
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"cc-gateway/internal/ingest"
	"cc-gateway/internal/parse"
	"cc-gateway/internal/term"
)

// maxCapture bounds how many response bytes we retain for storage/screen so a
// huge (e.g. base64 image) body cannot blow up memory. Beyond this we keep the
// stats but mark the stored body truncated.
const maxCapture = 8 << 20 // 8 MB

// hop-by-hop headers are connection-specific and must not be forwarded.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Handler forwards traffic. sink may be nil (storage disabled), in which case
// only the terminal view is driven.
type Handler struct {
	upstream *url.URL
	client   *http.Client
	con      *term.Console
	sink     *ingest.Sink

	counter uint64
}

// New builds a forwarding handler.
func New(upstream *url.URL, client *http.Client, con *term.Console, sink *ingest.Sink) *Handler {
	return &Handler{upstream: upstream, client: client, con: con, sink: sink}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := atomic.AddUint64(&h.counter, 1)
	start := time.Now()

	// Buffer the request body so we can summarize it and still forward it.
	// Claude Code request bodies are complete JSON documents, not streams.
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	h.con.LogRequest(id, r.Method, r.URL.RequestURI(), body)
	h.con.RecordRequest(id, r.Method, r.URL.RequestURI(), body)

	// Build the upstream request: same method, path, query, headers, body.
	target := *h.upstream
	target.Path = singleJoin(h.upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		h.con.LogError(id, "build request", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	stripHopHeaders(outReq.Header)
	outReq.Host = h.upstream.Host

	resp, err := h.client.Do(outReq)
	if err != nil {
		h.con.LogError(id, "upstream", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		h.submit(id, r, body, nil, false, false, 502, "", "", start, time.Now(), nil, err)
		return
	}
	defer resp.Body.Close()

	// Mirror response headers + status to the client.
	copyHeaders(w.Header(), resp.Header)
	stripHopHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	ct := resp.Header.Get("Content-Type")
	enc := resp.Header.Get("Content-Encoding")
	isSSE := strings.Contains(ct, "text/event-stream") && enc == ""

	var stats *parse.LiveStats
	if isSSE {
		stats = parse.NewLiveStats()
	}

	// We always need the response body when either the terminal view wants it or
	// a storage sink is attached. The toggle decision is made once, up front: a
	// toggle flipped mid-stream takes effect on the next request.
	capture := h.con.WantsBody() || h.sink != nil
	var rawBody bytes.Buffer
	truncated := false

	// Stream the response straight through, flushing each chunk so Claude Code
	// sees tokens arrive in real time. We tap the bytes read-only.
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			w.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
			if stats != nil {
				stats.Feed(chunk)
			}
			if capture {
				if rawBody.Len() < maxCapture {
					rawBody.Write(chunk)
				} else {
					truncated = true
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	end := time.Now()

	h.con.LogResponse(id, resp.Status, resp.StatusCode, ct, isSSE, stats, end.Sub(start))
	if capture {
		h.con.RecordResponse(id, r.Method, r.URL.RequestURI(), resp.Status, resp.StatusCode, rawBody.Bytes(), isSSE, end.Sub(start))
	}

	var respBytes []byte
	if rawBody.Len() > 0 {
		respBytes = make([]byte, rawBody.Len())
		copy(respBytes, rawBody.Bytes())
	}
	h.submit(id, r, body, respBytes, truncated, isSSE, resp.StatusCode, resp.Status, ct, start, end, stats, nil)
}

// submit hands a finished exchange to the capture sink, if any. It is fully
// best-effort: a nil sink, a full queue, or a panic here never affects the
// forwarding that already completed above.
func (h *Handler) submit(id uint64, r *http.Request, reqBody, respBody []byte, truncated, isSSE bool, status int, statusText, ct string, start, end time.Time, stats *parse.LiveStats, ferr error) {
	if h.sink == nil {
		return
	}
	defer func() { recover() }()
	ev := &ingest.Event{
		ID:          id,
		Method:      r.Method,
		Path:        r.URL.Path,
		URL:         r.URL.RequestURI(),
		ReqBody:     reqBody,
		RespBody:    respBody,
		Truncated:   truncated,
		Status:      status,
		StatusText:  statusText,
		ContentType: ct,
		IsSSE:       isSSE,
		StartedAt:   start,
		EndedAt:     end,
		UserAgent:   r.Header.Get("User-Agent"),
		Stats:       stats,
	}
	if ferr != nil {
		ev.Error = ferr.Error()
	}
	h.sink.Submit(ev)
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func stripHopHeaders(h http.Header) {
	for _, k := range hopHeaders {
		h.Del(k)
	}
}

func singleJoin(a, b string) string {
	switch {
	case a == "" || a == "/":
		return b
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	default:
		return a + b
	}
}
