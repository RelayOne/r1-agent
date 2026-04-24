// Package replay implements session replay for debugging and learning.
// Inspired by claw-code's session recording and OpenHands' trajectory logging:
//
// Recording full session trajectories enables:
// - Post-mortem debugging (what went wrong and when)
// - Learning from successful sessions (extract patterns)
// - Regression detection (did the agent get worse at X?)
// - Training data collection for fine-tuning
//
// Sessions are recorded as a sequence of timestamped events that can be
// replayed step-by-step or fast-forwarded to specific points.
package replay

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// EventType classifies replay events.
type EventType string

const (
	EventMessage   EventType = "message"    // conversation turn
	EventToolCall  EventType = "tool_call"  // tool invocation
	EventToolResult EventType = "tool_result" // tool output
	EventDecision  EventType = "decision"   // agent decision point
	EventError     EventType = "error"      // error occurrence
	EventPhase     EventType = "phase"      // phase transition
	EventMetric    EventType = "metric"     // performance metric
)

// Event is a single recorded event.
type Event struct {
	Seq       int            `json:"seq"`
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Elapsed   time.Duration  `json:"elapsed"` // since session start
	Data      map[string]any `json:"data"`
}

// Recording is a complete session recording.
type Recording struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id,omitempty"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Events    []Event   `json:"events"`
	Outcome   string    `json:"outcome,omitempty"` // "success", "failure", "timeout"
	Tags      []string  `json:"tags,omitempty"`
}

// Recorder captures session events.
type Recorder struct {
	mu        sync.Mutex
	recording Recording
	seq       int
}

// NewRecorder starts recording a new session.
func NewRecorder(id, taskID string) *Recorder {
	return &Recorder{
		recording: Recording{
			ID:        id,
			TaskID:    taskID,
			StartTime: time.Now(),
		},
	}
}

// Record adds an event to the recording.
func (r *Recorder) Record(eventType EventType, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	e := Event{
		Seq:       r.seq,
		Type:      eventType,
		Timestamp: time.Now(),
		Elapsed:   time.Since(r.recording.StartTime),
		Data:      data,
	}
	r.recording.Events = append(r.recording.Events, e)
}

// RecordMessage records a conversation message.
func (r *Recorder) RecordMessage(role, content string) {
	r.Record(EventMessage, map[string]any{
		"role":    role,
		"content": content,
	})
}

// RecordToolCall records a tool invocation.
func (r *Recorder) RecordToolCall(tool string, args map[string]any) {
	r.Record(EventToolCall, map[string]any{
		"tool": tool,
		"args": args,
	})
}

// RecordError records an error.
func (r *Recorder) RecordError(err string, context map[string]any) {
	data := map[string]any{"error": err}
	for k, v := range context {
		data[k] = v
	}
	r.Record(EventError, data)
}

// Finish marks the recording as complete.
func (r *Recorder) Finish(outcome string, tags ...string) *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.recording.EndTime = time.Now()
	r.recording.Outcome = outcome
	r.recording.Tags = tags

	rec := r.recording
	return &rec
}

// Save writes a recording to a file.
func Save(rec *Recording, path string) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads a recording from a file.
func Load(path string) (*Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var rec Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &rec, nil
}

// Player replays a recording.
type Player struct {
	recording *Recording
	position  int
}

// NewPlayer creates a player for a recording.
func NewPlayer(rec *Recording) *Player {
	return &Player{recording: rec}
}

// Next advances to the next event. Returns nil at the end.
func (p *Player) Next() *Event {
	if p.position >= len(p.recording.Events) {
		return nil
	}
	e := &p.recording.Events[p.position]
	p.position++
	return e
}

// Peek returns the next event without advancing.
func (p *Player) Peek() *Event {
	if p.position >= len(p.recording.Events) {
		return nil
	}
	return &p.recording.Events[p.position]
}

// Seek jumps to a specific event sequence number.
func (p *Player) Seek(seq int) bool {
	for i, e := range p.recording.Events {
		if e.Seq == seq {
			p.position = i
			return true
		}
	}
	return false
}

// SeekToType jumps to the next event of the given type.
func (p *Player) SeekToType(eventType EventType) *Event {
	for p.position < len(p.recording.Events) {
		e := &p.recording.Events[p.position]
		p.position++
		if e.Type == eventType {
			return e
		}
	}
	return nil
}

// Reset returns to the beginning.
func (p *Player) Reset() {
	p.position = 0
}

// Remaining returns the number of events left.
func (p *Player) Remaining() int {
	return len(p.recording.Events) - p.position
}

// Duration returns the total recording duration.
func (rec *Recording) Duration() time.Duration {
	if rec.EndTime.IsZero() {
		if len(rec.Events) > 0 {
			return rec.Events[len(rec.Events)-1].Elapsed
		}
		return 0
	}
	return rec.EndTime.Sub(rec.StartTime)
}

// EventCounts returns counts by event type.
func (rec *Recording) EventCounts() map[EventType]int {
	counts := make(map[EventType]int)
	for _, e := range rec.Events {
		counts[e.Type]++
	}
	return counts
}

// Errors extracts all error events.
func (rec *Recording) Errors() []Event {
	var errors []Event
	for _, e := range rec.Events {
		if e.Type == EventError {
			errors = append(errors, e)
		}
	}
	return errors
}

// Summary produces a human-readable description.
func (rec *Recording) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s", rec.ID)
	if rec.TaskID != "" {
		fmt.Fprintf(&b, " (task: %s)", rec.TaskID)
	}
	fmt.Fprintf(&b, "\nOutcome: %s, Duration: %s, Events: %d\n",
		rec.Outcome, rec.Duration().Round(time.Millisecond), len(rec.Events))

	counts := rec.EventCounts()
	for et, c := range counts {
		fmt.Fprintf(&b, "  %s: %d\n", et, c)
	}
	return b.String()
}
