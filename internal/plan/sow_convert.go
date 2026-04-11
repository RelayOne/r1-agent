package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// dumpRespMu serializes the debug dump writes so two concurrent
// conversion attempts don't interleave at the byte level.
var dumpRespMu sync.Mutex

// collectModelText pulls assistant text out of a Chat response. It
// prefers `text` content blocks (the normal case) but falls back to
// `thinking` blocks when no text was emitted at all — which happens
// when extended-thinking models burn their entire output budget on
// reasoning and never reach the final answer. The fallback is best-
// effort: if the JSON SOW is hidden inside a thinking block, downstream
// JSON extraction (jsonutil.ExtractJSONObject) will still find the
// {...} object inside it.
//
// The second return value is a one-line-per-block diagnostic that
// callers can include in error messages so the failure mode is
// obvious without re-running with extra logging.
func collectModelText(resp *provider.ChatResponse) (string, string) {
	if resp == nil {
		return "", "  <nil response>\n"
	}
	var text, thinking strings.Builder
	var diag strings.Builder
	for i, c := range resp.Content {
		fmt.Fprintf(&diag, "  block[%d] type=%q text_len=%d thinking_len=%d name=%q\n",
			i, c.Type, len(c.Text), len(c.Thinking), c.Name)
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "thinking", "redacted_thinking":
			if c.Thinking != "" {
				thinking.WriteString(c.Thinking)
				thinking.WriteString("\n")
			}
		}
	}
	if text.Len() > 0 {
		return text.String(), diag.String()
	}
	// No text blocks at all. If thinking blocks exist, fall back to
	// them — extraction will salvage any embedded JSON object.
	if thinking.Len() > 0 {
		return thinking.String(), diag.String()
	}
	return "", diag.String()
}

// marshalRespOrEmpty pretty-prints a ChatResponse to JSON. On marshal
// failure it returns a one-line marker so the dump file always has
// something useful in it.
func marshalRespOrEmpty(resp *provider.ChatResponse) []byte {
	if resp == nil {
		return []byte("null\n")
	}
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return []byte(fmt.Sprintf("marshal error: %v\n", err))
	}
	return b
}

// hashBytes returns a short hex SHA-256 of b. Used to invalidate the prose
// cache when the source file changes.
func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ProseDetectionResult describes how LoadSOWSmart interpreted the input file.
type ProseDetectionResult struct {
	// Format is one of: "json", "yaml", "prose"
	Format string
	// ConvertedPath is where a converted prose SOW was written (empty if
	// the original was already structured).
	ConvertedPath string
	// OriginalPath is the user-supplied file.
	OriginalPath string
}

// sowConversionPrompt is the strict prompt used to turn free-form prose into
// a structured Stoke SOW. It enforces the schema and gives examples of the
// session-by-session decomposition Stoke expects.
const sowConversionPrompt = `You are converting a free-form project specification into a strict Stoke SOW (Statement of Work) JSON document.

The SOW must be session-by-session with acceptance criteria that can be verified mechanically. Sessions run sequentially; tasks within a session run in parallel subject to dependencies; acceptance criteria gate the transition from one session to the next.

Required JSON schema:

{
  "id": "string (short slug, required)",
  "name": "string (human title, required)",
  "description": "string (optional)",
  "stack": {
    "language": "rust|typescript|go|python (required if inferrable)",
    "framework": "next|react-native|actix-web|... (optional)",
    "monorepo": {"tool": "cargo-workspace|turborepo|nx|...", "manager": "pnpm|npm|yarn", "packages": ["..."]},
    "infra": [{"name": "postgres|redis|...", "version": "15", "env_vars": ["DATABASE_URL"]}]
  },
  "sessions": [
    {
      "id": "S1 (short)",
      "phase": "foundation|core|integration|... (optional)",
      "title": "string (required)",
      "description": "string (optional)",
      "tasks": [
        {
          "id": "T1 (short, unique across all sessions)",
          "description": "string (specific, one-sentence)",
          "files": ["path/to/file"],
          "dependencies": ["other task IDs"],
          "type": "refactor|typesafety|docs|security|architecture|devops|concurrency|review"
        }
      ],
      "acceptance_criteria": [
        {
          "id": "AC1 (short, unique)",
          "description": "string",
          "command": "shell command that must exit 0, OR",
          "file_exists": "path/to/required/file, OR",
          "content_match": {"file": "path", "pattern": "string"}
        }
      ],
      "inputs": ["names of outputs from prior sessions this session uses"],
      "outputs": ["artifacts this session produces"],
      "infra_needed": ["names from stack.infra"]
    }
  ]
}

RULES:
1. Output ONLY the JSON. No prose, no backticks, no markdown fences, no commentary.
2. Every session MUST have at least one verifiable acceptance_criteria. Prefer "command" (e.g. "cargo build" or "go test ./...") over file_exists. Use file_exists only for artifacts that don't have a build/test.
3. Task IDs are unique across the entire SOW (T1, T2, ..., not restarting per session). Task.dependencies entries must ALL reference task IDs that exist somewhere in this SOW — never a stale or renamed ID.
4. Session COUNT follows from the feature decomposition — typically one session per major deliverable or feature slice. Don't compress sessions for compression's sake. A SOW describing 10 deliverables naturally has ~10 sessions. A SOW describing 3 deliverables has ~3 sessions.
5. Every task description must be a single specific sentence — no bullet lists inside.
6. Infer the stack from the prose. If the prose says "Rust" or mentions Cargo, set language="rust". If it says Next.js, set framework="next". If ambiguous, leave stack fields empty.
7. If the prose mentions Postgres, Redis, or other services, add them to stack.infra with env_vars they need. Every name referenced in session.infra_needed MUST also appear in stack.infra.
8. The first session must be foundational (repo layout, deps, config, one end-to-end 'hello world' build pass). The last session must be integration/acceptance.

DECOMPOSITION PRINCIPLES:

  a. TASKS SHOULD BE SMALL AND FOCUSED. One task = one discrete change
     the agent can complete in a few tool calls. 10-15 tasks per
     session is fine; 3 tasks per session usually means you grouped
     too coarsely. Small tasks give the agent focused context, bound
     failure to one file/concern, and let parallel execution work
     because file sets stay disjoint. DO split "create package.json +
     tsconfig + eslintrc" into three tasks if they have distinct
     contents. DON'T split "add a single function" into three tasks.

  b. SESSIONS GROUP RELATED TASKS UNDER ONE ACCEPTANCE BOUNDARY. A
     session = "one feature or one infrastructure slice whose
     completion is verifiable as a unit". The acceptance criteria
     test the session's overall outcome, not each task individually.
     One session per major deliverable from the source spec is
     usually the right granularity.

  c. EACH SESSION SHOULD HAVE 2-4 ACCEPTANCE CRITERIA, not 6+. The
     ACs verify the session's feature works as a whole (build green,
     tests pass, one smoke check), not that each task wrote the file
     it was supposed to. If you're about to emit a 5th AC, check
     whether the first 4 already cover it.

  d. SESSION BOUNDARIES ARE CONTEXT BOUNDARIES. Within a session the
     agent carries context across tasks (prior tool results, wisdom,
     shared system prompt). Across sessions the context resets.
     Don't split a feature's implementation across two sessions just
     because it has many tasks — keep the whole feature in one
     session so later tasks can build on earlier tool-use state.

  e. THE FIRST SESSION is foundation: repo layout, dependency
     install, config files, one end-to-end 'hello world' that
     compiles. Don't spread foundation across three sessions.

  f. THE LAST SESSION is integration + polish + docs + deployment
     configs, in one pass. Don't have a separate "Polish" session
     that touches code from 5 prior sessions — fold that work into
     the sessions that own the code.

  g. Avoid session names like "Cleanup" or "Misc" — those are smells
     for "I didn't know where to put this work". Every session
     should have a clear feature name.

ACCEPTANCE CRITERION HYGIENE (follow these or the SOW will fail on real execution):

  a. Commands run in the CURRENT WORKING DIRECTORY — there is no remote
     clone, no $REPO_URL, no mktemp. Do NOT emit commands that start
     with "cd $(mktemp -d)" or "git clone $REPO_URL".

  b. Keep each session to 3-5 acceptance criteria. More than 5 is
     usually a sign you're checking implementation details instead
     of behavior. Cut until each criterion is load-bearing.

  c. Every command must terminate on its own in under 60 seconds.
     Never emit long-running processes (no "next dev", no "vitest"
     without "run", no "expo start").

  d. Never use "|| echo ok" / "|| true" / "|| echo 'X'" fallbacks.
     These turn every command into a pass and defeat the whole point
     of mechanical verification. If a check is optional, don't emit
     it at all.

  e. For Node workspaces: stoke auto-runs 'pnpm install' and prepends
     node_modules/.bin to PATH before AC evaluation. So commands can
     call "tsc", "vitest", "eslint", "next", "jest" directly without
     "npx" or "pnpm exec" wrappers. Prefer direct binaries; they're
     cheaper and more reliable.

  f. Prefer "pnpm --filter <pkg> <script>" over cd-into-directory +
     run-command. Filters are scope-safe and consistent with the
     monorepo's declared scripts.

  g. Grep checks are OK for "the word X appears in file Y" structural
     assertions but should NEVER be used for behavioral assertions
     like "SSE works" or "auth redirects unauthenticated users". Use
     a real build/test command instead, or drop the criterion.

  h. file_exists is OK for artifacts like .github/workflows/ci.yml,
     README.md, package.json — things whose existence IS the
     deliverable. Do NOT use file_exists on source files: the
     content matters, not the path.

  i. If an acceptance criterion requires a tool (axe-core for a11y,
     eas-cli for mobile submission, docker for containerization),
     that tool must be declared in the relevant package.json OR in
     stack.infra. Do NOT emit commands that depend on unspecified
     global tools.

  j. Do NOT emit acceptance criteria that run Playwright, Cypress, or
     other browser-based E2E test frameworks. These require browser
     binaries, display servers, and complex setup that an automated
     build agent cannot provide. Use build/typecheck/unit-test
     commands instead. If the SOW mentions E2E tests, defer them to
     a manual testing session — do NOT make them acceptance criteria.

PROSE INPUT:
`

// ConvertProseToSOW sends free-form project prose to the configured LLM and
// returns a parsed SOW plus its raw JSON. Requires a provider and a model.
// Used by sowCmd when the user passes a .txt or .md file instead of a
// pre-structured SOW.
func ConvertProseToSOW(prose string, prov provider.Provider, model string) (*SOW, []byte, error) {
	if strings.TrimSpace(prose) == "" {
		return nil, nil, fmt.Errorf("empty prose")
	}
	if prov == nil {
		return nil, nil, fmt.Errorf("no provider configured (check --runner / --native-api-key)")
	}

	fullPrompt := sowConversionPrompt + prose

	userMsg, _ := json.Marshal([]map[string]interface{}{
		{"type": "text", "text": fullPrompt},
	})

	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 16000,
		Messages: []provider.ChatMessage{
			{Role: "user", Content: userMsg},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("provider chat: %w", err)
	}

	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		// Dump everything we can to /tmp so we can diagnose. Anthropic-shape
		// providers like MiniMax-via-LiteLLM occasionally return responses
		// where every content block is empty AND usage is zero — that
		// previously surfaced here as a bare "empty response" with no clue
		// what happened.
		dumpRespMu.Lock()
		_ = os.WriteFile("/tmp/stoke-sow-resp-debug.json", marshalRespOrEmpty(resp), 0o644)
		dumpRespMu.Unlock()
		return nil, nil, fmt.Errorf("empty response from model (stop_reason=%q, %d content blocks; full response saved to /tmp/stoke-sow-resp-debug.json)\n%s", resp.StopReason, len(resp.Content), diag)
	}

	// On any parse failure below, write both the raw model output and
	// the extracted JSON blob to /tmp for post-mortem inspection. These
	// files are overwritten per call and are never read by the runner,
	// so they're safe to leave behind.
	dumpOnErr := func(extracted []byte) {
		_ = os.WriteFile("/tmp/stoke-sow-raw.txt", []byte(raw), 0o644)
		if extracted != nil {
			_ = os.WriteFile("/tmp/stoke-sow-extracted.json", extracted, 0o644)
		}
	}

	// Robust extraction: handles markdown fences, prose preamble,
	// BOM, trailing commas, etc. via the shared jsonutil helper.
	jsonBlob, extractErr := jsonutil.ExtractJSONObject(raw)
	if extractErr != nil {
		// Last-ditch: send the broken raw output back to the model
		// with a narrow 'fix the JSON syntax' prompt. Long prose
		// conversions occasionally produce structurally invalid
		// JSON (missing commas between array elements, stray colons
		// where comma-separated keys were intended, etc.) that no
		// static repair can reliably handle. One extra LLM call,
		// but it recovers dozens of minutes of downstream work.
		repaired, repairErr := repairJSONViaLLM(raw, prov, model)
		if repairErr != nil {
			dumpOnErr(nil)
			return nil, nil, fmt.Errorf("parse generated SOW: %w; repair attempt also failed: %v (raw saved to /tmp/stoke-sow-raw.txt)", extractErr, repairErr)
		}
		jsonBlob = repaired
	}

	sow, err := ParseSOW(jsonBlob, "generated.json")
	if err != nil {
		dumpOnErr(jsonBlob)
		return nil, jsonBlob, fmt.Errorf("parse generated SOW: %w (raw: /tmp/stoke-sow-raw.txt, extracted: /tmp/stoke-sow-extracted.json)\n\nfirst 800 chars of extracted:\n%s", err, truncateForError(string(jsonBlob), 800))
	}
	// Auto-synthesize missing required fields on acceptance criteria
	// (id, description). LLM-generated SOWs occasionally emit a
	// criterion with only "command" or "file_exists" populated,
	// missing the id/description the validator requires. Rather than
	// halt on a trivial bookkeeping miss, fill in sensible defaults
	// and let the critique+refine loop improve them downstream.
	autoFillMissingACFields(sow)
	if errs := ValidateSOW(sow); len(errs) > 0 {
		return sow, jsonBlob, fmt.Errorf("generated SOW failed validation: %s", strings.Join(errs, "; "))
	}
	return sow, []byte(jsonBlob), nil
}

// autoFillMissingACFields walks every acceptance criterion and fills
// in synthetic values for id and description when the LLM omitted
// them. Uses a deterministic naming scheme so two successive runs on
// the same SOW produce stable IDs.
//
// Mutation is in place on the passed SOW.
func autoFillMissingACFields(sow *SOW) {
	if sow == nil {
		return
	}
	// Collect existing IDs so our synthetic IDs don't collide.
	existing := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, ac := range s.AcceptanceCriteria {
			if ac.ID != "" {
				existing[ac.ID] = true
			}
		}
	}

	// Generate a fresh unique ID within this session scope.
	nextID := func(sessID string, used map[string]bool) string {
		for i := 1; i < 10000; i++ {
			candidate := fmt.Sprintf("%s-ac%d", sessID, i)
			if !used[candidate] && !existing[candidate] {
				used[candidate] = true
				existing[candidate] = true
				return candidate
			}
		}
		return fmt.Sprintf("%s-ac-x", sessID)
	}

	// Build a sensible description when none was provided, using the
	// shape of the criterion that did come through.
	fallbackDesc := func(ac AcceptanceCriterion) string {
		switch {
		case ac.Command != "":
			// Truncate long commands so the description stays readable.
			cmd := ac.Command
			if len(cmd) > 80 {
				cmd = cmd[:77] + "..."
			}
			return "command succeeds: " + cmd
		case ac.FileExists != "":
			return "file exists: " + ac.FileExists
		case ac.ContentMatch != nil && ac.ContentMatch.File != "":
			return fmt.Sprintf("file %s contains expected content", ac.ContentMatch.File)
		default:
			return "session acceptance check"
		}
	}

	for si := range sow.Sessions {
		sess := &sow.Sessions[si]
		used := map[string]bool{}
		for _, ac := range sess.AcceptanceCriteria {
			if ac.ID != "" {
				used[ac.ID] = true
			}
		}
		for ci := range sess.AcceptanceCriteria {
			ac := &sess.AcceptanceCriteria[ci]
			if ac.ID == "" {
				ac.ID = nextID(sess.ID, used)
			}
			if ac.Description == "" {
				ac.Description = fallbackDesc(*ac)
			}
		}
	}
}

// LoadSOWFile loads a SOW from a path, auto-detecting JSON / YAML / prose.
// Prose files (.txt, .md, or content that isn't JSON/YAML) are converted
// via ConvertProseToSOW using the supplied provider. The converted JSON is
// cached at `${projectRoot}/.stoke/sow-from-prose.json` so re-runs don't
// pay for a fresh conversion every time.
//
// detectProseFmt returns: (sow, result, err).
//
// When err is non-nil the caller should fail loudly — partial/invalid SOWs
// are not silently accepted.
func LoadSOWFile(path, projectRoot string, prov provider.Provider, model string) (*SOW, ProseDetectionResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ProseDetectionResult{OriginalPath: path}, fmt.Errorf("read SOW file: %w", err)
	}

	result := ProseDetectionResult{OriginalPath: path}
	ext := strings.ToLower(filepath.Ext(path))

	// Structured formats: parse directly.
	switch ext {
	case ".json":
		sow, err := ParseSOW(data, path)
		result.Format = "json"
		return sow, result, err
	case ".yaml", ".yml":
		sow, err := ParseSOW(data, path)
		result.Format = "yaml"
		return sow, result, err
	}

	// Unknown extension — sniff content.
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		sow, err := ParseSOW(data, "sniffed.json")
		result.Format = "json"
		return sow, result, err
	}

	// Prose path. Check cache first so we don't re-call the LLM for an
	// identical input file.
	stokeDir := filepath.Join(projectRoot, ".stoke")
	cachePath := filepath.Join(stokeDir, "sow-from-prose.json")
	if cached, ok := loadProseCache(cachePath, data); ok {
		result.Format = "prose"
		result.ConvertedPath = cachePath
		return cached, result, nil
	}

	sow, jsonBlob, err := ConvertProseToSOW(string(data), prov, model)
	if err != nil {
		return nil, result, err
	}

	// Persist the converted SOW + the source hash so rerunning with the
	// same file hits the cache.
	if mkErr := os.MkdirAll(stokeDir, 0o755); mkErr == nil {
		if writeErr := writeProseCache(cachePath, data, jsonBlob); writeErr == nil {
			result.ConvertedPath = cachePath
		}
	}
	result.Format = "prose"
	return sow, result, nil
}

// stripMarkdownFences removes ```json / ``` fences the model may have added
// despite the explicit instruction not to.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove first ``` line
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// proseCache is the on-disk cache file format: stores both the source prose
// hash and the converted SOW blob so we can detect stale caches.
type proseCache struct {
	SourceHash  string          `json:"source_hash"`
	Generated   json.RawMessage `json:"generated_sow"`
}

func loadProseCache(path string, sourceData []byte) (*SOW, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var c proseCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false
	}
	if c.SourceHash != hashBytes(sourceData) {
		return nil, false
	}
	sow, err := ParseSOW(c.Generated, "cache.json")
	if err != nil {
		return nil, false
	}
	return sow, true
}

func writeProseCache(path string, sourceData, generatedBlob []byte) error {
	c := proseCache{
		SourceHash: hashBytes(sourceData),
		Generated:  json.RawMessage(generatedBlob),
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
