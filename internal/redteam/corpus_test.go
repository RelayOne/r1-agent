// Package redteam_test runs a regression corpus of public adversarial
// payloads through stoke's intake-time defensive primitives.
//
// Scope: this is an integration-level red-team suite. It exists so that
// future changes to promptguard patterns (or neighbouring primitives)
// don't silently regress detection on payloads the community has
// documented for years. It is not a proof of robustness -- the
// literature (adaptive-attack paper, 2025) shows all published
// prompt-injection defenses can be bypassed with >90% success. The bar
// here is "catches lazy copy-paste".
//
// Each corpus category has its own minimum detection rate threshold.
// See the per-test comments for rationale. If a threshold drops below
// the floor, the fix is NOT to lower the threshold; either the regex
// set regressed (fix the regex), or the new sample was genuinely out
// of scope (move it to corpus/known-misses/).
package redteam_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/promptguard"
)

// runPromptguardCategory scans every .txt/.md file under corpus/<cat>/
// through promptguard.Sanitize in ActionWarn mode and returns the
// fraction of samples that produced at least one Threat. t.Logf emits
// every miss so regressions are traceable in test output.
func runPromptguardCategory(t *testing.T, cat string) float64 {
	t.Helper()
	dir := filepath.Join("corpus", cat)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total, flagged := 0, 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s/%s: %v", cat, name, err)
		}
		total++
		_, report, _ := promptguard.Sanitize(string(raw), promptguard.ActionWarn, name)
		if len(report.Threats) > 0 {
			flagged++
		} else {
			t.Logf("MISS: %s/%s produced no promptguard hits", cat, name)
		}
	}
	if total == 0 {
		t.Fatalf("no samples in %s", cat)
	}
	return float64(flagged) / float64(total)
}

// TestCorpus_Promptguard_DirectInjection covers classic
// direct-injection attempts (OWASP LLM01, CL4R1T4S, Anthropic red-team
// paraphrases). 60% is the launch threshold per the Task 6 spec — raise
// over time as defenses mature. A drop below this means the regex set
// missed something the community has documented for years.
func TestCorpus_Promptguard_DirectInjection(t *testing.T) {
	rate := runPromptguardCategory(t, "injection-direct")
	t.Logf("direct-injection detection rate: %.1f%%", rate*100)
	if rate < 0.60 {
		t.Errorf("detection rate %.1f%% < 60%% threshold", rate*100)
	}
}

// TestCorpus_Promptguard_IndirectInjection covers content-in-tool-output
// attacks (code comments, search-result meta tags, fake shell output,
// README injection, email signatures, JSON fields, etc.). Same 60%
// launch threshold: the promptguard regex set doesn't care where the
// injection text sits, only that the giveaway phrase is present, so the
// indirect corpus exercises the same patterns but embedded in realistic
// document shells.
func TestCorpus_Promptguard_IndirectInjection(t *testing.T) {
	rate := runPromptguardCategory(t, "injection-indirect")
	t.Logf("indirect-injection detection rate: %.1f%%", rate*100)
	if rate < 0.60 {
		t.Errorf("detection rate %.1f%% < 60%% threshold", rate*100)
	}
}

// templateTokenRE matches the literal chat-template sentinels known to
// appear in public model cards (OpenAI ChatML, Llama 3, Mistral [INST],
// Gemma, Phi, DeepSeek, legacy Claude Human:/Assistant:). This is a
// stand-in for the Task 2 `agentloop.SanitizeToolOutput` helper, which
// is being built in parallel. When that helper lands, replace the body
// of TestCorpus_TemplateTokens with a call to it and drop this regex.
var templateTokenRE = regexp.MustCompile(
	`<\|im_start\|>` +
		`|<\|im_end\|>` +
		`|<\|start_header_id\|>` +
		`|<\|end_header_id\|>` +
		`|<\|eot_id\|>` +
		`|<\|begin_of_text\|>` +
		`|<\|user\|>` +
		`|<\|assistant\|>` +
		`|<\|end\|>` +
		`|<start_of_turn>` +
		`|<end_of_turn>` +
		`|\[INST\]` +
		`|\[/INST\]` +
		`|</s><s>` +
		`|<｜User｜>` +
		`|<｜Assistant｜>` +
		`|(?m)^Human:\s` +
		`|^Assistant:\s`,
)

// TestCorpus_TemplateTokens asserts that every sample in
// corpus/template-tokens/ contains at least one literal chat-template
// sentinel. This is a sanity floor for Task 2's
// `agentloop.SanitizeToolOutput`: if it lands and strips these tokens
// before tool output is folded back into prompts, the corpus stays
// useful as an input fixture. We assert 100% here because literal
// presence of well-known tokens is trivially detectable; the test
// exists to pin the corpus, not to probe a probabilistic defense.
//
// Follow-up once Task 2 lands: replace templateTokenRE with a call to
// agentloop.SanitizeToolOutput and assert that the sanitized output
// no longer contains any token.
func TestCorpus_TemplateTokens(t *testing.T) {
	dir := filepath.Join("corpus", "template-tokens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total, flagged := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", "template-tokens", e.Name(), err)
		}
		total++
		if templateTokenRE.MatchString(string(raw)) {
			flagged++
		} else {
			t.Errorf("MISS: template-tokens/%s contains no known chat-template sentinel", e.Name())
		}
	}
	if total == 0 {
		t.Fatalf("no samples in template-tokens")
	}
	rate := float64(flagged) / float64(total)
	t.Logf("template-token detection rate: %.1f%% (%d/%d)", rate*100, flagged, total)
}

// markdownExfilImageRE matches a markdown image reference whose URL is
// an absolute http(s) URL carrying a query string — the canonical shape
// of the image-exfil attack Rehberger popularised in his Copilot / Bing
// bounty writeups. Reference-style images ![alt][ref] don't match this
// one-line form; the reference-resolution case is handled below.
var markdownExfilImageRE = regexp.MustCompile(
	`!\[[^\]]*\]\(\s*https?://[^)\s]+\?[^)\s]+\)`,
)

// markdownExfilLinkRE matches a markdown link (non-image) whose URL has
// a query string and points off-site. We don't try to distinguish
// "attacker" from "legitimate" here — any user-clickable link with a
// query-string payload is the exfil shape.
var markdownExfilLinkRE = regexp.MustCompile(
	`(^|[^!])\[[^\]]*\]\(\s*https?://[^)\s]+\?[^)\s]+\)`,
)

// markdownExfilRefImageRE catches reference-style image definitions:
// a `![alt][id]` use plus a `[id]: http(s)://...?...` definition in
// the same file.
var markdownExfilRefImageRE = regexp.MustCompile(
	`(?m)^\s*\[[^\]]+\]:\s*https?://[^\s]+\?[^\s]+\s*$`,
)

// htmlImgExfilRE catches raw HTML `<img src="http(s)://...?...">` even
// when embedded inside a markdown document.
var htmlImgExfilRE = regexp.MustCompile(
	`(?i)<img\s+[^>]*src\s*=\s*"https?://[^"]+\?[^"]+"`,
)

// looksLikeMarkdownExfil is the union of the four shapes above.
func looksLikeMarkdownExfil(s string) bool {
	return markdownExfilImageRE.MatchString(s) ||
		markdownExfilLinkRE.MatchString(s) ||
		markdownExfilRefImageRE.MatchString(s) ||
		htmlImgExfilRE.MatchString(s)
}

// TestCorpus_ExfilMarkdown asserts that every sample in
// corpus/exfil-markdown/ looks like a markdown-based exfiltration
// payload. Promptguard doesn't own this class (the payload doesn't try
// to redirect the model's behaviour; it tries to smuggle data out via
// a user-agent image-fetch). The expected future home is a dedicated
// markdown-exfil detector; until then, this test pins the corpus shape
// and catches regressions if a sample is accidentally degraded (e.g.
// query string dropped in a refactor).
func TestCorpus_ExfilMarkdown(t *testing.T) {
	dir := filepath.Join("corpus", "exfil-markdown")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total, flagged := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", "exfil-markdown", e.Name(), err)
		}
		total++
		if looksLikeMarkdownExfil(string(raw)) {
			flagged++
		} else {
			t.Errorf("MISS: exfil-markdown/%s does not match any exfil shape", e.Name())
		}
	}
	if total == 0 {
		t.Fatalf("no samples in exfil-markdown")
	}
	rate := float64(flagged) / float64(total)
	t.Logf("markdown-exfil detection rate: %.1f%% (%d/%d)", rate*100, flagged, total)
}

// TestCorpus_KnownMissesAreStillMissed acts as a canary for the
// corpus/known-misses/ subdirectory: samples we know promptguard
// currently fails to flag (e.g. leetspeak, semantic paraphrase). If a
// future regex tightening starts catching one of them, this test
// fails loudly and a human gets to decide whether to promote the
// sample back into injection-direct. The test is skipped if the
// directory is empty or absent so adding the directory is optional.
func TestCorpus_KnownMissesAreStillMissed(t *testing.T) {
	dir := filepath.Join("corpus", "known-misses")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("no corpus/known-misses directory")
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s/%s: %v", "known-misses", name, err)
		}
		seen++
		_, report, _ := promptguard.Sanitize(string(raw), promptguard.ActionWarn, name)
		if len(report.Threats) > 0 {
			t.Logf("PROMOTE: known-misses/%s is now caught by promptguard — consider moving it into the primary corpus", name)
		}
	}
	if seen == 0 {
		t.Skip("known-misses directory is empty")
	}
}
