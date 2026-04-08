package ctxpack

import (
	"testing"
)

func TestPackBasic(t *testing.T) {
	items := []Item{
		{ID: "sys", Category: "system", Content: "system prompt", Tokens: 100, Required: true},
		{ID: "f1", Category: "file", Content: "main.go", Tokens: 500, Relevance: 0.9},
		{ID: "f2", Category: "file", Content: "utils.go", Tokens: 300, Relevance: 0.5},
		{ID: "f3", Category: "file", Content: "test.go", Tokens: 400, Relevance: 0.3},
	}

	result := Pack(items, Config{MaxTokens: 1000, ReserveResponse: 100})

	if result.TotalTokens > 900 {
		t.Errorf("exceeded budget: %d", result.TotalTokens)
	}

	// Required item should always be included
	hasRequired := false
	for _, item := range result.Included {
		if item.ID == "sys" {
			hasRequired = true
		}
	}
	if !hasRequired {
		t.Error("required item should be included")
	}

	// Most relevant non-required should be f1
	if len(result.Included) < 2 || result.Included[1].ID != "f1" {
		t.Error("f1 (highest relevance) should be included")
	}
}

func TestPackRespectsRelevanceOrder(t *testing.T) {
	items := []Item{
		{ID: "low", Tokens: 100, Relevance: 0.1},
		{ID: "high", Tokens: 100, Relevance: 0.9},
		{ID: "mid", Tokens: 100, Relevance: 0.5},
	}

	result := Pack(items, Config{MaxTokens: 250, ReserveResponse: 0})

	// Should include high and mid, exclude low
	included := make(map[string]bool)
	for _, item := range result.Included {
		included[item.ID] = true
	}
	if !included["high"] {
		t.Error("high relevance should be included")
	}
	if included["low"] && !included["mid"] {
		t.Error("mid should be preferred over low")
	}
}

func TestPackMinRelevance(t *testing.T) {
	items := []Item{
		{ID: "good", Tokens: 100, Relevance: 0.5},
		{ID: "bad", Tokens: 100, Relevance: 0.05},
	}

	result := Pack(items, Config{MaxTokens: 1000, MinRelevance: 0.1})
	for _, item := range result.Included {
		if item.ID == "bad" {
			t.Error("low relevance item should be excluded")
		}
	}
}

func TestPackPinnedItems(t *testing.T) {
	items := []Item{
		{ID: "pinned", Tokens: 100, Relevance: 0.1, Pinned: true},
		{ID: "better", Tokens: 100, Relevance: 0.9},
	}

	result := Pack(items, Config{MaxTokens: 150, ReserveResponse: 0})

	included := make(map[string]bool)
	for _, item := range result.Included {
		included[item.ID] = true
	}
	if !included["pinned"] {
		t.Error("pinned item should be included despite low relevance")
	}
}

func TestPackUtilization(t *testing.T) {
	items := []Item{
		{ID: "a", Tokens: 500, Relevance: 1.0},
	}

	result := Pack(items, Config{MaxTokens: 1000, ReserveResponse: 0})
	if result.Utilization < 0.49 || result.Utilization > 0.51 {
		t.Errorf("expected ~0.5 utilization, got %f", result.Utilization)
	}
}

func TestPackWithCategories(t *testing.T) {
	items := []Item{
		{ID: "f1", Category: "file", Tokens: 300, Relevance: 0.9},
		{ID: "f2", Category: "file", Tokens: 300, Relevance: 0.8},
		{ID: "h1", Category: "history", Tokens: 200, Relevance: 0.7},
	}

	result := PackWithCategories(items, Config{MaxTokens: 1000}, map[string]int{
		"file": 400, // only 400 tokens for files
	})

	fileTokens := 0
	for _, item := range result.Included {
		if item.Category == "file" {
			fileTokens += item.Tokens
		}
	}
	if fileTokens > 400 {
		t.Errorf("file tokens should be <= 400, got %d", fileTokens)
	}
}

func TestPackEmpty(t *testing.T) {
	result := Pack(nil, Config{MaxTokens: 1000})
	if len(result.Included) != 0 {
		t.Error("empty input should produce empty output")
	}
}

func TestPackZeroBudget(t *testing.T) {
	items := []Item{{ID: "a", Tokens: 100, Relevance: 1.0}}
	result := Pack(items, Config{MaxTokens: 0})
	if len(result.Included) != 0 {
		t.Error("zero budget should include nothing")
	}
}

func TestSummary(t *testing.T) {
	result := PackResult{
		Included:    []Item{{ID: "a", Category: "file", Tokens: 100}},
		Excluded:    []Item{{ID: "b", Category: "file", Tokens: 200}},
		TotalTokens: 100,
		Utilization: 0.5,
	}
	s := result.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestPackEfficiency(t *testing.T) {
	// Item with high relevance-per-token should be preferred
	items := []Item{
		{ID: "big", Tokens: 500, Relevance: 0.6},   // 0.0012 per token
		{ID: "small", Tokens: 50, Relevance: 0.5},   // 0.01 per token (better ratio)
	}

	result := Pack(items, Config{MaxTokens: 100, ReserveResponse: 0})
	included := make(map[string]bool)
	for _, item := range result.Included {
		included[item.ID] = true
	}
	if !included["small"] {
		t.Error("small (better ratio) should be included when budget is tight")
	}
}
