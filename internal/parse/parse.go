// Package parse turns Anthropic Messages API request and response bodies into
// structured values. It is pure: no I/O, no color, no terminal concerns. Both
// the terminal renderer and the database writer consume the same parsed form so
// the wire bytes are interpreted in exactly one place.
package parse

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// Usage holds the token accounting reported by the API.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ContentBlock is a single Anthropic content block. Different block types use
// different fields; only the ones present for a given Type are meaningful.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Message is one conversation turn with its content already normalized into a
// slice of blocks (a bare string body becomes a single text block).
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ParsedRequest is a decoded /v1/messages request body.
type ParsedRequest struct {
	OK           bool // false when the body is not a Messages request
	Model        string
	Stream       bool
	MaxTokens    int
	System       []ContentBlock
	Messages     []Message
	ToolNames    []string
	MetadataUser string // metadata.user_id, used for session correlation
	BodySize     int
}

// ParsedResponse is a decoded assistant reply, whether it arrived as a single
// JSON message or was reassembled from an SSE stream.
type ParsedResponse struct {
	OK         bool           `json:"ok"`
	Model      string         `json:"model,omitempty"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage      Usage          `json:"usage"`
}

// raw decode shapes ------------------------------------------------------------

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type rawRequest struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens"`
	System    json.RawMessage `json:"system"`
	Messages  []rawMessage    `json:"messages"`
	Tools     []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// ParseRequest decodes a Messages API request body. OK is false (and the zero
// value otherwise returned) when the body is not a JSON Messages request, in
// which case callers should fall back to showing the raw bytes.
func ParseRequest(body []byte) ParsedRequest {
	pr := ParsedRequest{BodySize: len(body)}
	if len(body) == 0 || body[0] != '{' {
		return pr
	}
	var rr rawRequest
	if json.Unmarshal(body, &rr) != nil {
		return pr
	}
	pr.OK = true
	pr.Model = rr.Model
	pr.Stream = rr.Stream
	pr.MaxTokens = rr.MaxTokens
	pr.MetadataUser = rr.Metadata.UserID
	pr.System = decodeContent(rr.System)
	for _, t := range rr.Tools {
		pr.ToolNames = append(pr.ToolNames, t.Name)
	}
	for _, m := range rr.Messages {
		pr.Messages = append(pr.Messages, Message{Role: m.Role, Content: decodeContent(m.Content)})
	}
	return pr
}

type rawResponse struct {
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// ParseResponse decodes an assistant reply. When isSSE is true the body is an
// event stream that is reassembled back into content blocks; otherwise it is a
// single JSON message. The body is transparently gunzipped first, since upstream
// responses are gzip-encoded whenever the client advertised it. OK is false for
// non-message payloads (errors, other endpoints) so callers can fall back to raw
// bytes.
func ParseResponse(body []byte, isSSE bool) ParsedResponse {
	body = maybeGunzip(body)
	if isSSE {
		return reassembleSSE(body)
	}
	return parseJSONResponse(body)
}

// ParseResponseAuto is like ParseResponse but determines on its own whether the
// (decompressed) body is an SSE stream or a single JSON message. Used when the
// stored is_sse flag is unreliable, e.g. re-parsing historical rows.
func ParseResponseAuto(body []byte) ParsedResponse {
	body = maybeGunzip(body)
	if looksSSE(body) {
		return reassembleSSE(body)
	}
	return parseJSONResponse(body)
}

func parseJSONResponse(body []byte) ParsedResponse {
	var pr ParsedResponse
	if len(body) == 0 || body[0] != '{' {
		return pr
	}
	var rr rawResponse
	if json.Unmarshal(body, &rr) != nil || len(rr.Content) == 0 {
		return pr
	}
	pr.OK = true
	pr.Model = rr.Model
	pr.Content = rr.Content
	pr.StopReason = rr.StopReason
	pr.Usage = rr.Usage
	return pr
}

// IsSSEBody reports whether a (possibly gzip-compressed) response body is an
// Anthropic event stream.
func IsSSEBody(body []byte) bool { return looksSSE(maybeGunzip(body)) }

// maybeGunzip decompresses body when it is gzip-compressed, returning it
// unchanged on any error or when it is not gzip.
func maybeGunzip(body []byte) []byte {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil || len(out) == 0 {
		return body
	}
	return out
}

// looksSSE reports whether body is an event stream (rather than a JSON message).
func looksSSE(body []byte) bool {
	head := bytes.TrimLeft(body, " \r\n\t")
	return bytes.HasPrefix(head, []byte("event:")) || bytes.HasPrefix(head, []byte("data:")) ||
		bytes.Contains(body, []byte("\ndata:"))
}

// decodeContent normalizes a message/system content value — which may be a bare
// string or an array of blocks — into a slice of ContentBlock.
func decodeContent(raw json.RawMessage) []ContentBlock {
	raw = bytes.TrimSpace(raw)
	if !HasJSON(raw) {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []ContentBlock{{Type: "text", Text: s}}
		}
	case '[':
		var blocks []ContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			return blocks
		}
	}
	// Unknown shape: keep it as text so nothing is silently dropped.
	return []ContentBlock{{Type: "text", Text: string(raw)}}
}

// NormalizeResultContent decodes a tool_result's content (a bare string or an
// array of blocks) into a slice of ContentBlock.
func NormalizeResultContent(raw json.RawMessage) []ContentBlock { return decodeContent(raw) }

// SystemText returns the system prompt blocks concatenated into plain text, used
// for mining session metadata (working directory, git branch) out of the
// Claude Code system prompt.
func (p ParsedRequest) SystemText() string {
	var b strings.Builder
	for _, blk := range p.System {
		if blk.Text != "" {
			b.WriteString(blk.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ToolUses returns the tool_use blocks in an assistant reply.
func (p ParsedResponse) ToolUses() []ContentBlock {
	var out []ContentBlock
	for _, b := range p.Content {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// HasJSON reports whether raw holds a non-empty, non-null JSON value.
func HasJSON(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && string(s) != "null"
}

// CompactJSON renders v as single-line JSON, or returns it unchanged if invalid.
func CompactJSON(v []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, v) == nil {
		return buf.String()
	}
	return string(v)
}

// SortedFields decodes a JSON object into key/value pairs sorted by key, with
// string values unquoted and nested structures left as compact JSON. It returns
// nil when raw is not a non-empty object.
type Field struct {
	Key   string
	Value string
	Multi bool // value is a multi-line string
}

func SortedFields(raw json.RawMessage) []Field {
	raw = bytes.TrimSpace(raw)
	if !HasJSON(raw) || string(raw) == "{}" || raw[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Field, 0, len(keys))
	for _, k := range keys {
		v := bytes.TrimSpace(m[k])
		if len(v) > 0 && v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				out = append(out, Field{Key: k, Value: s, Multi: strings.Contains(s, "\n")})
				continue
			}
		}
		out = append(out, Field{Key: k, Value: CompactJSON(v)})
	}
	return out
}
