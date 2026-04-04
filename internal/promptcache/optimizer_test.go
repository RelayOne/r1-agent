package promptcache

import (
	"strings"
	"testing"
)

func TestBuildBasic(t *testing.T) {
	o := New()
	o.AddSection(Section{Label: "Instructions", Content: "You are a coding assistant.", Static: true, Priority: 0})
	o.AddSection(Section{Label: "Task", Content: "Fix the bug in main.go.", Static: false, Priority: 10})

	p := o.Build("Here is the code...")
	if p.System == "" {
		t.Error("system prompt should not be empty")
	}
	if p.User != "Here is the code..." {
		t.Error("user content should be preserved")
	}
}

func TestStaticFirst(t *testing.T) {
	o := New()
	o.AddSection(Section{Label: "Dynamic", Content: "task specific", Static: false, Priority: 10})
	o.AddSection(Section{Label: "Static", Content: "always the same", Static: true, Priority: 0})

	p := o.Build("")
	// Static content should appear before dynamic
	staticIdx := strings.Index(p.System, "always the same")
	dynamicIdx := strings.Index(p.System, "task specific")
	if staticIdx > dynamicIdx {
		t.Error("static content should come before dynamic")
	}
}

func TestCacheHit(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: "static content", Static: true})

	o.Build("task 1")
	o.Build("task 2") // same static = cache hit

	stats := o.Stats()
	if stats.Hits != 1 {
		t.Errorf("expected 1 cache hit, got %d", stats.Hits)
	}
}

func TestCacheBreak(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: "version 1", Static: true})
	o.Build("task")

	o.SetSections([]Section{{Content: "version 2", Static: true}})
	o.Build("task")

	stats := o.Stats()
	if stats.Breaks != 1 {
		t.Errorf("expected 1 cache break, got %d", stats.Breaks)
	}
}

func TestTokenEstimation(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: strings.Repeat("word ", 100), Static: true})
	o.AddSection(Section{Content: "short", Static: false})

	p := o.Build("")
	if p.StaticTokens == 0 {
		t.Error("should estimate static tokens")
	}
}

func TestHitRate(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: "stable", Static: true})

	o.Build("a")
	o.Build("b")
	o.Build("c")

	rate := o.HitRate()
	if rate < 0.5 {
		t.Errorf("expected high hit rate, got %f", rate)
	}
}

func TestSuggestions(t *testing.T) {
	o := New()
	o.AddSection(Section{Label: "big dynamic", Content: strings.Repeat("x", 5000), Static: false})

	suggestions := o.Suggestions()
	if len(suggestions) == 0 {
		t.Error("should suggest making large dynamic section static")
	}
}

func TestEstimateSavings(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: strings.Repeat("x", 4000), Static: true}) // ~1000 tokens

	o.Build("a")
	o.Build("b") // cache hit, saves ~1000 tokens

	savings := o.EstimateSavings(3.0) // $3/M tokens
	if savings <= 0 {
		t.Error("should estimate positive savings")
	}
}

func TestPriorityOrdering(t *testing.T) {
	o := New()
	o.AddSection(Section{Label: "C", Content: "third", Static: true, Priority: 30})
	o.AddSection(Section{Label: "A", Content: "first", Static: true, Priority: 10})
	o.AddSection(Section{Label: "B", Content: "second", Static: true, Priority: 20})

	p := o.Build("")
	firstIdx := strings.Index(p.System, "first")
	secondIdx := strings.Index(p.System, "second")
	thirdIdx := strings.Index(p.System, "third")

	if firstIdx > secondIdx || secondIdx > thirdIdx {
		t.Error("sections should be ordered by priority")
	}
}

func TestStaticHash(t *testing.T) {
	o := New()
	o.AddSection(Section{Content: "stable", Static: true})

	p1 := o.Build("a")
	p2 := o.Build("b")

	if p1.StaticHash != p2.StaticHash {
		t.Error("same static content should produce same hash")
	}
}

func TestEmptyOptimizer(t *testing.T) {
	o := New()
	p := o.Build("just a user message")
	if p.User != "just a user message" {
		t.Error("should work with no sections")
	}
}
