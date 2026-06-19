package ingest

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"

	"cc-gateway/internal/parse"
	"cc-gateway/internal/store"
)

const previewMax = 2000

// build turns a captured Event into a store.Record plus the live Written
// notification. It does all parsing and correlation off the hot path.
func build(ev *Event) (*store.Record, Written) {
	req := parse.ParseRequest(ev.ReqBody)
	resp := parse.ParseResponse(ev.RespBody, ev.IsSSE)

	model := firstNonEmpty(resp.Model, req.Model)
	if model == "" && ev.Stats != nil {
		model = ev.Stats.Model
	}

	usage := resp.Usage
	if usage == (parse.Usage{}) && ev.Stats != nil {
		usage = parse.Usage{
			InputTokens:              ev.Stats.InTokens,
			OutputTokens:             ev.Stats.OutTokens,
			CacheReadInputTokens:     ev.Stats.CacheRead,
			CacheCreationInputTokens: ev.Stats.CacheWrite,
		}
	}
	stop := resp.StopReason
	if stop == "" && ev.Stats != nil {
		stop = ev.Stats.StopReason
	}

	sysText := req.SystemText()
	rec := &store.Record{
		SessionKey:  sessionKey(req.MetadataUser, sysText),
		Model:       model,
		Cwd:         mineCwd(sysText),
		GitBranch:   mineBranch(sysText),
		CliVersion:  cliVersion(ev.UserAgent),
		StartedAt:   ev.StartedAt,
		EndedAt:     ev.EndedAt,
		DurationMs:  ev.EndedAt.Sub(ev.StartedAt).Milliseconds(),
		Method:      ev.Method,
		Path:        ev.Path,
		Status:      ev.Status,
		Stream:      req.Stream,
		IsSSE:       ev.IsSSE,
		Truncated:   ev.Truncated,
		ReqBody:     ev.ReqBody,
		RespBody:    ev.RespBody,
		In:          usage.InputTokens,
		Out:         usage.OutputTokens,
		CacheRead:   usage.CacheReadInputTokens,
		CacheWrite:  usage.CacheCreationInputTokens,
		StopReason:  stop,
		NumMessages: len(req.Messages),
		Error:       ev.Error,
	}
	rec.EstCost = estCost(model, usage)

	// Assistant response blocks drive tool analytics, previews and the call side
	// of each tool span.
	for i, b := range resp.Content {
		row := store.BlockRow{Role: "assistant", Idx: i, Type: b.Type}
		switch b.Type {
		case "text":
			setText(&row, b.Text)
		case "thinking":
			setText(&row, b.Thinking)
		case "tool_use":
			row.ToolName = b.Name
			row.ToolUseID = b.ID
			row.TextPreview = preview(parse.CompactJSON(b.Input))
			rec.NumTools++
		}
		rec.Blocks = append(rec.Blocks, row)
	}

	// The newest user turn carries the human prompt (searchable) and the
	// tool_result blocks that complete the previous turn's tool spans.
	if last := lastUserMessage(req.Messages); last != nil {
		for i, b := range last.Content {
			switch b.Type {
			case "text":
				row := store.BlockRow{Role: "user", Idx: i, Type: "text"}
				setText(&row, b.Text)
				rec.Blocks = append(rec.Blocks, row)
			case "tool_result":
				row := store.BlockRow{Role: "user", Idx: i, Type: "tool_result", ToolUseID: b.ToolUseID, IsError: b.IsError}
				setText(&row, resultText(b.Content))
				rec.Blocks = append(rec.Blocks, row)
			}
		}
	}

	w := Written{
		Type:       "request",
		SessionKey: rec.SessionKey,
		TsStart:    ev.StartedAt.UnixMilli(),
		DurationMs: rec.DurationMs,
		Status:     ev.Status,
		Model:      model,
		InTokens:   usage.InputTokens,
		OutTokens:  usage.OutputTokens,
		NumTools:   rec.NumTools,
		EstCost:    rec.EstCost,
		Error:      ev.Error,
	}
	return rec, w
}

// setText fills the preview and char length of a block from a text value.
func setText(r *store.BlockRow, s string) {
	r.CharLen = len(s)
	r.TextPreview = preview(s)
}

// lastUserMessage returns the last message authored by the user, or nil.
func lastUserMessage(msgs []parse.Message) *parse.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return &msgs[i]
		}
	}
	return nil
}

// resultText flattens a tool_result's content blocks into plain text.
func resultText(raw []byte) string {
	var b strings.Builder
	for _, blk := range parse.NormalizeResultContent(raw) {
		if blk.Text != "" {
			b.WriteString(blk.Text)
			b.WriteByte('\n')
		} else if blk.Type != "" && blk.Type != "text" {
			b.WriteString("[" + blk.Type + "]\n")
		}
	}
	return b.String()
}

func preview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > previewMax {
		s = s[:previewMax]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// session correlation
// ---------------------------------------------------------------------------

var sessionRe = regexp.MustCompile(`session_+([0-9a-fA-F-]{6,})`)

// sessionKey derives a stable session identifier. Claude Code's metadata.user_id
// embeds a session UUID; we prefer that. Failing that we fall back to the whole
// user_id, then to a hash of the system prompt so identical-context requests at
// least group together.
func sessionKey(metaUser, sysText string) string {
	if m := sessionRe.FindStringSubmatch(metaUser); m != nil {
		return "s_" + m[1]
	}
	if metaUser != "" {
		return "u_" + metaUser
	}
	sum := sha1.Sum([]byte(sysText))
	return "anon_" + hex.EncodeToString(sum[:])[:12]
}

var cwdRe = regexp.MustCompile(`(?i)(?:current\s+)?working directory:\s*(.+)`)
var branchRe = regexp.MustCompile(`(?i)current branch:\s*(\S+)`)
var cliRe = regexp.MustCompile(`claude-cli/(\S+)`)

func mineCwd(sysText string) string {
	if m := cwdRe.FindStringSubmatch(sysText); m != nil {
		return strings.TrimSpace(firstLine(m[1]))
	}
	return ""
}

func mineBranch(sysText string) string {
	if m := branchRe.FindStringSubmatch(sysText); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func cliVersion(ua string) string {
	if m := cliRe.FindStringSubmatch(ua); m != nil {
		return m[1]
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
