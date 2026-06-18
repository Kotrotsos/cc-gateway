// cc-gateway is a transparent, zero-modification proxy that sits between
// Claude Code and the Anthropic API. Its only job is to make the traffic
// visible: every request and response is forwarded byte-for-byte (so streaming,
// tool use and OAuth all keep working exactly as before) while the gateway
// prints an intuitive view of what is flowing past.
package main

import (
	"bufio"
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
	"sort"
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
		recordResponse(id, r, resp, rawBody.Bytes(), isSSE, time.Since(start))
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

// recordRequest sends a request to the two sinks: the screen gets a formatted,
// human-readable view of the conversation; the file gets the raw JSON verbatim.
func recordRequest(id uint64, r *http.Request, body []byte) {
	if !screenFull.Load() && !rec.on() {
		return
	}
	header := fmt.Sprintf("#%d  REQUEST  %s %s", id, r.Method, r.URL.RequestURI())
	if screenFull.Load() {
		dumpLinesToScreen(cCyan, header, renderRequest(body))
	}
	rec.write(header, body) // raw JSON to file
}

// recordResponse renders the assistant's reply (assembled from the SSE stream or
// the JSON body) to the screen, and writes the raw bytes to the file.
func recordResponse(id uint64, r *http.Request, resp *http.Response, body []byte, isSSE bool, dur time.Duration) {
	header := fmt.Sprintf("#%d  RESPONSE  %s %s   %s   %s",
		id, r.Method, r.URL.RequestURI(), resp.Status, dur.Round(time.Millisecond))
	if screenFull.Load() {
		col := cGreen
		if resp.StatusCode >= 400 {
			col = cRed
		}
		dumpLinesToScreen(col, header, renderResponse(body, isSSE))
	}
	rec.write(header, body) // raw bytes to file
}

// dumpLinesToScreen prints a bordered, indented block of already-formatted lines
// atomically so concurrent requests never interleave.
func dumpLinesToScreen(col, header string, lines []string) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf("%s╞══ %s%s\n", color(col), header, color(cReset))
	for _, ln := range lines {
		fmt.Printf("  %s\n", ln)
	}
	fmt.Printf("%s╰──%s\n", color(cDim), color(cReset))
}

// ---------------------------------------------------------------------------
// message formatting  (screen view — never raw JSON)
// ---------------------------------------------------------------------------

// contentBlock is a single Anthropic content block. Different block types use
// different fields; only the ones present for a given Type are meaningful.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type fullReq struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Messages []apiMessage    `json:"messages"`
}

// renderRequest turns a Messages API request body into formatted lines: the
// system prompt followed by each message, with text, tool calls and tool
// results laid out readably. Anything we can't parse falls back to pretty JSON.
func renderRequest(body []byte) []string {
	var fr fullReq
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &fr) != nil || len(fr.Messages) == 0 {
		return prettyOrRaw(body)
	}
	var out []string
	if hasJSON(fr.System) {
		out = append(out, c(cMagenta, "system"))
		renderContent(fr.System, "  ", &out)
	}
	for _, m := range fr.Messages {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, roleLabel(m.Role))
		renderContent(m.Content, "  ", &out)
	}
	return out
}

type respMsg struct {
	Model      string         `json:"model"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      usage          `json:"usage"`
}

// renderResponse formats the assistant reply. For SSE it assembles the streamed
// deltas back into content blocks; for a plain JSON message it reads them
// directly. Non-message payloads (errors, other endpoints) fall back to JSON.
func renderResponse(body []byte, isSSE bool) []string {
	if isSSE {
		return renderSSEResponse(body)
	}
	var rm respMsg
	if len(body) > 0 && body[0] == '{' && json.Unmarshal(body, &rm) == nil && len(rm.Content) > 0 {
		var out []string
		out = append(out, roleLabel("assistant")+dimParen(rm.Model))
		for _, b := range rm.Content {
			renderBlock(b, "  ", &out)
		}
		out = append(out, c(cDim, tokenLine(rm.Usage.InputTokens, rm.Usage.OutputTokens,
			rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens, rm.StopReason)))
		return out
	}
	return prettyOrRaw(body)
}

type asmBlock struct {
	typ  string
	name string
	id   string
	buf  strings.Builder
}

type sseContentEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
}

func renderSSEResponse(body []byte) []string {
	var (
		model      string
		order      []int
		blocks     = map[int]*asmBlock{}
		stopReason string
		inTok      int
		outTok     int
		cacheR     int
		cacheW     int
	)
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseContentEvent
		if json.Unmarshal([]byte(line[len("data: "):]), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			model = ev.Message.Model
			inTok = ev.Message.Usage.InputTokens
			cacheR = ev.Message.Usage.CacheReadInputTokens
			cacheW = ev.Message.Usage.CacheCreationInputTokens
		case "content_block_start":
			b := &asmBlock{typ: ev.ContentBlock.Type, name: ev.ContentBlock.Name, id: ev.ContentBlock.ID}
			b.buf.WriteString(ev.ContentBlock.Text)
			b.buf.WriteString(ev.ContentBlock.Thinking)
			blocks[ev.Index] = b
			order = append(order, ev.Index)
		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil {
				break
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.buf.WriteString(ev.Delta.Text)
			case "thinking_delta":
				b.buf.WriteString(ev.Delta.Thinking)
			case "input_json_delta":
				b.buf.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				outTok = ev.Usage.OutputTokens
			}
		}
	}

	var out []string
	out = append(out, roleLabel("assistant")+dimParen(model))
	for _, idx := range order {
		b := blocks[idx]
		cb := contentBlock{Type: b.typ, Name: b.name, ID: b.id}
		switch b.typ {
		case "text":
			cb.Text = b.buf.String()
		case "thinking":
			cb.Thinking = b.buf.String()
		case "tool_use":
			cb.Input = json.RawMessage(b.buf.String())
		}
		renderBlock(cb, "  ", &out)
	}
	out = append(out, c(cDim, tokenLine(inTok, outTok, cacheR, cacheW, stopReason)))
	return out
}

// renderContent formats a message's content, which may be a bare string or an
// array of content blocks.
func renderContent(raw json.RawMessage, indent string, out *[]string) {
	raw = bytes.TrimSpace(raw)
	if !hasJSON(raw) {
		return
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			appendText(out, s, indent)
			return
		}
	case '[':
		var blocks []contentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			for _, b := range blocks {
				renderBlock(b, indent, out)
			}
			return
		}
	}
	appendText(out, string(raw), indent)
}

func renderBlock(b contentBlock, indent string, out *[]string) {
	switch b.Type {
	case "text":
		appendText(out, b.Text, indent)
	case "thinking":
		*out = append(*out, indent+c(cMagenta, "thinking"))
		appendText(out, b.Thinking, indent+"  ")
	case "tool_use":
		*out = append(*out, indent+c(cYellow, "tool_use: "+b.Name)+dimParen(b.ID))
		renderJSONFields(b.Input, indent+"  ", out)
	case "tool_result":
		status := "ok"
		if b.IsError {
			status = "error"
		}
		*out = append(*out, indent+c(cYellow, "tool_result")+dimParen(b.ToolUseID+" "+status))
		renderContent(b.Content, indent+"  ", out)
	case "image":
		*out = append(*out, indent+c(cDim, "[image]"))
	case "document":
		*out = append(*out, indent+c(cDim, "[document]"))
	default:
		if b.Text != "" {
			appendText(out, b.Text, indent)
		} else {
			*out = append(*out, indent+c(cDim, "["+b.Type+"]"))
		}
	}
}

// renderJSONFields prints a tool-use input object as readable "key: value"
// lines (one per top-level field, keys sorted) rather than a JSON blob. String
// values keep their newlines; nested structures are shown as compact JSON.
func renderJSONFields(raw json.RawMessage, indent string, out *[]string) {
	raw = bytes.TrimSpace(raw)
	if !hasJSON(raw) || string(raw) == "{}" {
		return
	}
	var m map[string]json.RawMessage
	if raw[0] != '{' || json.Unmarshal(raw, &m) != nil {
		appendText(out, string(raw), indent)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := bytes.TrimSpace(m[k])
		if len(v) > 0 && v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				if strings.Contains(s, "\n") {
					*out = append(*out, indent+c(cBold, k)+":")
					appendText(out, s, indent+"  ")
				} else {
					*out = append(*out, indent+c(cBold, k)+": "+s)
				}
				continue
			}
		}
		*out = append(*out, indent+c(cBold, k)+": "+compactJSON(v))
	}
}

// appendText splits text on newlines and appends each line with the indent so
// the screen view preserves the original line breaks.
func appendText(out *[]string, text, indent string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	for _, ln := range strings.Split(text, "\n") {
		*out = append(*out, indent+ln)
	}
}

func roleLabel(role string) string {
	col := cBold
	switch role {
	case "user":
		col = cCyan
	case "assistant":
		col = cGreen
	}
	return c(col, role)
}

func dimParen(s string) string {
	if s == "" {
		return ""
	}
	return " " + c(cDim, "("+s+")")
}

func tokenLine(in, out, cacheR, cacheW int, stopReason string) string {
	s := fmt.Sprintf("tokens: in %d / out %d", in, out)
	if cacheR > 0 || cacheW > 0 {
		s += fmt.Sprintf("   cache: read %d / write %d", cacheR, cacheW)
	}
	if stopReason != "" {
		s += "   stop_reason: " + stopReason
	}
	return s
}

func compactJSON(v []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, v) == nil {
		return buf.String()
	}
	return string(v)
}

// prettyOrRaw indents JSON for readability, or returns the body split into
// lines when it isn't JSON.
func prettyOrRaw(body []byte) []string {
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') {
		var buf bytes.Buffer
		if json.Indent(&buf, body, "", "  ") == nil {
			return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		}
	}
	s := strings.TrimRight(string(body), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// hasJSON reports whether raw holds a non-empty, non-null JSON value.
func hasJSON(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && string(s) != "null"
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
%shotkeys:%s  %ss%s show formatted messages on screen (toggle)   %sf%s record raw JSON to a file (toggle)
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
	statusLine(cGreen, "recording ON  →  "+path+"  (raw JSON request/response bodies)")
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
				statusLine(cGreen, "screen: showing FORMATTED messages  (press s to stop)")
			} else {
				statusLine(cYellow, "screen: formatted messages OFF")
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
	cRed     = "31"
	cGreen   = "32"
	cYellow  = "33"
	cMagenta = "35"
	cCyan    = "36"
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
