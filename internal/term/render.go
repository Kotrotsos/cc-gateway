package term

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cc-gateway/internal/parse"
)

// RecordRequest renders a human-readable view of the request to the screen (when
// the s toggle is on) and writes the raw JSON to the recording file (when f is
// on). It is a no-op when neither sink is active.
func (con *Console) RecordRequest(id uint64, method, uri string, body []byte) {
	if !con.full.Load() && !con.rec.on() {
		return
	}
	header := fmt.Sprintf("#%d  REQUEST  %s %s", id, method, uri)
	if con.full.Load() {
		con.dumpLines(cCyan, header, con.renderRequest(body))
	}
	con.rec.write(header, body)
}

// RecordResponse renders the assistant reply to the screen and writes the raw
// bytes to the recording file.
func (con *Console) RecordResponse(id uint64, method, uri, status string, statusCode int, body []byte, isSSE bool, dur time.Duration) {
	if !con.full.Load() && !con.rec.on() {
		return
	}
	header := fmt.Sprintf("#%d  RESPONSE  %s %s   %s   %s", id, method, uri, status, dur.Round(time.Millisecond))
	if con.full.Load() {
		col := cGreen
		if statusCode >= 400 {
			col = cRed
		}
		con.dumpLines(col, header, con.renderResponse(body, isSSE))
	}
	con.rec.write(header, body)
}

// dumpLines prints a bordered, indented block of already-formatted lines
// atomically so concurrent requests never interleave.
func (con *Console) dumpLines(col, header string, lines []string) {
	con.mu.Lock()
	defer con.mu.Unlock()
	fmt.Printf("%s╞══ %s%s\n", con.color(col), header, con.color(cReset))
	for _, ln := range lines {
		fmt.Printf("  %s\n", ln)
	}
	fmt.Printf("%s╰──%s\n", con.color(cDim), con.color(cReset))
}

// renderRequest turns a Messages request into formatted lines: the system prompt
// followed by each message. Anything we can't parse falls back to pretty JSON.
func (con *Console) renderRequest(body []byte) []string {
	pr := parse.ParseRequest(body)
	if !pr.OK || len(pr.Messages) == 0 {
		return con.prettyOrRaw(body)
	}
	var out []string
	if len(pr.System) > 0 {
		out = append(out, con.paint(cMagenta, "system"))
		for _, b := range pr.System {
			con.renderBlock(b, "  ", &out)
		}
	}
	for _, m := range pr.Messages {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, con.roleLabel(m.Role))
		for _, b := range m.Content {
			con.renderBlock(b, "  ", &out)
		}
	}
	return out
}

// renderResponse formats the assistant reply (reassembled from SSE or read from
// a JSON message). Non-message payloads fall back to JSON.
func (con *Console) renderResponse(body []byte, isSSE bool) []string {
	pr := parse.ParseResponse(body, isSSE)
	if !pr.OK {
		return con.prettyOrRaw(body)
	}
	var out []string
	out = append(out, con.roleLabel("assistant")+con.dimParen(pr.Model))
	for _, b := range pr.Content {
		con.renderBlock(b, "  ", &out)
	}
	out = append(out, con.paint(cDim, tokenLine(pr.Usage, pr.StopReason)))
	return out
}

func (con *Console) renderBlock(b parse.ContentBlock, indent string, out *[]string) {
	switch b.Type {
	case "text":
		con.appendText(out, b.Text, indent)
	case "thinking":
		*out = append(*out, indent+con.paint(cMagenta, "thinking"))
		con.appendText(out, b.Thinking, indent+"  ")
	case "tool_use":
		*out = append(*out, indent+con.paint(cYellow, "tool_use: "+b.Name)+con.dimParen(b.ID))
		con.renderFields(b.Input, indent+"  ", out)
	case "tool_result":
		status := "ok"
		if b.IsError {
			status = "error"
		}
		*out = append(*out, indent+con.paint(cYellow, "tool_result")+con.dimParen(b.ToolUseID+" "+status))
		for _, sub := range parse.NormalizeResultContent(b.Content) {
			con.renderBlock(sub, indent+"  ", out)
		}
	case "image":
		*out = append(*out, indent+con.paint(cDim, "[image]"))
	case "document":
		*out = append(*out, indent+con.paint(cDim, "[document]"))
	default:
		if b.Text != "" {
			con.appendText(out, b.Text, indent)
		} else {
			*out = append(*out, indent+con.paint(cDim, "["+b.Type+"]"))
		}
	}
}

// renderFields prints a tool-use input object as readable "key: value" lines.
func (con *Console) renderFields(raw json.RawMessage, indent string, out *[]string) {
	fields := parse.SortedFields(raw)
	if fields == nil {
		if parse.HasJSON(raw) && string(bytes.TrimSpace(raw)) != "{}" {
			con.appendText(out, string(raw), indent)
		}
		return
	}
	for _, f := range fields {
		if f.Multi {
			*out = append(*out, indent+con.paint(cBold, f.Key)+":")
			con.appendText(out, f.Value, indent+"  ")
		} else {
			*out = append(*out, indent+con.paint(cBold, f.Key)+": "+f.Value)
		}
	}
}

// appendText splits text on newlines and appends each line with the indent.
func (con *Console) appendText(out *[]string, text, indent string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	for _, ln := range strings.Split(text, "\n") {
		*out = append(*out, indent+ln)
	}
}

func (con *Console) roleLabel(role string) string {
	col := cBold
	switch role {
	case "user":
		col = cCyan
	case "assistant":
		col = cGreen
	}
	return con.paint(col, role)
}

func (con *Console) dimParen(s string) string {
	if s == "" {
		return ""
	}
	return " " + con.paint(cDim, "("+s+")")
}

func tokenLine(u parse.Usage, stopReason string) string {
	s := fmt.Sprintf("tokens: in %d / out %d", u.InputTokens, u.OutputTokens)
	if u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
		s += fmt.Sprintf("   cache: read %d / write %d", u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
	if stopReason != "" {
		s += "   stop_reason: " + stopReason
	}
	return s
}

// prettyOrRaw indents JSON for readability, or splits non-JSON into lines.
func (con *Console) prettyOrRaw(body []byte) []string {
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

// contentType is a tiny convenience for response logging from a *http.Response.
func ContentType(resp *http.Response) string { return resp.Header.Get("Content-Type") }
