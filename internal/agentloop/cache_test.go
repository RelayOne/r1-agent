package agentloop

import (
	"testing"

	"github.com/RelayOne/r1/internal/provider"
)

func TestSortToolsDeterministic(t *testing.T) {
	tools := []provider.ToolDef{
		{Name: "write_file"},
		{Name: "bash"},
		{Name: "read_file"},
		{Name: "grep"},
	}
	sorted := SortToolsDeterministic(tools)
	expected := []string{"bash", "grep", "read_file", "write_file"}
	for i, name := range expected {
		if sorted[i].Name != name {
			t.Errorf("sorted[%d]=%q, want %q", i, sorted[i].Name, name)
		}
	}
	// Original should be unchanged
	if tools[0].Name != "write_file" {
		t.Error("original slice was modified")
	}
}

func TestBuildCachedSystemPrompt(t *testing.T) {
	blocks := BuildCachedSystemPrompt("You are a coding assistant.", "cwd: /tmp")
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].CacheControl == nil {
		t.Error("static block should have cache_control")
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control type=%q, want ephemeral", blocks[0].CacheControl.Type)
	}
	if blocks[1].CacheControl != nil {
		t.Error("dynamic block should not have cache_control")
	}
}

func TestBuildCachedSystemPromptNoDynamic(t *testing.T) {
	blocks := BuildCachedSystemPrompt("Static only.", "")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestEstimateToolTokens(t *testing.T) {
	tools := []provider.ToolDef{
		{Name: "bash"}, // built-in: 245
		{Name: "str_replace_based_edit_tool"}, // built-in: 700
		{Name: "custom", Description: "A custom tool that does something"},
	}
	tokens := EstimateToolTokens(tools)
	// 346 (fixed) + 245 (bash) + 700 (editor) + 20 + len(desc)/4
	minExpected := 346 + 245 + 700 + 20
	if tokens < minExpected {
		t.Errorf("tokens=%d, expected at least %d", tokens, minExpected)
	}
}

func TestEstimateToolTokensEmpty(t *testing.T) {
	tokens := EstimateToolTokens(nil)
	if tokens != 346 {
		t.Errorf("empty tools tokens=%d, want 346 (fixed overhead)", tokens)
	}
}

func TestCacheSavingsEstimate(t *testing.T) {
	noCost, withCost := CacheSavingsEstimate(5000, 1000, 3000, 10, "claude-sonnet-4-5")
	if noCost <= 0 {
		t.Error("no-cache cost should be > 0")
	}
	if withCost <= 0 {
		t.Error("with-cache cost should be > 0")
	}
	if withCost >= noCost {
		t.Errorf("caching should reduce cost: no_cache=%.4f, with_cache=%.4f", noCost, withCost)
	}
	savings := (noCost - withCost) / noCost * 100
	if savings < 30 {
		t.Errorf("savings=%.1f%%, expected at least 30%%", savings)
	}
}

func TestDefaultCacheConfig(t *testing.T) {
	cfg := DefaultCacheConfig()
	if !cfg.CacheTools {
		t.Error("CacheTools should default to true")
	}
	if !cfg.CacheSystem {
		t.Error("CacheSystem should default to true")
	}
	if !cfg.AutoCache {
		t.Error("AutoCache should default to true")
	}
}
