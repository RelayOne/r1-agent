package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

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
3. Task IDs are unique across the entire SOW (T1, T2, ..., not restarting per session).
4. Break the work into 3-12 sessions. Each session should be completable in one focused work block.
5. Every task description must be a single specific sentence — no bullet lists inside.
6. Infer the stack from the prose. If the prose says "Rust" or mentions Cargo, set language="rust". If it says Next.js, set framework="next". If ambiguous, leave stack fields empty.
7. If the prose mentions Postgres, Redis, or other services, add them to stack.infra with env_vars they need.
8. The first session must be foundational (repo layout, deps, config). The last session must be integration/acceptance.

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

	var raw string
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil, fmt.Errorf("empty response from model")
	}

	// Robust extraction: handles markdown fences, prose preamble,
	// BOM, trailing commas, etc. via the shared jsonutil helper.
	jsonBlob, extractErr := jsonutil.ExtractJSONObject(raw)
	if extractErr != nil {
		return nil, nil, fmt.Errorf("parse generated SOW: %w", extractErr)
	}

	sow, err := ParseSOW(jsonBlob, "generated.json")
	if err != nil {
		return nil, jsonBlob, fmt.Errorf("parse generated SOW: %w\n\nraw JSON:\n%s", err, truncateForError(string(jsonBlob), 800))
	}
	if errs := ValidateSOW(sow); len(errs) > 0 {
		return sow, jsonBlob, fmt.Errorf("generated SOW failed validation: %s", strings.Join(errs, "; "))
	}
	return sow, []byte(jsonBlob), nil
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
