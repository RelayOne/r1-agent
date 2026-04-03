package stream

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SSEParser parses Server-Sent Events (SSE) streams from Anthropic's Messages API.
// Inspired by claw-code-parity's Rust SSE parser. Unlike NDJSON (Claude Code CLI output),
// SSE uses "event:" + "data:" line pairs separated by blank lines.
// This enables direct Anthropic API streaming without requiring Claude Code CLI.
type SSEParser struct {
	buffer []byte
}

// NewSSEParser creates an SSE parser.
func NewSSEParser() *SSEParser {
	return &SSEParser{}
}

// SSEFrame holds a parsed SSE frame with event type and data payload.
type SSEFrame struct {
	Event string
	Data  string
}

// Push feeds new bytes into the parser and returns any complete SSE events.
// Frames are delimited by double newlines (\n\n or \r\n\r\n).
func (p *SSEParser) Push(chunk []byte) ([]Event, error) {
	p.buffer = append(p.buffer, chunk...)
	var events []Event

	for {
		frame, ok := p.nextFrame()
		if !ok {
			break
		}
		ev, err := p.parseFrame(frame)
		if err != nil {
			return events, err
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	return events, nil
}

// Finish processes any remaining buffered data.
func (p *SSEParser) Finish() ([]Event, error) {
	if len(p.buffer) == 0 {
		return nil, nil
	}
	trailing := string(p.buffer)
	p.buffer = nil
	ev, err := p.parseFrame(trailing)
	if err != nil {
		return nil, err
	}
	if ev != nil {
		return []Event{*ev}, nil
	}
	return nil, nil
}

// nextFrame extracts the next complete SSE frame from the buffer.
func (p *SSEParser) nextFrame() (string, bool) {
	s := string(p.buffer)

	// Look for \n\n or \r\n\r\n separator
	idx := strings.Index(s, "\n\n")
	sepLen := 2
	if idx < 0 {
		idx = strings.Index(s, "\r\n\r\n")
		sepLen = 4
	}
	if idx < 0 {
		return "", false
	}

	frame := s[:idx]
	p.buffer = []byte(s[idx+sepLen:])
	return frame, true
}

// parseFrame converts a raw SSE frame into a stream Event.
// Returns nil for ping events and [DONE] signals.
func (p *SSEParser) parseFrame(frame string) (*Event, error) {
	trimmed := strings.TrimSpace(frame)
	if trimmed == "" {
		return nil, nil
	}

	var dataLines []string
	var eventName string

	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimRight(line, "\r")
		// Comment lines
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
		}
	}

	// Skip pings
	if eventName == "ping" {
		return nil, nil
	}

	if len(dataLines) == 0 {
		return nil, nil
	}

	payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if payload == "[DONE]" {
		return nil, nil
	}

	return p.sseEventToStreamEvent(eventName, payload)
}

// sseEventToStreamEvent maps Anthropic SSE event types to Stoke stream events.
// Event types from Anthropic Messages API:
//   - message_start: initial message metadata
//   - content_block_start: new content block (text or tool_use)
//   - content_block_delta: incremental content update
//   - content_block_stop: content block finished
//   - message_delta: stop reason, usage update
//   - message_stop: final message signal
//   - error: API error
func (p *SSEParser) sseEventToStreamEvent(eventName, payload string) (*Event, error) {
	switch eventName {
	case "message_start":
		return p.parseMessageStart(payload)
	case "content_block_start":
		return p.parseContentBlockStart(payload)
	case "content_block_delta":
		return p.parseContentBlockDelta(payload)
	case "content_block_stop":
		return nil, nil // no useful data
	case "message_delta":
		return p.parseMessageDelta(payload)
	case "message_stop":
		return nil, nil // terminal signal, no data
	case "error":
		return p.parseError(payload)
	default:
		// Unknown event type — pass through as stream_event
		return &Event{
			Type:    "stream_event",
			Subtype: eventName,
			Raw:     []byte(payload),
		}, nil
	}
}

func (p *SSEParser) parseMessageStart(payload string) (*Event, error) {
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return nil, fmt.Errorf("parse message_start: %w", err)
	}
	return &Event{
		Type:      "assistant",
		SessionID: msg.Message.ID,
		Tokens: TokenUsage{
			Input:         msg.Message.Usage.InputTokens,
			Output:        msg.Message.Usage.OutputTokens,
			CacheCreation: msg.Message.Usage.CacheCreationInputTokens,
			CacheRead:     msg.Message.Usage.CacheReadInputTokens,
		},
		Raw: []byte(payload),
	}, nil
}

func (p *SSEParser) parseContentBlockStart(payload string) (*Event, error) {
	var block struct {
		ContentBlock struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(payload), &block); err != nil {
		return nil, fmt.Errorf("parse content_block_start: %w", err)
	}

	ev := &Event{Type: "assistant", Raw: []byte(payload)}
	if block.ContentBlock.Type == "tool_use" {
		var input map[string]interface{}
		if block.ContentBlock.Input != nil {
			json.Unmarshal(block.ContentBlock.Input, &input)
		}
		ev.ToolUses = append(ev.ToolUses, ToolUse{
			ID:    block.ContentBlock.ID,
			Name:  block.ContentBlock.Name,
			Input: input,
		})
	}
	return ev, nil
}

func (p *SSEParser) parseContentBlockDelta(payload string) (*Event, error) {
	var delta struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// For tool_use input streaming
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(payload), &delta); err != nil {
		return nil, fmt.Errorf("parse content_block_delta: %w", err)
	}

	ev := &Event{Type: "stream_event", Raw: []byte(payload)}
	switch delta.Delta.Type {
	case "text_delta":
		ev.DeltaType = "text_delta"
		ev.DeltaText = delta.Delta.Text
	case "input_json_delta":
		ev.DeltaType = "input_json_delta"
		ev.DeltaText = delta.Delta.PartialJSON
	}
	return ev, nil
}

func (p *SSEParser) parseMessageDelta(payload string) (*Event, error) {
	var delta struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &delta); err != nil {
		return nil, fmt.Errorf("parse message_delta: %w", err)
	}

	return &Event{
		Type:       "result",
		StopReason: delta.Delta.StopReason,
		Tokens:     TokenUsage{Output: delta.Usage.OutputTokens},
		Raw:        []byte(payload),
	}, nil
}

func (p *SSEParser) parseError(payload string) (*Event, error) {
	var errMsg struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &errMsg); err != nil {
		return &Event{Type: "error", IsError: true, ResultText: payload, Raw: []byte(payload)}, nil
	}

	ev := &Event{
		Type:       "error",
		IsError:    true,
		Subtype:    errMsg.Error.Type,
		ResultText: errMsg.Error.Message,
		Raw:        []byte(payload),
	}
	if errMsg.Error.Type == "rate_limit_error" || errMsg.Error.Type == "overloaded_error" {
		ev.Subtype = "rate_limited"
	}
	return ev, nil
}
