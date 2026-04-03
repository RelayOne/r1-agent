package tokenest

import (
	"strings"
	"testing"
)

func TestEstimateEnglish(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog"
	est := Estimate(text, ContentEnglish)
	// ~44 chars / 4.0 ≈ 11 tokens
	if est < 8 || est > 15 {
		t.Errorf("expected ~11 tokens, got %d", est)
	}
}

func TestEstimateCode(t *testing.T) {
	code := `func main() {
	fmt.Println("hello")
}`
	est := Estimate(code, ContentCode)
	if est < 5 || est > 20 {
		t.Errorf("expected reasonable estimate, got %d", est)
	}
}

func TestEstimateEmpty(t *testing.T) {
	if Estimate("", ContentEnglish) != 0 {
		t.Error("empty string should be 0 tokens")
	}
}

func TestEstimateMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello, how are you?"},
	}
	est := EstimateMessages(msgs)
	// 3 base + 2*4 overhead + content tokens
	if est < 15 {
		t.Errorf("expected at least 15 tokens, got %d", est)
	}
}

func TestBudgetBasic(t *testing.T) {
	b := NewBudget("claude-opus-4", 4000)
	if b.Limit != 200000 {
		t.Errorf("expected 200000, got %d", b.Limit)
	}
	if b.Available() != 196000 {
		t.Errorf("expected 196000 available, got %d", b.Available())
	}
}

func TestBudgetAdd(t *testing.T) {
	b := Budget{Limit: 100, Reserved: 20, Used: 0}

	ok := b.Add(50)
	if !ok || b.Used != 50 {
		t.Error("should accept 50 tokens")
	}

	ok = b.Add(40) // 50+40+20 = 110 > 100
	if ok {
		t.Error("should reject: would exceed budget")
	}

	ok = b.Add(30) // 50+30+20 = 100 = limit
	if !ok {
		t.Error("should accept: exactly at limit")
	}
}

func TestBudgetWouldFit(t *testing.T) {
	b := Budget{Limit: 100, Reserved: 20, Used: 70}
	// Available: 100 - 70 - 20 = 10

	if b.WouldFit(strings.Repeat("x", 100)) {
		t.Error("100 chars should not fit in 10 tokens")
	}
	if !b.WouldFit("hi") {
		t.Error("2 chars should fit")
	}
}

func TestBudgetUtilization(t *testing.T) {
	b := Budget{Limit: 100, Used: 75}
	u := b.Utilization()
	if u != 0.75 {
		t.Errorf("expected 0.75, got %f", u)
	}
}

func TestUnknownModel(t *testing.T) {
	b := NewBudget("unknown-model", 1000)
	if b.Limit != 128000 {
		t.Errorf("expected default 128000, got %d", b.Limit)
	}
}

func TestTruncateToFit(t *testing.T) {
	text := strings.Repeat("word ", 1000) // ~5000 chars
	truncated, did := TruncateToFit(text, 50, ContentEnglish)
	if !did {
		t.Error("should have truncated")
	}
	if len(truncated) >= len(text) {
		t.Error("truncated should be shorter")
	}
	if !strings.HasSuffix(truncated, "... [truncated]") {
		t.Error("should have truncation marker")
	}
}

func TestTruncateNoOp(t *testing.T) {
	text := "short"
	truncated, did := TruncateToFit(text, 1000, ContentEnglish)
	if did {
		t.Error("should not truncate short text")
	}
	if truncated != text {
		t.Error("should return original text")
	}
}

func TestDetectContentType(t *testing.T) {
	code := `func main() {
	x := 10
	fmt.Println(x)
}
func helper() {
	y := 20
}`
	if DetectContentType(code) != ContentCode {
		t.Errorf("expected Code, got %d", DetectContentType(code))
	}

	english := "This is a simple English sentence about programming concepts and software design patterns."
	if DetectContentType(english) != ContentEnglish {
		t.Errorf("expected English, got %d", DetectContentType(english))
	}

	json := `{"key": "value", "nested": {"a": 1}}`
	if DetectContentType(json) != ContentJSON {
		t.Errorf("expected JSON, got %d", DetectContentType(json))
	}
}

func TestDetectEmpty(t *testing.T) {
	if DetectContentType("") != ContentEnglish {
		t.Error("empty should default to English")
	}
}
