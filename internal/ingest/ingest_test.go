package ingest

import (
	"path/filepath"
	"testing"
	"time"

	"cc-gateway/internal/parse"
	"cc-gateway/internal/store"
)

const reqBody1 = `{
  "model":"claude-opus-4-8","stream":true,"max_tokens":1024,
  "metadata":{"user_id":"user_abc_account__session_DEADBEEF-1111"},
  "system":[{"type":"text","text":"You are Claude Code.\nWorking directory: /Users/me/proj\nCurrent branch: main"}],
  "messages":[{"role":"user","content":"fix the bug"}],
  "tools":[{"name":"Read"},{"name":"Bash"}]
}`

const sseResp1 = `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":4201,"cache_read_input_tokens":3800}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look at it."}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/x\"}"}}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}
`

const reqBody2 = `{
  "model":"claude-opus-4-8","stream":true,
  "metadata":{"user_id":"user_abc_account__session_DEADBEEF-1111"},
  "system":[{"type":"text","text":"You are Claude Code.\nWorking directory: /Users/me/proj"}],
  "messages":[
    {"role":"user","content":"fix the bug"},
    {"role":"assistant","content":[{"type":"text","text":"Let me look at it."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/x"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"package main","is_error":false}]}
  ]
}`

const sseResp2 = `data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":4300}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Fixed the off-by-one."}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":17}}
`

func feed(t *testing.T, s *store.Store, req, resp string, ua string, start time.Time) {
	t.Helper()
	stats := parse.NewLiveStats()
	stats.Feed([]byte(resp))
	ev := &Event{
		Method: "POST", Path: "/v1/messages", URL: "/v1/messages",
		ReqBody: []byte(req), RespBody: []byte(resp), IsSSE: true,
		Status: 200, StatusText: "200 OK", ContentType: "text/event-stream",
		StartedAt: start, EndedAt: start.Add(800 * time.Millisecond),
		UserAgent: ua, Stats: stats,
	}
	rec, _ := build(ev)
	if _, _, _, err := s.WriteExchange(rec); err != nil {
		t.Fatalf("WriteExchange: %v", err)
	}
}

func TestPipeline(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	base := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	feed(t, st, reqBody1, sseResp1, "claude-cli/2.1.0 (external)", base)
	feed(t, st, reqBody2, sseResp2, "claude-cli/2.1.0 (external)", base.Add(5*time.Second))

	// One session, correlated by the embedded session id, with mined metadata.
	sessions, err := st.ListSessions(store.SessionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.SessionKey != "s_DEADBEEF-1111" {
		t.Errorf("session key = %q", s.SessionKey)
	}
	if s.Cwd != "/Users/me/proj" {
		t.Errorf("cwd = %q", s.Cwd)
	}
	if s.GitBranch != "main" {
		t.Errorf("branch = %q", s.GitBranch)
	}
	if s.CliVersion != "2.1.0" {
		t.Errorf("cli = %q", s.CliVersion)
	}
	if s.NumRequests != 2 {
		t.Errorf("num_requests = %d", s.NumRequests)
	}
	if s.OutTokens != 42+17 {
		t.Errorf("out tokens = %d", s.OutTokens)
	}

	// Trace: two requests and one reconstructed span linking req1 -> req2.
	d, err := st.GetSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Requests) != 2 {
		t.Fatalf("want 2 requests, got %d", len(d.Requests))
	}
	if got := d.Requests[0].ToolUses; len(got) != 1 || got[0].Name != "Read" {
		t.Errorf("req0 tool uses = %+v", got)
	}
	if len(d.Spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(d.Spans))
	}
	sp := d.Spans[0]
	if !sp.HasResult {
		t.Errorf("span has no result")
	}
	if sp.CallRequest != d.Requests[0].ID || sp.ResultReq != d.Requests[1].ID {
		t.Errorf("span links call=%d result=%d (reqs %d,%d)", sp.CallRequest, sp.ResultReq, d.Requests[0].ID, d.Requests[1].ID)
	}
	wantDur := d.Requests[1].TsStart - d.Requests[0].TsEnd
	if sp.DurationMs != wantDur {
		t.Errorf("span duration = %d, want %d", sp.DurationMs, wantDur)
	}

	// Analytics: two tool? only one tool_use, model histogram, series.
	a, err := st.Analytics(store.AnalyticsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Totals.Requests != 2 {
		t.Errorf("totals.requests = %d", a.Totals.Requests)
	}
	if len(a.Tools) != 1 || a.Tools[0].Name != "Read" || a.Tools[0].Count != 1 {
		t.Errorf("tools = %+v", a.Tools)
	}

	// Search finds assistant text.
	hits, err := st.Search("off-by-one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Errorf("search returned no hits")
	}

	// Request detail reconstructs the SSE response.
	rd, err := st.GetRequest(d.Requests[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rd.Response.OK || len(rd.Response.Content) != 2 {
		t.Errorf("response content = %+v", rd.Response.Content)
	}
	if rd.RawResponse == nil || *rd.RawResponse == "" {
		t.Errorf("raw response missing")
	}
}
