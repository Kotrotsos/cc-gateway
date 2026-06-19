package parse

import (
	"bufio"
	"bytes"
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Live SSE stats — fed chunk-by-chunk on the hot path to build a summary of the
// event flow and token usage without altering or buffering the whole stream.
// ---------------------------------------------------------------------------

type sseSummaryEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage Usage  `json:"usage"`
	} `json:"message"`
	Usage *Usage `json:"usage"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

// LiveStats parses an SSE stream incrementally as bytes arrive. It is safe to
// feed arbitrary chunk boundaries; partial lines are buffered until complete.
type LiveStats struct {
	leftover   []byte
	Events     map[string]int
	Order      []string
	Model      string
	InTokens   int
	OutTokens  int
	CacheRead  int
	CacheWrite int
	StopReason string
}

// NewLiveStats returns a ready-to-feed LiveStats.
func NewLiveStats() *LiveStats {
	return &LiveStats{Events: map[string]int{}}
}

// Feed consumes a chunk of the SSE stream, updating counts and usage.
func (s *LiveStats) Feed(b []byte) {
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

func (s *LiveStats) handleLine(line []byte) {
	const prefix = "data: "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	data := bytes.TrimPrefix(line, []byte(prefix))
	var ev sseSummaryEvent
	if json.Unmarshal(data, &ev) != nil || ev.Type == "" {
		return
	}
	if _, seen := s.Events[ev.Type]; !seen {
		s.Order = append(s.Order, ev.Type)
	}
	s.Events[ev.Type]++

	switch ev.Type {
	case "message_start":
		s.Model = ev.Message.Model
		s.InTokens = ev.Message.Usage.InputTokens
		s.CacheRead = ev.Message.Usage.CacheReadInputTokens
		s.CacheWrite = ev.Message.Usage.CacheCreationInputTokens
	case "message_delta":
		if ev.Usage != nil {
			s.OutTokens = ev.Usage.OutputTokens
		}
		if ev.Delta.StopReason != "" {
			s.StopReason = ev.Delta.StopReason
		}
	}
}

// ---------------------------------------------------------------------------
// SSE reassembly — turns a complete recorded event stream back into the message
// the model actually sent.
// ---------------------------------------------------------------------------

type sseContentEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Model string `json:"model"`
		Usage Usage  `json:"usage"`
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
	Usage *Usage `json:"usage"`
}

type asmBlock struct {
	typ  string
	name string
	id   string
	buf  bytes.Buffer
}

func reassembleSSE(body []byte) ParsedResponse {
	var (
		pr     ParsedResponse
		order  []int
		blocks = map[int]*asmBlock{}
	)
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var ev sseContentEvent
		if json.Unmarshal(line[len("data: "):], &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			pr.Model = ev.Message.Model
			pr.Usage.InputTokens = ev.Message.Usage.InputTokens
			pr.Usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
			pr.Usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
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
				pr.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				pr.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		}
	}

	for _, idx := range order {
		b := blocks[idx]
		cb := ContentBlock{Type: b.typ, Name: b.name, ID: b.id}
		switch b.typ {
		case "text":
			cb.Text = b.buf.String()
		case "thinking":
			cb.Thinking = b.buf.String()
		case "tool_use":
			cb.Input = json.RawMessage(b.buf.String())
		}
		pr.Content = append(pr.Content, cb)
	}
	pr.OK = len(pr.Content) > 0 || pr.Model != ""
	return pr
}
