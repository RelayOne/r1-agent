package main

// Substrate deterministic offload pre-pass.
//
// Motivation (from the Substrate×r1 end-to-end experiment, FINDINGS.md):
// exposing the Substrate codegen MCP tool to the sow worker was NOT enough —
// even with the tool advertised and trusted, the LLM worker hand-wrote the
// handler with edit_file instead of calling the tool. Exposure != usage.
//
// The fix is to stop relying on the model's discretion for covered codegen.
// Before a sow task burns LLM tokens generating a handler, we deterministically
// classify the task against Substrate's template library. If Substrate reports
// a confident, syntactically-validated hit, we write that code directly and
// skip the LLM dispatch for the task entirely. Otherwise we return nil and the
// task falls through to the normal worker loop — fail-safe, mirroring the
// router's abstain behaviour. The model never loses a task; it only loses the
// chance to re-derive code Substrate already emits at ~0 generation tokens.
//
// Gated behind R1_SOW_OFFLOAD=1 so it is strictly opt-in and the same binary
// runs both arms (set = offload pre-pass active; unset = baseline).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/plan"
)

// offerResult mirrors the JSON the `substrate offer` CLI prints on stdout:
//
//	{ "hit": bool, "code": "...", "intent_type": "...",
//	  "validated_syntax": bool, "tokens_used": N }
//
// Fields absent on a miss (code/validated_syntax) decode to their zero value.
type offerResult struct {
	Hit             bool   `json:"hit"`
	Code            string `json:"code"`
	IntentType      string `json:"intent_type"`
	ValidatedSyntax bool   `json:"validated_syntax"`
	TokensUsed      int    `json:"tokens_used"`
}

// offloadConfig is resolved from the environment once per task. Empty/zero
// values mean "feature off" or "use defaults".
type offloadConfig struct {
	enabled bool   // R1_SOW_OFFLOAD=1
	bin     string // SUBSTRATE_BIN (path to the substrate release binary)
	lib     string // SUBSTRATE_LIB (absolute path to a template library dir)
}

func resolveOffloadConfig() offloadConfig {
	return offloadConfig{
		enabled: os.Getenv("R1_SOW_OFFLOAD") == "1",
		bin:     os.Getenv("SUBSTRATE_BIN"),
		lib:     os.Getenv("SUBSTRATE_LIB"),
	}
}

// runSubstrateOffer shells out to `substrate offer --request <text>` and parses
// the JSON verdict. The request text is passed via argv (never a secret), and
// the lib path is forced absolute so the verdict is independent of the worker's
// cwd. Any non-zero exit / parse failure yields ok=false so the caller falls
// back to the LLM path. This is the only impure (subprocess) part of the hook;
// classification and code application below are pure and unit-tested.
func runSubstrateOffer(cfg offloadConfig, request string) (offerResult, bool) {
	if cfg.bin == "" || cfg.lib == "" {
		return offerResult{}, false
	}
	args := []string{"offer", "--request", request, "--lib", cfg.lib}
	cmd := exec.Command(cfg.bin, args...)
	// Inherit the environment (substrate's intent parser reads
	// OPENROUTER_API_KEY from env for the NL-classification step). We never
	// place the key on argv.
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return offerResult{}, false
	}
	var res offerResult
	if jErr := json.Unmarshal(out, &res); jErr != nil {
		return offerResult{}, false
	}
	return res, true
}

// offerConfident reports whether an offer verdict is safe to apply without LLM
// review. Conservative on purpose: a hit alone is not enough — we require the
// emitted code to have passed Substrate's own syntax validation and to be
// non-empty. Anything less falls through to the model.
func offerConfident(res offerResult) bool {
	return res.Hit && res.ValidatedSyntax && strings.TrimSpace(res.Code) != ""
}

// offloadTarget picks the single file the offered code should be written to.
// We only offload when the task declares exactly one target file and it is a
// Rust source file (Substrate's libraries are rust-axum / rust-actix today).
// Returning ok=false means "can't safely place this" -> fall through to LLM.
func offloadTarget(task plan.Task) (string, bool) {
	if len(task.Files) != 1 {
		return "", false
	}
	f := strings.TrimSpace(task.Files[0])
	if f == "" || !strings.HasSuffix(f, ".rs") {
		return "", false
	}
	return f, true
}

// applyOfferedCode writes offered code into the task's target file inside
// repoRoot. It is pure I/O over (repoRoot, task, res) — no subprocess, no env —
// so the decision-and-write logic is exercised directly by unit tests.
//
// Behaviour:
//   - target file must already exist (Substrate emits a handler body to splice
//     into an existing crate; it does not scaffold the module). Missing file =>
//     fall through to the LLM, which can create scaffolding.
//   - idempotent: if the emitted symbol is already present, treat as a no-op
//     success (nothing to do) rather than appending a duplicate.
//   - otherwise append the handler (newline-separated) to the file.
//
// Returns the bytes written (0 on idempotent no-op) or an error.
func applyOfferedCode(repoRoot string, target string, res offerResult) (int, error) {
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

// offloadPrePass is the deterministic pre-pass invoked from the sow task loop
// before execNativeTask. It returns a non-nil *TaskExecResult when the task was
// satisfied by Substrate (the LLM dispatch is then skipped), or nil to fall
// through to the normal worker loop.
//
// Fail-safe contract: every uncertain or error condition returns nil. The hook
// can only ever ADD a fast deterministic path; it can never fail a task that
// the LLM would otherwise have handled.
func offloadPrePass(task plan.Task, repoRoot string) *plan.TaskExecResult {
	cfg := resolveOffloadConfig()
	return offloadPrePassWith(cfg, runSubstrateOffer, task, repoRoot)
}

// offloadPrePassWith is the injectable core of offloadPrePass: the offer
// function is a parameter so tests can drive it without a subprocess.
func offloadPrePassWith(
	cfg offloadConfig,
	offer func(offloadConfig, string) (offerResult, bool),
	task plan.Task,
	repoRoot string,
) *plan.TaskExecResult {
	if !cfg.enabled {
		return nil
	}
	target, ok := offloadTarget(task)
	if !ok {
		return nil
	}
	req := strings.TrimSpace(task.Description)
	if req == "" {
		return nil
	}

	res, ok := offer(cfg, req)
	if !ok || !offerConfident(res) {
		return nil
	}

	n, err := applyOfferedCode(repoRoot, target, res)
	if err != nil {
		// Could not place the code -> let the LLM handle it.
		fmt.Printf("    ⚠ substrate-offload: %s declined (%v) — falling through to worker\n", task.ID, err)
		return nil
	}

	fmt.Printf("    ⚡ substrate-offload: %s satisfied by Substrate template %q "+
		"(%d bytes written, %d intent-parse tokens, 0 generation tokens) — skipping LLM dispatch\n",
		task.ID, res.IntentType, n, res.TokensUsed)

	return &plan.TaskExecResult{
		TaskID:  task.ID,
		Success: true,
	}
}
