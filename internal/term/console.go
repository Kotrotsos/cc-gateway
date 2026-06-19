// Package term renders the live terminal view of gateway traffic and owns the
// interactive screen/record toggles. It is a thin consumer of package parse:
// all interpretation of wire bytes happens there, term only formats and prints.
package term

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-gateway/internal/parse"
)

// ANSI colors.
const (
	cReset   = "0"
	cBold    = "1"
	cDim     = "2"
	cRed     = "31"
	cGreen   = "32"
	cYellow  = "33"
	cMagenta = "35"
	cCyan    = "36"
)

// Console holds terminal-view state: the colour setting, the print mutex, the
// "show formatted messages" toggle (s) and the file recorder (f).
type Console struct {
	noColor bool
	mu      sync.Mutex // serializes all stdout writes
	full    atomic.Bool
	rec     recorder

	origStty string
}

// New returns a Console. startFull sets the initial state of the s toggle.
func New(noColor bool, startFull bool) *Console {
	c := &Console{noColor: noColor}
	c.full.Store(startFull)
	return c
}

// WantsBody reports whether the terminal view currently needs the full request
// and response bodies (because the s toggle or file recorder is on). The proxy
// combines this with the store setting to decide whether to capture bytes.
func (con *Console) WantsBody() bool { return con.full.Load() || con.rec.on() }

func (con *Console) color(code string) string {
	if con.noColor || os.Getenv("NO_COLOR") != "" {
		return ""
	}
	return "\033[" + code + "m"
}

// paint wraps s in the given colour and resets after.
func (con *Console) paint(code, s string) string {
	if con.noColor || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// block prints a titled, bordered group atomically so concurrent requests never
// interleave mid-block.
func (con *Console) block(col, title string, t time.Time, lines []string) {
	con.mu.Lock()
	defer con.mu.Unlock()
	ts := t.Format("15:04:05.000")
	fmt.Printf("%s┌─ %s %s%s\n", con.color(col), title, con.color(cDim)+ts, con.color(cReset))
	for _, ln := range lines {
		fmt.Printf("%s│%s  %s\n", con.color(col), con.color(cReset), ln)
	}
	fmt.Printf("%s└─%s\n", con.color(col), con.color(cReset))
}

// LogRequest prints the one-glance request summary block.
func (con *Console) LogRequest(id uint64, method, uri string, body []byte) {
	var lines []string
	lines = append(lines, fmt.Sprintf("%s %s", con.paint(cBold, method), uri))

	if pr := parse.ParseRequest(body); pr.OK {
		meta := []string{}
		if pr.Model != "" {
			meta = append(meta, "model "+con.paint(cYellow, pr.Model))
		}
		meta = append(meta, fmt.Sprintf("stream %v", pr.Stream))
		if pr.MaxTokens > 0 {
			meta = append(meta, fmt.Sprintf("max_tokens %d", pr.MaxTokens))
		}
		lines = append(lines, strings.Join(meta, "   "))

		counts := []string{
			fmt.Sprintf("%d messages", len(pr.Messages)),
			fmt.Sprintf("%d tools", len(pr.ToolNames)),
		}
		if len(pr.System) > 0 {
			counts = append(counts, "system prompt")
		}
		counts = append(counts, fmt.Sprintf("%s body", HumanBytes(len(body))))
		lines = append(lines, con.paint(cDim, strings.Join(counts, "   ")))
	} else if len(body) > 0 {
		lines = append(lines, con.paint(cDim, fmt.Sprintf("%s body", HumanBytes(len(body)))))
	}

	con.block(cCyan, fmt.Sprintf("→ #%d  REQUEST", id), time.Now(), lines)
}

// LogResponse prints the one-glance response summary block, including the SSE
// flow shape and token usage when available.
func (con *Console) LogResponse(id uint64, status string, statusCode int, contentType string, isSSE bool, s *parse.LiveStats, dur time.Duration) {
	col := cGreen
	if statusCode >= 400 {
		col = cRed
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("%s   %s   %s", con.paint(cBold, status), contentType, dur.Round(time.Millisecond)))

	if isSSE && s != nil && len(s.Order) > 0 {
		var flow []string
		for _, name := range s.Order {
			n := s.Events[name]
			if n > 1 {
				flow = append(flow, fmt.Sprintf("%s ×%d", name, n))
			} else {
				flow = append(flow, name)
			}
		}
		lines = append(lines, con.paint(cDim, "flow: ")+strings.Join(flow, " → "))

		tok := fmt.Sprintf("tokens: in %s / out %s", con.paint(cYellow, itoa(s.InTokens)), con.paint(cYellow, itoa(s.OutTokens)))
		if s.CacheRead > 0 || s.CacheWrite > 0 {
			tok += fmt.Sprintf("   cache: read %d / write %d", s.CacheRead, s.CacheWrite)
		}
		lines = append(lines, tok)
		if s.StopReason != "" {
			lines = append(lines, con.paint(cDim, "stop_reason: ")+s.StopReason)
		}
	}

	con.block(col, fmt.Sprintf("← #%d  RESPONSE", id), time.Now(), lines)
}

// LogError prints a red error block.
func (con *Console) LogError(id uint64, where string, err error) {
	con.block(cRed, fmt.Sprintf("✗ #%d  ERROR", id), time.Now(), []string{where + ": " + err.Error()})
}

// statusLine prints a single highlighted toggle-state line atomically.
func (con *Console) statusLine(col, msg string) {
	con.mu.Lock()
	defer con.mu.Unlock()
	fmt.Printf("%s● %s%s\n", con.color(col), msg, con.color(cReset))
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// HumanBytes formats a byte count as B / KB / MB.
func HumanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
