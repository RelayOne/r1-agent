package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Event is a parsed event from a Claude Code or Codex CLI NDJSON stream.
type Event struct {
	Type      string     `json:"type"`
	Subtype   string     `json:"subtype,omitempty"`
	SessionID string     `json:"session_id,omitempty"`
	IsError   bool       `json:"is_error,omitempty"`
	Raw       []byte     `json:"-"`
	ToolUses  []ToolUse  `json:"-"`
	ToolResults []ToolResult `json:"-"`
	CostUSD    float64   `json:"-"`
	Tokens     TokenUsage `json:"-"`
	DurationMs int64     `json:"-"`
	NumTurns   int       `json:"-"`
	StopReason string    `json:"-"`
	ResultText string    `json:"-"`
	DeltaText  string    `json:"-"`
	DeltaType  string    `json:"-"`
}

// ToolUse represents a single tool invocation extracted from an assistant message.
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

// ToolResult represents the outcome of a tool execution returned in a user message.
type ToolResult struct {
	ToolUseID  string
	Content    string
	IsError    bool
	DurationMs int64
}

// TokenUsage tracks input, output, and cache token counts for a single engine execution.
type TokenUsage struct {
	Input              int `json:"input_tokens"`
	Output             int `json:"output_tokens"`
	CacheCreation      int `json:"cache_creation_input_tokens"`
	CacheRead          int `json:"cache_read_input_tokens"`
}

// Parser reads NDJSON from a claude -p subprocess and emits parsed events.
type Parser struct {
	StreamIdleTimeout time.Duration
	PostResultTimeout time.Duration
	GlobalTimeout     time.Duration
}

// DefaultParser returns production-safe timeouts.
func DefaultParser() *Parser {
	return &Parser{
		StreamIdleTimeout: 90 * time.Second,
		PostResultTimeout: 30 * time.Second,
		GlobalTimeout:     30 * time.Minute,
	}
}

// Parse reads NDJSON from r and sends events to the returned channel.
// Closes the channel when done. The done channel closes when parsing completes.
func (p *Parser) Parse(r io.Reader, done chan<- struct{}) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer close(done)

		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)

		idle := time.NewTimer(p.StreamIdleTimeout)
		defer idle.Stop()
		global := time.NewTimer(p.GlobalTimeout)
		defer global.Stop()

		resultSeen := false
		var postResult *time.Timer
		defer func() {
			if postResult != nil {
				postResult.Stop()
			}
		}()

		lines := make(chan string, 16)
		scanDone := make(chan error, 1)
		go func() {
			for scanner.Scan() { lines <- scanner.Text() }
			scanDone <- scanner.Err()
		}()

		for {
			select {
			case line, ok := <-lines:
				if !ok { return }
				resetTimer(idle, p.StreamIdleTimeout)
				line = strings.TrimSpace(line)
				if line == "" || line[0] != '{' { continue }
				if !json.Valid([]byte(line)) { continue }
				ev := parseLine([]byte(line))
				ch <- ev
				if ev.Type == "result" {
					resultSeen = true
					postResult = time.NewTimer(p.PostResultTimeout)
				}
			case err := <-scanDone:
				// Drain any remaining buffered lines before returning
				for {
					select {
					case line := <-lines:
						line = strings.TrimSpace(line)
						if line == "" || line[0] != '{' { continue }
						if !json.Valid([]byte(line)) { continue }
						ev := parseLine([]byte(line))
						ch <- ev
						if ev.Type == "result" { resultSeen = true }
					default:
						goto drained
					}
				}
			drained:
				if err != nil && err != io.EOF {
					ch <- Event{Type: "error", IsError: true, ResultText: fmt.Sprintf("read: %v", err)}
				}
				return
			case <-idle.C:
				ch <- Event{Type: "error", Subtype: "stream_idle_timeout", IsError: true}
				return
			case <-global.C:
				ch <- Event{Type: "error", Subtype: "global_timeout", IsError: true}
				return
			}
			if resultSeen && postResult != nil {
				select {
				case <-postResult.C: return
				default:
				}
			}
		}
	}()
	return ch
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select { case <-t.C: default: }
	}
	t.Reset(d)
}

// --- JSON structures ---

type rawEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	Result    string          `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	TotalCost float64         `json:"total_cost_usd,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	NumTurns   int            `json:"num_turns,omitempty"`
}

type rawMessage struct {
	Content    []rawContent `json:"content,omitempty"`
	StopReason string       `json:"stop_reason,omitempty"`
	Usage      *TokenUsage  `json:"usage,omitempty"`
}

type rawContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content2  string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type rawStreamEvent struct {
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"delta,omitempty"`
}

func parseLine(data []byte) Event {
	var raw rawEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		return Event{Type: "unknown", Raw: data}
	}
	ev := Event{
		Type: raw.Type, Subtype: raw.Subtype, SessionID: raw.SessionID,
		IsError: raw.IsError, Raw: data,
	}
	switch raw.Type {
	case "assistant":
		if raw.Message != nil {
			var msg rawMessage
			if json.Unmarshal(raw.Message, &msg) == nil {
				ev.StopReason = msg.StopReason
				if msg.Usage != nil { ev.Tokens = *msg.Usage }
				for _, c := range msg.Content {
					switch c.Type {
					case "text": ev.DeltaText += c.Text
					case "tool_use":
						var input map[string]interface{}
						json.Unmarshal(c.Input, &input)
						ev.ToolUses = append(ev.ToolUses, ToolUse{ID: c.ID, Name: c.Name, Input: input})
					}
				}
			}
		}
	case "user":
		if raw.Message != nil {
			var msg rawMessage
			if json.Unmarshal(raw.Message, &msg) == nil {
				for _, c := range msg.Content {
					if c.Type == "tool_result" {
						ev.ToolResults = append(ev.ToolResults, ToolResult{
							ToolUseID: c.ToolUseID, Content: c.Content2, IsError: c.IsError,
						})
					}
				}
			}
		}
	case "result":
		ev.CostUSD = raw.TotalCost
		ev.DurationMs = raw.DurationMs
		ev.NumTurns = raw.NumTurns
		ev.ResultText = raw.Result
		if ev.ResultText == "" && raw.Error != "" { ev.ResultText = raw.Error; ev.IsError = true }
		if raw.Usage != nil {
			var u TokenUsage
			if json.Unmarshal(raw.Usage, &u) == nil { ev.Tokens = u }
		}
	case "stream_event":
		if raw.Event != nil {
			var se rawStreamEvent
			if json.Unmarshal(raw.Event, &se) == nil && se.Delta != nil {
				ev.DeltaType = se.Delta.Type
				ev.DeltaText = se.Delta.Text
			}
		}
	case "rate_limit_event":
		ev.IsError = true
		ev.Subtype = "rate_limited"
	}
	return ev
}
