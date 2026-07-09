package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/ctxcompress"
)

// history builds a synthetic conversation with a task brief, several middle
// tool_use/tool_result pairs (some with large payloads), and a recent tail.
func history() []agentloop.Message {
	big := strings.Repeat("x", 1000)
	return []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "TASK BRIEF: build the thing"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t1", Name: "read_file"}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: big}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t2", Name: "read_file"}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t2", Content: big}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t3", Name: "bash"}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t3", Content: big}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "recent-1"}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "recent-2"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "recent-3"}}},
	}
}

func countBlocks(msgs []agentloop.Message) (toolUse, toolResult int) {
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				toolUse++
			case "tool_result":
				toolResult++
			}
		}
	}
	return
}

func TestPolicyCompactor_PreservesPairingAndDeterministic(t *testing.T) {
	ctxcompress.SetCompressor(ctxcompress.NewPolicyCompressor(ctxcompress.Policy{MinBytes: 200, DropRedundant: true}))
	defer ctxcompress.ResetCompressor()

	compact := buildPolicyCompactor(3, 200, condensedSentinel)
	in := history()

	out1 := compact(in, 0)
	out2 := compact(in, 0)

	// Deterministic: identical output across calls.
	if !reflect.DeepEqual(out1, out2) {
		t.Fatal("policy compactor must be deterministic")
	}
	// Message count preserved.
	if len(out1) != len(in) {
		t.Fatalf("message count changed: %d -> %d", len(in), len(out1))
	}
	// Tool pairing preserved (same tool_use / tool_result counts).
	iu, ir := countBlocks(in)
	ou, or := countBlocks(out1)
	if iu != ou || ir != or {
		t.Fatalf("pairing changed: use %d->%d result %d->%d", iu, ou, ir, or)
	}
	// The big middle tool_results were compressed to pointer markers.
	if !strings.Contains(out1[2].Content[0].Content, "truncated") {
		t.Fatalf("middle tool_result should be compressed, got: %q", out1[2].Content[0].Content)
	}
	// Task brief (first message) preserved verbatim.
	if out1[0].Content[0].Text != "TASK BRIEF: build the thing" {
		t.Fatal("task brief must be preserved verbatim")
	}
	// Recent tail preserved verbatim.
	if out1[9].Content[0].Text != "recent-3" {
		t.Fatal("recent tail must be preserved verbatim")
	}
}

func TestPolicyCompactor_Idempotent(t *testing.T) {
	ctxcompress.SetCompressor(ctxcompress.NewPolicyCompressor(ctxcompress.Policy{MinBytes: 200}))
	defer ctxcompress.ResetCompressor()

	compact := buildPolicyCompactor(3, 200, condensedSentinel)
	once := compact(history(), 0)
	twice := compact(once, 0)
	if !reflect.DeepEqual(once, twice) {
		t.Fatal("compaction must be idempotent on an already-compacted history")
	}
}

// noopCompressor selects nothing.
type noopCompressor struct{}

func (noopCompressor) Compress([]ctxcompress.Item) []int { return nil }
func (noopCompressor) Backend() string                   { return "noop" }

// TestPolicyCompactor_SwapProof: the SAME compactor call site routes through
// whatever compressor is registered — a no-op backend leaves history untouched.
func TestPolicyCompactor_SwapProof(t *testing.T) {
	defer ctxcompress.ResetCompressor()
	compact := buildPolicyCompactor(3, 200, condensedSentinel)
	in := history()

	ctxcompress.SetCompressor(noopCompressor{})
	out := compact(in, 0)
	iu, ir := countBlocks(in)
	ou, or := countBlocks(out)
	if iu != ou || ir != or {
		t.Fatal("no-op compressor must not change pairing")
	}
	if out[2].Content[0].Content != in[2].Content[0].Content {
		t.Fatal("no-op compressor must leave tool_result content untouched")
	}
}
