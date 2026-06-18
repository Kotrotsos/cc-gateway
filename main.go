// cc-gateway is a transparent, zero-modification proxy that sits between
// Claude Code and the Anthropic API. Its only job is to make the traffic
// visible: every request and response is forwarded byte-for-byte (so streaming,
// tool use and OAuth all keep working exactly as before) while the gateway
// prints an intuitive view of what is flowing past.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	port     = flag.String("port", "8443", "local port to listen on")
	upstream = flag.String("upstream", "https://api.anthropic.com", "upstream Anthropic base URL")
	showBody = flag.Bool("body", false, "also dump full request/response bodies")
	noColor  = flag.Bool("no-color", false, "disable ANSI colors")
)

var reqCounter uint64

// screenFull, when true, prints the full request and response bodies to the
// screen in addition to the usual summary. Toggled live with the "s" key.
var screenFull atomic.Bool

// rec is the live file recorder, toggled on/off with the "f" key.
var rec recorder

// hop-by-hop headers are connection-specific and must not be forwarded.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func main() {
	flag.Parse()

	up, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -upstream: %v\n", err)
		os.Exit(1)
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment, // honor HTTPS_PROXY for outbound
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		// No timeout: streaming completions can run for minutes.
	}

	h := &handler{upstream: up, client: client}

	srv := &http.Server{
		Addr:              "localhost:" + *port,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		// No WriteTimeout: we stream long-lived SSE responses.
	}

	// The -body flag sets the initial state of the live screen toggle.
	screenFull.Store(*showBody)

	keys := startKeyboard()
	printBanner(*port, up.String(), keys)

	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

type handler struct {
	upstream *url.URL
	client   *http.Client
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := atomic.AddUint64(&reqCounter, 1)
	start := time.Now()

	// Buffer the request body so we can summarize it and still forward it.
	// Claude Code request bodies are complete JSON documents, not streams.
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	logRequest(id, r, body)
	recordRequest(id, r, body)

	// Build the upstream request: same method, path, query, headers, body.
	target := *h.upstream
	target.Path = singleJoin(h.upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		logError(id, "build request", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	stripHopHeaders(outReq.Header)
	outReq.Host = h.upstream.Host

	resp, err := h.client.Do(outReq)
	if err != nil {
		logError(id, "upstream", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
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

	stats := &sseStats{events: map[string]int{}}
	var rawBody bytes.Buffer

	// Decide once, up front, whether to keep a copy of the response body for the
	// screen dump or the file recorder. A toggle flipped mid-stream takes effect
	// on the next request, not retroactively on this one.
	capture := screenFull.Load() || rec.on()

	// Stream the response straight through, flushing each chunk so Claude Code
	// sees tokens arrive in real time. We tap the bytes read-only for the summary.
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			w.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
			if isSSE {
				stats.feed(chunk)
			}
			if capture {
				rawBody.Write(chunk)
			}
		}
		if rerr != nil {
			break
		}
	}

	logResponse(id, resp, isSSE, stats, time.Since(start))
	if capture {
		recordResponse(id, r, resp, rawBody.Bytes(), time.Since(start))
	}
}

// ---------------------------------------------------------------------------
// SSE tap
// ---------------------------------------------------------------------------

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
	Usage *usage `json:"usage"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

// sseStats parses a Server-Sent-Events stream incrementally to build a summary
// of the event flow and token usage without altering the bytes in any way.
type sseStats struct {
	leftover   []byte
	events     map[string]int
	order      []string
	model      string
	inTokens   int
	outTokens  int
	cacheRead  int
	cacheWrite int
	stopReason string
}

func (s *sseStats) feed(b []byte) {
	s.leftover = append(s.leftover, b...)
	for {
		i := bytes.IndexByte(s.leftover, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimRight(s.leftover[:i], "\r")
		s.leftover = s.leftover[i+1:]
		s.handleLine(line)
	}
}

func (s *sseStats) handleLine(line []byte) {
	const prefix = "data: "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	data := bytes.TrimPrefix(line, []byte(prefix))
	var ev sseEvent
	if json.Unmarshal(data, &ev) != nil || ev.Type == "" {
		return
	}
	if _, seen := s.events[ev.Type]; !seen {
		s.order = append(s.order, ev.Type)
	}
	s.events[ev.Type]++

	switch ev.Type {
	case "message_start":
		s.model = ev.Message.Model
		s.inTokens = ev.Message.Usage.InputTokens
		s.cacheRead = ev.Message.Usage.CacheReadInputTokens
		s.cacheWrite = ev.Message.Usage.CacheCreationInputTokens
	case "message_delta":
		if ev.Usage != nil {
			s.outTokens = ev.Usage.OutputTokens
		}
		if ev.Delta.StopReason != "" {
			s.stopReason = ev.Delta.StopReason
		}
	}
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

var printMu sync.Mutex

func logRequest(id uint64, r *http.Request, body []byte) {
	var lines []string
	lines = append(lines, fmt.Sprintf("%s %s", c(cBold, r.Method), r.URL.RequestURI()))

	if info := parseReqBody(body); info != nil {
		meta := []string{}
		if info.Model != "" {
			meta = append(meta, "model "+c(cYellow, info.Model))
		}
		meta = append(meta, fmt.Sprintf("stream %v", info.Stream))
		if info.MaxTokens > 0 {
			meta = append(meta, fmt.Sprintf("max_tokens %d", info.MaxTokens))
		}
		lines = append(lines, strings.Join(meta, "   "))

		counts := []string{
			fmt.Sprintf("%d messages", len(info.Messages)),
			fmt.Sprintf("%d tools", len(info.Tools)),
		}
		if len(info.System) > 0 && string(info.System) != "null" {
			counts = append(counts, "system prompt")
		}
		counts = append(counts, fmt.Sprintf("%s body", humanBytes(len(body))))
		lines = append(lines, c(cDim, strings.Join(counts, "   ")))
	} else if len(body) > 0 {
		lines = append(lines, c(cDim, fmt.Sprintf("%s body", humanBytes(len(body)))))
	}

	block(cCyan, fmt.Sprintf("→ #%d  REQUEST", id), time.Now(), lines)
}

func logResponse(id uint64, resp *http.Response, isSSE bool, s *sseStats, dur time.Duration) {
	color := cGreen
	if resp.StatusCode >= 400 {
		color = cRed
	}
	var lines []string
	status := fmt.Sprintf("%s   %s   %s", c(cBold, resp.Status), resp.Header.Get("Content-Type"), dur.Round(time.Millisecond))
	lines = append(lines, status)

	if isSSE && len(s.order) > 0 {
		var flow []string
		for _, name := range s.order {
			n := s.events[name]
			if n > 1 {
				flow = append(flow, fmt.Sprintf("%s ×%d", name, n))
			} else {
				flow = append(flow, name)
			}
		}
		lines = append(lines, c(cDim, "flow: ")+strings.Join(flow, " → "))

		tok := fmt.Sprintf("tokens: in %s / out %s", c(cYellow, itoa(s.inTokens)), c(cYellow, itoa(s.outTokens)))
		if s.cacheRead > 0 || s.cacheWrite > 0 {
			tok += fmt.Sprintf("   cache: read %d / write %d", s.cacheRead, s.cacheWrite)
		}
		lines = append(lines, tok)
		if s.stopReason != "" {
			lines = append(lines, c(cDim, "stop_reason: ")+s.stopReason)
		}
	}

	block(color, fmt.Sprintf("← #%d  RESPONSE", id), time.Now(), lines)
}

func logError(id uint64, where string, err error) {
	block(cRed, fmt.Sprintf("✗ #%d  ERROR", id), time.Now(), []string{where + ": " + err.Error()})
}

// recordRequest dumps a full request (headers omitted, body in full) to the
// screen and/or the recording file, depending on the current toggle state.
func recordRequest(id uint64, r *http.Request, body []byte) {
	if !screenFull.Load() && !rec.on() {
		return
	}
	header := fmt.Sprintf("#%d  REQUEST  %s %s", id, r.Method, r.URL.RequestURI())
	if screenFull.Load() {
		dumpToScreen(cCyan, header, body)
	}
	rec.write(header, body)
}

// recordResponse dumps a full response body (the raw, byte-for-byte stream) to
// the screen and/or the recording file.
func recordResponse(id uint64, r *http.Request, resp *http.Response, body []byte, dur time.Duration) {
	header := fmt.Sprintf("#%d  RESPONSE  %s %s   %s   %s",
		id, r.Method, r.URL.RequestURI(), resp.Status, dur.Round(time.Millisecond))
	if screenFull.Load() {
		dumpToScreen(cGreen, header, body)
	}
	rec.write(header, body)
}

// dumpToScreen prints one full body, bordered and dimmed, atomically.
func dumpToScreen(col, header string, body []byte) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf("%s╞══ %s%s\n", color(col), header, color(cReset))
	os.Stdout.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Println()
	}
	fmt.Printf("%s╰──%s\n", color(cDim), color(cReset))
}

// block prints a titled, bordered group atomically so concurrent requests
// never interleave mid-block.
func block(col, title string, t time.Time, lines []string) {
	printMu.Lock()
	defer printMu.Unlock()
	ts := t.Format("15:04:05.000")
	fmt.Printf("%s┌─ %s %s%s\n", color(col), title, color(cDim)+ts, color(cReset))
	for _, ln := range lines {
		fmt.Printf("%s│%s  %s\n", color(col), color(cReset), ln)
	}
	fmt.Printf("%s└─%s\n", color(col), color(cReset))
}

type reqBody struct {
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
	Messages  []json.RawMessage `json:"messages"`
	System    json.RawMessage   `json:"system"`
	Tools     []json.RawMessage `json:"tools"`
}

func parseReqBody(body []byte) *reqBody {
	if len(body) == 0 || body[0] != '{' {
		return nil
	}
	var rb reqBody
	if json.Unmarshal(body, &rb) != nil {
		return nil
	}
	return &rb
}

func printBanner(port, up string, keys bool) {
	base := "http://localhost:" + port
	b := color(cBold)
	r := color(cReset)
	cy := color(cCyan)
	dim := color(cDim)

	fmt.Printf(`
%s  cc-gateway %s  transparent Claude Code traffic monitor
%s  forwarding %s  →  %s%s

%sPoint Claude Code at the gateway (any one of these):%s

  %sexport ANTHROPIC_BASE_URL=%s%s
  claude

%sor for a single run:%s

  %sANTHROPIC_BASE_URL=%s claude%s

%sNothing is modified, filtered, or stored. Your existing auth (OAuth token
or ANTHROPIC_API_KEY) passes straight through. Ctrl-C to stop.%s
`,
		b, r, dim, base, up, r,
		b, r,
		cy, base, r,
		b, r,
		cy, base, r,
		dim, r,
	)

	if keys {
		fmt.Printf(`
%shotkeys:%s  %ss%s show full bodies on screen (toggle)   %sf%s record everything to a file (toggle)
`, b, r, cy, r, cy, r)
	}

	fmt.Printf("\n%s──────────────────────────────────────────────────────────────────────%s\n\n", dim, r)
}

// ---------------------------------------------------------------------------
// file recorder  (toggled with "f")
// ---------------------------------------------------------------------------

// recorder writes a plain-text, ANSI-free transcript of full requests and
// responses to a timestamped file. Pressing "f" opens a fresh file; pressing
// "f" again closes it. It is safe for concurrent in-flight requests.
type recorder struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func (rec *recorder) on() bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.file != nil
}

// toggle starts a new recording file or stops the current one.
func (rec *recorder) toggle() {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.file != nil {
		path := rec.path
		rec.file.Close()
		rec.file = nil
		rec.path = ""
		statusLine(cYellow, "recording OFF  →  "+path)
		return
	}

	path := fmt.Sprintf("cc-gateway-%s.log", time.Now().Format("20060102-150405"))
	f, err := os.Create(path)
	if err != nil {
		statusLine(cRed, "recording failed: "+err.Error())
		return
	}
	rec.file = f
	rec.path = path
	statusLine(cGreen, "recording ON  →  "+path+"  (full request/response bodies)")
}

// write appends one full request or response record to the file, if recording.
func (rec *recorder) write(header string, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.file == nil {
		return
	}
	const bar = "════════════════════════════════════════════════════════════════════"
	fmt.Fprintf(rec.file, "%s\n%s  %s\n%s\n", bar, header, time.Now().Format("2006-01-02 15:04:05.000"),
		"────────────────────────────────────────────────────────────────────")
	rec.file.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		rec.file.WriteString("\n")
	}
	rec.file.WriteString("\n")
}

// ---------------------------------------------------------------------------
// keyboard  (live toggles)
// ---------------------------------------------------------------------------

var origStty string

// startKeyboard puts the terminal into cbreak mode so single keypresses are
// read without Enter, and launches the key loop. It returns false (and changes
// nothing) when stdin is not an interactive terminal. Ctrl-C still works: we
// leave signal generation enabled and restore the terminal in a signal handler.
func startKeyboard() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	st, err := stty("-g")
	if err != nil {
		return false
	}
	origStty = strings.TrimSpace(st)
	if _, err := stty("-icanon", "-echo", "min", "1", "time", "0"); err != nil {
		return false
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		restoreStty()
		os.Exit(0)
	}()

	go keyLoop()
	return true
}

func keyLoop() {
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case 's', 'S':
			now := !screenFull.Load()
			screenFull.Store(now)
			if now {
				statusLine(cGreen, "screen: showing FULL request/response bodies  (press s to stop)")
			} else {
				statusLine(cYellow, "screen: full bodies OFF")
			}
		case 'f', 'F':
			rec.toggle()
		}
	}
}

// stty runs the stty utility against the controlling terminal (stdin).
func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return string(out), err
}

func restoreStty() {
	if origStty != "" {
		stty(origStty)
	}
}

// statusLine prints a single highlighted toggle-state line atomically.
func statusLine(col, msg string) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf("%s● %s%s\n", color(col), msg, color(cReset))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// ANSI colors
const (
	cReset  = "0"
	cBold   = "1"
	cDim    = "2"
	cRed    = "31"
	cGreen  = "32"
	cYellow = "33"
	cCyan   = "36"
)

func color(code string) string {
	if *noColor || os.Getenv("NO_COLOR") != "" {
		return ""
	}
	return "\033[" + code + "m"
}

// c wraps s in the given color and resets after.
func c(code, s string) string {
	if *noColor || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}
