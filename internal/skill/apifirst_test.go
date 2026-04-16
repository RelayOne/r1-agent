package skill

import (
	"strings"
	"testing"
)

func TestDefaultTier_BrowserNames(t *testing.T) {
	for _, name := range []string{"browser_navigate", "playwright_click", "puppeteer_screenshot", "selenium_find", "webdriver_wait"} {
		if got := DefaultTier(name); got != TierFallback {
			t.Errorf("%q tier=%q want fallback", name, got)
		}
	}
}

func TestDefaultTier_RestrictedNames(t *testing.T) {
	for _, name := range []string{"shell_rm_rf", "invoke_sudo_privileged", "db_drop_table", "format_disk_low"} {
		if got := DefaultTier(name); got != TierRestricted {
			t.Errorf("%q tier=%q want restricted", name, got)
		}
	}
}

func TestDefaultTier_PrimaryNames(t *testing.T) {
	for _, name := range []string{"web_search", "calendar_list_events", "code_search", "email_send"} {
		if got := DefaultTier(name); got != TierPrimary {
			t.Errorf("%q tier=%q want primary", name, got)
		}
	}
}

func TestSort_PrimaryBeforeFallbackBeforeRestricted(t *testing.T) {
	tools := []PositionedTool{
		{Name: "browser_nav", Tier: TierFallback},
		{Name: "web_search", Tier: TierPrimary},
		{Name: "shell_rm_rf", Tier: TierRestricted},
		{Name: "code_search", Tier: TierPrimary},
	}
	Sort(tools)
	want := []string{"code_search", "web_search", "browser_nav", "shell_rm_rf"}
	for i, t2 := range tools {
		if t2.Name != want[i] {
			t.Errorf("sorted[%d]=%q want %q", i, t2.Name, want[i])
		}
	}
}

func TestRenderPromptSection_Headers(t *testing.T) {
	tools := []PositionedTool{
		{Name: "web_search", Tier: TierPrimary, Description: "structured web search"},
		{Name: "browser_nav", Tier: TierFallback, Description: "browser automation"},
		{Name: "shell_rm_rf", Tier: TierRestricted, Description: "filesystem delete"},
	}
	out := RenderPromptSection(tools)
	for _, want := range []string{
		"PRIMARY TOOLS (try these first",
		"FALLBACK TOOLS (use only when",
		"RESTRICTED TOOLS (require explicit policy grant",
		"web_search: structured web search",
		"browser_nav: browser automation",
		"shell_rm_rf: filesystem delete",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderPromptSection_EmptyTiersOmitted(t *testing.T) {
	tools := []PositionedTool{
		{Name: "web_search", Tier: TierPrimary, Description: "s"},
	}
	out := RenderPromptSection(tools)
	if strings.Contains(out, "FALLBACK TOOLS") {
		t.Error("empty fallback tier should be omitted")
	}
	if strings.Contains(out, "RESTRICTED TOOLS") {
		t.Error("empty restricted tier should be omitted")
	}
}

func TestRenderPromptSection_ReasonAppended(t *testing.T) {
	tools := []PositionedTool{
		{Name: "browser_nav", Tier: TierFallback, Description: "d",
			Reason: "needs JS execution"},
	}
	out := RenderPromptSection(tools)
	if !strings.Contains(out, "(needs JS execution)") {
		t.Errorf("reason should render in parens; got:\n%s", out)
	}
}
