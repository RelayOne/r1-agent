// Package substrate is R1's internal deterministic-codegen accelerator.
//
// PROVENANCE: the routing mechanism here was internalized into R1 from
// RelayOne/substrate (commit 5dc3287) on 2026-07-09. Substrate is deprecated as
// a standalone product and is now an internal accelerator: R1 routes routine,
// covered codegen to a deterministic engine first and falls through to frontier
// inference on any miss or uncertainty (fail-safe). The deterministic engine
// itself stays external — it is invoked as a release binary / MCP server, and R1
// does not embed its corpus or templates. No efficiency claims are made here; the
// only numbers R1 records are measured at runtime (tokens actually used).
//
// Motivation (from the Substrate x R1 end-to-end experiment): exposing the
// Substrate codegen MCP tool to the worker was NOT enough — even with the tool
// advertised and trusted, the model hand-wrote the handler with edit_file
// instead of calling the tool. Exposure != usage. So covered codegen is no
// longer left to the model's discretion: before a task burns inference tokens,
// the task is classified against the engine's template library, and a confident,
// syntactically-validated hit is written directly, skipping the model dispatch.
//
// Gated behind R1_SOW_OFFLOAD=1 so it is strictly opt-in and the same binary runs
// both arms (set = router active; unset = baseline).
package substrate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/plan"
)

// OfferResult mirrors the JSON the `substrate offer` CLI prints on stdout:
//
//	{ "hit": bool, "code": "...", "intent_type": "...",
//	  "validated_syntax": bool, "tokens_used": N }
//
// Fields absent on a miss (code/validated_syntax) decode to their zero value.
type OfferResult struct {
	Hit             bool   `json:"hit"`
	Code            string `json:"code"`
	IntentType      string `json:"intent_type"`
	ValidatedSyntax bool   `json:"validated_syntax"`
	TokensUsed      int    `json:"tokens_used"`
}

// Config is resolved from the environment once per task. Empty/zero values mean
// "feature off" or "use defaults".
type Config struct {
	Enabled bool   // R1_SOW_OFFLOAD=1
	Bin     string // SUBSTRATE_BIN (path to the substrate release binary)
	Lib     string // SUBSTRATE_LIB (absolute path to a template library dir)
}

// ResolveConfig reads the router configuration from the environment.
func ResolveConfig() Config {
	return Config{
		Enabled: os.Getenv("R1_SOW_OFFLOAD") == "1",
		Bin:     os.Getenv("SUBSTRATE_BIN"),
		Lib:     os.Getenv("SUBSTRATE_LIB"),
	}
}

// RunOffer shells out to `substrate offer --request <text>` and parses the JSON
// verdict. The request text is passed via argv (never a secret), and the lib path
// is forced through as-is so the verdict is independent of the worker's cwd. Any
// non-zero exit / parse failure yields ok=false so the caller falls back to the
// inference path. This is the only impure (subprocess) part of the router;
// classification and code application below are pure and unit-tested.
func RunOffer(cfg Config, request string) (OfferResult, bool) {
	if cfg.Bin == "" || cfg.Lib == "" {
		return OfferResult{}, false
	}
	args := []string{"offer", "--request", request, "--lib", cfg.Lib}
	cmd := exec.Command(cfg.Bin, args...)
	// Inherit the environment (the engine's intent parser reads its model API
	// key from env for the NL-classification step). We never place the key on
	// argv.
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return OfferResult{}, false
	}
	var res OfferResult
	if jErr := json.Unmarshal(out, &res); jErr != nil {
		return OfferResult{}, false
	}
	return res, true
}

// OfferConfident reports whether an offer verdict is safe to apply without model
// review. Conservative on purpose: a hit alone is not enough — the emitted code
// must have passed the engine's own syntax validation and be non-empty. Anything
// less falls through to inference.
func OfferConfident(res OfferResult) bool {
	return res.Hit && res.ValidatedSyntax && strings.TrimSpace(res.Code) != ""
}

// Target picks the single file the offered code should be written to. R1 only
// offloads when the task declares exactly one target file and it is a Rust source
// file (the engine's libraries are rust-axum / rust-actix today). Returning
// ok=false means "can't safely place this" -> fall through to inference.
func Target(task plan.Task) (string, bool) {
	if len(task.Files) != 1 {
		return "", false
	}
	f := strings.TrimSpace(task.Files[0])
	if f == "" || !strings.HasSuffix(f, ".rs") {
		return "", false
	}
	return f, true
}

// ApplyOfferedCode writes offered code into the task's target file inside
// repoRoot. It is pure I/O over (repoRoot, target, res) — no subprocess, no env —
// so the decision-and-write logic is exercised directly by unit tests.
//
// Behaviour:
//   - target file must already exist (the engine emits a handler body to splice
//     into an existing crate; it does not scaffold the module). Missing file =>
//     fall through to inference, which can create scaffolding.
//   - idempotent: if the emitted symbol is already present, treat as a no-op
//     success rather than appending a duplicate.
//   - otherwise append the handler (newline-separated) to the file.
//
// Returns the bytes written (0 on idempotent no-op) or an error.
func ApplyOfferedCode(repoRoot, target string, res OfferResult) (int, error) {
	abs := filepath.Join(repoRoot, target)
	existing, err := os.ReadFile(abs)
	if err != nil {
		return 0, fmt.Errorf("offload target unreadable (%s): %w", target, err)
	}
	code := strings.TrimRight(res.Code, "\n") + "\n"

	// Idempotency: if the offered function signature already appears, don't
	// double-write. We key on the `pub async fn <name>(` prefix when present.
	if sig := extractFnSignature(code); sig != "" && strings.Contains(string(existing), sig) {
		return 0, nil
	}

	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(code)
	if err := os.WriteFile(abs, []byte(b.String()), 0o644); err != nil {
		return 0, fmt.Errorf("offload write failed (%s): %w", target, err)
	}
	return len(code), nil
}

// extractFnSignature returns the `pub async fn name(` / `pub fn name(` prefix of
// the first function declaration in code, used for idempotency checks. Empty if
// no recognisable signature is found.
func extractFnSignature(code string) string {
	for _, line := range strings.Split(code, "\n") {
		t := strings.TrimSpace(line)
		for _, kw := range []string{"pub async fn ", "pub fn ", "async fn ", "fn "} {
			if strings.HasPrefix(t, kw) {
				if i := strings.IndexByte(t, '('); i > 0 {
					return t[:i+1]
				}
			}
		}
	}
	return ""
}

// PrePass is the deterministic router pre-pass invoked from the sow task loop
// before model dispatch. It returns a non-nil *TaskExecResult when the task was
// satisfied by the engine (the inference dispatch is then skipped), or nil to
// fall through to the normal worker loop.
//
// Fail-safe contract: every uncertain or error condition returns nil. The router
// can only ever ADD a fast deterministic path; it can never fail a task that
// inference would otherwise have handled.
func PrePass(task plan.Task, repoRoot string) *plan.TaskExecResult {
	return PrePassWith(ResolveConfig(), RunOffer, task, repoRoot)
}

// PrePassWith is the injectable core of PrePass: the offer function is a
// parameter so tests can drive it without a subprocess.
func PrePassWith(
	cfg Config,
	offer func(Config, string) (OfferResult, bool),
	task plan.Task,
	repoRoot string,
) *plan.TaskExecResult {
	if !cfg.Enabled {
		return nil
	}
	target, ok := Target(task)
	if !ok {
		return nil
	}
	req := strings.TrimSpace(task.Description)
	if req == "" {
		return nil
	}

	res, ok := offer(cfg, req)
	if !ok || !OfferConfident(res) {
		return nil
	}

	n, err := ApplyOfferedCode(repoRoot, target, res)
	if err != nil {
		// Could not place the code -> let inference handle it.
		fmt.Printf("    substrate-offload: %s declined (%v) — falling through to worker\n", task.ID, err)
		return nil
	}

	fmt.Printf("    substrate-offload: %s satisfied by template %q "+
		"(%d bytes written, %d intent-parse tokens, 0 generation tokens) — skipping inference dispatch\n",
		task.ID, res.IntentType, n, res.TokensUsed)

	return &plan.TaskExecResult{
		TaskID:  task.ID,
		Success: true,
	}
}
