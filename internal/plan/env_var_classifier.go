// Package plan — env_var_classifier.go
//
// Agentic classifier that decides which infra-declared env vars are
// GENUINELY required at build/test time versus purely runtime concerns
// of the code being built. Without this, stoke's SOW-reasoner tends
// to over-declare env vars (e.g. EVENT_STREAM_URL "because the feature
// involves real-time events") and the scheduler then refuses to run
// the session because the operator hasn't set a runtime URL locally.
// That's a category error: you're BUILDING the code, not deploying it.
//
// Decision categories:
//   - "build-required": must exist at build time (e.g. NEXT_PUBLIC_* baked
//     into bundle, monorepo workspace roots, tool license keys)
//   - "runtime-only": the deployed service needs it, not the build
//     (DB URLs, API endpoints, webhook secrets, message broker URLs)
//   - "unsure": the LLM couldn't confidently classify; caller decides
//     whether to gate (conservative default: don't gate)
//
// The result is cached keyed on sha256(sowSpec + declared vars list),
// so successive runs against the same SOW don't re-pay the LLM call.
package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/jsonutil"
	"github.com/RelayOne/r1/internal/provider"
)

var _ = context.Background // ctx param kept for future use / symmetry with other classifier calls

// EnvVarCategory enumerates the classifier's output labels.
type EnvVarCategory string

const (
	EnvVarBuildRequired EnvVarCategory = "build-required"
	EnvVarRuntimeOnly   EnvVarCategory = "runtime-only"
	EnvVarUnsure        EnvVarCategory = "unsure"
)

// EnvVarClassification is the classifier's verdict for one var.
type EnvVarClassification struct {
	Variable string         `json:"variable"`
	Category EnvVarCategory `json:"category"`
	Reason   string         `json:"reason"`
}

// EnvVarClassificationReport is the cached output for one SOW.
type EnvVarClassificationReport struct {
	SOWHash         string                 `json:"sow_hash"`
	ClassifiedAt    string                 `json:"classified_at"`
	Classifications []EnvVarClassification `json:"classifications"`
}

// BuildRequiredSet returns just the names flagged build-required.
// Caller uses this as a gating filter against os.Getenv lookups.
func (r *EnvVarClassificationReport) BuildRequiredSet() map[string]bool {
	out := make(map[string]bool, len(r.Classifications))
	for _, c := range r.Classifications {
		if c.Category == EnvVarBuildRequired {
			out[c.Variable] = true
		}
	}
	return out
}

// ClassifyEnvVars enumerates all infra-declared env vars in the SOW
// and asks the reasoning provider to classify each. Returns a report
// (possibly from cache). If prov is nil, returns a report that
// classifies everything as "unsure" so callers fall back to safe
// (non-gating) behavior.
func ClassifyEnvVars(ctx context.Context, prov provider.Provider, model string, sow *SOW, rawSOW string, repoRoot string) (*EnvVarClassificationReport, error) {
	if sow == nil {
		return &EnvVarClassificationReport{}, nil
	}
	vars := collectDeclaredEnvVars(sow)
	if len(vars) == 0 {
		return &EnvVarClassificationReport{}, nil
	}
	sort.Strings(vars)
	hash := hashClassifierInput(rawSOW, vars)

	// Cache check.
	if cached := loadClassifierCache(repoRoot, hash); cached != nil {
		return cached, nil
	}

	// No provider → conservative "unsure" for all so caller skips gating.
	if prov == nil {
		out := &EnvVarClassificationReport{SOWHash: hash, ClassifiedAt: time.Now().Format(time.RFC3339)}
		for _, v := range vars {
			out.Classifications = append(out.Classifications, EnvVarClassification{
				Variable: v,
				Category: EnvVarUnsure,
				Reason:   "no reasoning provider available — defaulting to non-gating",
			})
		}
		saveClassifierCache(repoRoot, out)
		return out, nil
	}

	prompt := buildClassifierPrompt(sow, rawSOW, vars)

	chatModel := model
	if chatModel == "" {
		chatModel = "claude-sonnet-4-6"
	}
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     chatModel,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("env-var classifier: %w", err)
	}

	var raw string
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}

	var parsed struct {
		Classifications []EnvVarClassification `json:"classifications"`
	}
	if _, perr := jsonutil.ExtractJSONInto(raw, &parsed); perr != nil {
		// Non-compliant verdict — mark everything unsure so the caller
		// doesn't over-block. Loud log so we know it happened.
		fmt.Printf("  ⚠ env-var classifier: non-JSON verdict, defaulting all to unsure\n")
		out := &EnvVarClassificationReport{SOWHash: hash, ClassifiedAt: time.Now().Format(time.RFC3339)}
		for _, v := range vars {
			out.Classifications = append(out.Classifications, EnvVarClassification{
				Variable: v,
				Category: EnvVarUnsure,
				Reason:   "classifier returned non-JSON; defaulting to non-gating",
			})
		}
		saveClassifierCache(repoRoot, out)
		return out, nil
	}

	// Backfill any vars the LLM skipped (shouldn't happen, but safe).
	seen := make(map[string]bool, len(parsed.Classifications))
	for _, c := range parsed.Classifications {
		seen[c.Variable] = true
	}
	for _, v := range vars {
		if !seen[v] {
			parsed.Classifications = append(parsed.Classifications, EnvVarClassification{
				Variable: v,
				Category: EnvVarUnsure,
				Reason:   "classifier omitted this variable; defaulting to non-gating",
			})
		}
	}

	out := &EnvVarClassificationReport{
		SOWHash:         hash,
		ClassifiedAt:    time.Now().Format(time.RFC3339),
		Classifications: parsed.Classifications,
	}
	saveClassifierCache(repoRoot, out)
	return out, nil
}

func collectDeclaredEnvVars(sow *SOW) []string {
	seen := map[string]bool{}
	for _, inf := range sow.Stack.Infra {
		for _, v := range inf.EnvVars {
			v = strings.TrimSpace(v)
			if v != "" {
				seen[v] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out
}

func hashClassifierInput(rawSOW string, vars []string) string {
	h := sha256.New()
	h.Write([]byte(rawSOW))
	h.Write([]byte{0})
	for _, v := range vars {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func classifierCachePath(repoRoot string) string {
	return filepath.Join(repoRoot, ".stoke", "env-var-classification.json")
}

func loadClassifierCache(repoRoot, wantHash string) *EnvVarClassificationReport {
	if repoRoot == "" {
		return nil
	}
	data, err := os.ReadFile(classifierCachePath(repoRoot))
	if err != nil {
		return nil
	}
	var r EnvVarClassificationReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}
	if r.SOWHash != wantHash {
		return nil
	}
	return &r
}

func saveClassifierCache(repoRoot string, r *EnvVarClassificationReport) {
	if repoRoot == "" || r == nil {
		return
	}
	_ = os.MkdirAll(filepath.Join(repoRoot, ".stoke"), 0o755)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(classifierCachePath(repoRoot), data, 0o644) // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
}

func buildClassifierPrompt(sow *SOW, rawSOW string, vars []string) string {
	spec := rawSOW
	if len(spec) > 4000 {
		spec = spec[:4000] + "\n... (truncated)"
	}

	var sb strings.Builder
	sb.WriteString("You are classifying infrastructure environment variables declared by a Statement of Work (SOW).\n\n")
	sb.WriteString("For each variable, decide whether it is required AT BUILD/TEST TIME ")
	sb.WriteString("or whether it is purely a RUNTIME concern of the deployed service.\n\n")
	sb.WriteString("Categories:\n")
	sb.WriteString("- \"build-required\": the env var MUST exist for `pnpm build`, `tsc --noEmit`, `vitest run`, `jest`, `cargo build`, `go build`, or similar build/test commands to succeed. Examples: NEXT_PUBLIC_* baked into the bundle, monorepo workspace roots, tool license keys.\n")
	sb.WriteString("- \"runtime-only\": the deployed service reads it at runtime, but the build + tests pass without it (tests should mock the upstream). Examples: database URLs, API base URLs, webhook secrets, message-broker URLs, event-stream URLs, OAuth secrets.\n")
	sb.WriteString("- \"unsure\": you cannot tell from the SOW alone.\n\n")
	sb.WriteString("Default bias: when in doubt, choose \"runtime-only\". Building a service locally should not require real upstream infrastructure.\n\n")
	sb.WriteString("SOW spec (truncated):\n")
	sb.WriteString("---\n")
	sb.WriteString(spec)
	sb.WriteString("\n---\n\n")
	sb.WriteString("Declared env vars to classify:\n")
	for _, v := range vars {
		sb.WriteString("- ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	sb.WriteString("\nRespond with STRICT JSON ONLY, no prose, shaped as:\n")
	sb.WriteString("{\n  \"classifications\": [\n    {\"variable\": \"NAME\", \"category\": \"build-required|runtime-only|unsure\", \"reason\": \"one sentence\"}\n  ]\n}\n")
	return sb.String()
}
