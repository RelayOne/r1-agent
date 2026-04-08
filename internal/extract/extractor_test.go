package extract

import (
	"testing"
)

func TestExtractCode(t *testing.T) {
	text := "Here's the code:\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\nDone."

	blocks := ExtractCode(text)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 code block, got %d", len(blocks))
	}
	if blocks[0].Language != "go" {
		t.Errorf("expected go, got %s", blocks[0].Language)
	}
	if blocks[0].Content == "" {
		t.Error("content should not be empty")
	}
}

func TestExtractMultipleCodeBlocks(t *testing.T) {
	text := "```python\nprint('a')\n```\n\nThen:\n\n```javascript\nconsole.log('b')\n```"

	blocks := ExtractCode(text)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Language != "python" {
		t.Errorf("first should be python, got %s", blocks[0].Language)
	}
	if blocks[1].Language != "javascript" {
		t.Errorf("second should be javascript, got %s", blocks[1].Language)
	}
}

func TestExtractCodeByLang(t *testing.T) {
	text := "```go\nfunc A() {}\n```\n```python\ndef b(): pass\n```\n```go\nfunc C() {}\n```"

	goBlocks := ExtractCodeByLang(text, "go")
	if len(goBlocks) != 2 {
		t.Errorf("expected 2 Go blocks, got %d", len(goBlocks))
	}

	pyBlocks := ExtractCodeByLang(text, "python")
	if len(pyBlocks) != 1 {
		t.Errorf("expected 1 Python block, got %d", len(pyBlocks))
	}
}

func TestExtractFirstCode(t *testing.T) {
	text := "```go\nfirst\n```\n```go\nsecond\n```"
	first := ExtractFirstCode(text)
	if first != "first" {
		t.Errorf("expected 'first', got %q", first)
	}
}

func TestExtractFirstCodeEmpty(t *testing.T) {
	if ExtractFirstCode("no code here") != "" {
		t.Error("should return empty for no code blocks")
	}
}

func TestExtractJSON(t *testing.T) {
	text := `Here's the result: {"name": "test", "value": 42}`

	results := ExtractJSON(text)
	if len(results) != 1 {
		t.Fatalf("expected 1 JSON object, got %d", len(results))
	}
	if results[0]["name"] != "test" {
		t.Error("should parse name field")
	}
}

func TestExtractJSONFromCodeBlock(t *testing.T) {
	text := "```json\n{\"key\": \"value\"}\n```"

	results := ExtractJSON(text)
	if len(results) != 1 {
		t.Fatalf("expected 1 JSON object, got %d", len(results))
	}
	if results[0]["key"] != "value" {
		t.Error("should parse key field")
	}
}

func TestExtractFirstJSON(t *testing.T) {
	text := `First: {"a": 1} then {"b": 2}`
	obj := ExtractFirstJSON(text)
	if obj == nil {
		t.Fatal("should find JSON")
	}
}

func TestExtractFirstJSONNone(t *testing.T) {
	if ExtractFirstJSON("no json here") != nil {
		t.Error("should return nil for no JSON")
	}
}

func TestExtractToolCalls(t *testing.T) {
	text := `I'll use the tool:
{"name": "read_file", "arguments": {"path": "/tmp/test.go"}}
`

	calls := ExtractToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Errorf("expected read_file, got %s", calls[0].Name)
	}
	if calls[0].Arguments["path"] != "/tmp/test.go" {
		t.Error("should parse path argument")
	}
}

func TestExtractToolCallsWithInput(t *testing.T) {
	text := `{"name": "edit", "input": {"file": "main.go", "content": "new"}}`

	calls := ExtractToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Arguments["file"] != "main.go" {
		t.Error("should parse input as arguments")
	}
}

func TestExtractEdits(t *testing.T) {
	text := `I'll make this change:

main.go
<<<<<<< SEARCH
func old() {
}
=======
func new() {
}
>>>>>>> REPLACE
`

	edits := ExtractEdits(text)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].File != "main.go" {
		t.Errorf("expected main.go, got %q", edits[0].File)
	}
	if edits[0].Search != "func old() {\n}" {
		t.Errorf("unexpected search: %q", edits[0].Search)
	}
	if edits[0].Replace != "func new() {\n}" {
		t.Errorf("unexpected replace: %q", edits[0].Replace)
	}
}

func TestExtractThinking(t *testing.T) {
	text := `<thinking>
Let me analyze this problem.
The issue is in the parsing logic.
</thinking>

Here's my answer.`

	thinking := ExtractThinking(text)
	if len(thinking) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(thinking))
	}
	if thinking[0] == "" {
		t.Error("thinking content should not be empty")
	}
}

func TestExtractAll(t *testing.T) {
	text := `<thinking>analyzing</thinking>

Here's the fix:

` + "```go\nfunc fix() {}\n```"

	blocks := ExtractAll(text)
	if len(blocks) < 2 {
		t.Errorf("expected at least 2 blocks, got %d", len(blocks))
	}

	hasThinking := false
	hasCode := false
	for _, b := range blocks {
		if b.Type == BlockThinking {
			hasThinking = true
		}
		if b.Type == BlockCode {
			hasCode = true
		}
	}
	if !hasThinking {
		t.Error("should extract thinking block")
	}
	if !hasCode {
		t.Error("should extract code block")
	}
}

func TestStripCode(t *testing.T) {
	text := "Before\n```go\ncode\n```\nAfter"
	stripped := StripCode(text)
	if stripped != "Before\n\nAfter" {
		t.Errorf("unexpected stripped: %q", stripped)
	}
}

func TestStripThinking(t *testing.T) {
	text := "<thinking>internal</thinking>\nVisible text"
	stripped := StripCode(text)
	if stripped != "Visible text" {
		t.Errorf("unexpected stripped: %q", stripped)
	}
}

func TestSplitResponse(t *testing.T) {
	text := "Here's the code:\n```go\nfunc main() {}\n```\nDone."
	prose, code := SplitResponse(text)

	if len(code) != 1 {
		t.Errorf("expected 1 code block, got %d", len(code))
	}
	if prose == "" {
		t.Error("prose should not be empty")
	}
}

func TestNoBlocks(t *testing.T) {
	text := "Just plain text with no special formatting."
	blocks := ExtractAll(text)
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestExtractCodeNoLanguage(t *testing.T) {
	text := "```\nplain code\n```"
	blocks := ExtractCode(text)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Language != "" {
		t.Errorf("expected empty language, got %q", blocks[0].Language)
	}
}
