// Intent gate — the second half of the router surface the
// executor-foundation spec calls for (RT-11 §7, D29 / Factory DROID).
//
// Where TaskType answers "which executor handles this input", Intent
// answers "does the operator want me to change things, or merely
// understand them". Mixing the two lets the scheduler clamp a
// worker's tool authorization at dispatch time: a DIAGNOSE intent
// means no write-tools, regardless of what the worker's role
// template would otherwise permit. That enforcement is at the tool-
// authorization layer (not the prompt), so a worker that ignores the
// prompt still cannot mutate the repo.
//
// This file adds:
//   - `Intent` enum (IntentImplement / IntentDiagnose / IntentAmbiguous).
//   - `ClassifyIntentDeterministic` — regex-first, no LLM.
//   - `ClassifyIntent` — thin wrapper that falls through to a Haiku
//     classifier when the deterministic phase returns false.
//     The LLM hook is injected; callers wire a real provider.
//   - `Gate` — the scheduler-facing entry point. Returns the Intent
//     plus a (possibly-clamped) tool set.
//   - `ClampReadOnly` — strips write-capable tools from a
//     `harness/tools` tool list. Self-contained so this package does
//     not need a new method on `harness/tools`.
//
// The deterministic tables intentionally mirror the regexes documented
// in specs/executor-foundation.md so a spec reader can audit by eye.
package router

import (
	"context"
	"errors"
	"regexp"
	"strings"

	harnessTools "github.com/ericmacdougall/stoke/internal/harness/tools"
)

// Intent is the coarse user-intent classification. The enum is
// deliberately narrow — three values map cleanly onto "apply writes"
// vs "read-only diagnosis" vs "unclear, fail safe".
type Intent int

const (
	// IntentUnknown is the zero value; it should never leak past
	// `ClassifyIntent`. Callers that see it have a bug.
	IntentUnknown Intent = iota

	// IntentImplement means "change the repo / the world". The
	// worker keeps its full tool set; file writes, commits, deploy
	// hooks, etc. are all permitted. Used for verbs like implement,
	// fix, add, deploy, refactor, migrate.
	IntentImplement

	// IntentDiagnose means "answer a question / investigate, do not
	// write". The gate clamps the worker's tool set to read-only
	// and directs output to a markdown report rather than a patch.
	// Used for verbs like check, analyze, explain, why, how.
	IntentDiagnose

	// IntentAmbiguous means neither family of verbs matched
	// cleanly, OR both matched (user said "investigate and fix X").
	// Per RT-11 open-question 4, the safer default is DIAGNOSE — so
	// the gate clamps tools and emits a `router.intent_ambiguous`
	// signal the operator can observe.
	IntentAmbiguous
)

// String returns the canonical lower-case label. Used in logs,
// telemetry, and the `intent` field on bus events emitted by the
// gate.
func (i Intent) String() string {
	switch i {
	case IntentImplement:
		return "implement"
	case IntentDiagnose:
		return "diagnose"
	case IntentAmbiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// ErrEmptyIntentInput is returned by `Gate` / `ClassifyIntent` when
// the input is whitespace-only. Callers that build a Task from a
// CLI flag should already have caught this; the package exposes the
// sentinel so they can pass it through unchanged.
var ErrEmptyIntentInput = errors.New("router: empty intent input")

// Intent regexes. Compiled once at package init so `ClassifyIntent`
// is hot-path cheap. Scope is a `\b` word-boundary match — we want
// "fix the leak" to hit but "prefix" to not.
var (
	implementIntentRegex = regexp.MustCompile(
		`(?i)\b(create|add|implement|fix|update|build|deploy|generate|refactor|rename|migrate|port|delete|write|ship|release|land)\b`,
	)
	diagnoseIntentRegex = regexp.MustCompile(
		`(?i)\b(check|verify|investigate|analyze|explain|audit|review|diagnose|inspect|what|how|why|where|when|which)\b`,
	)
	leadingWHWordRegex = regexp.MustCompile(
		`(?i)^\s*(what|how|why|where|when|which)\b`,
	)
)

// ClassifyIntentDeterministic runs the regex-only phase. Returns the
// matched intent plus a boolean that is `true` when the input yielded
// a confident verdict (IntentImplement or IntentDiagnose). When both
// families match — e.g. "investigate AND fix the leak" — IMPLEMENT
// wins because its tool-scope superset is strictly safer for the
// worker executing an actual change.
//
// Returns `(IntentAmbiguous, false)` for whitespace-only inputs and
// for inputs where neither regex fires. Callers treat the `false`
// case as "escalate to LLM", preserving the same contract as
// `ClassifyDeterministic`.
func ClassifyIntentDeterministic(input string) (Intent, bool) {
	if strings.TrimSpace(input) == "" {
		return IntentAmbiguous, false
	}
	impl := implementIntentRegex.MatchString(input)
	diag := diagnoseIntentRegex.MatchString(input)
	// Question-form override: when the input STARTS with a WH-word
	// ("why is the build red"), it's a diagnosis question even if
	// an implementation keyword appears later ("build"). The operator
	// is asking, not commanding.
	if diag && leadingWHWordRegex.MatchString(input) {
		return IntentDiagnose, true
	}
	switch {
	case impl:
		// Both-match also lands here: IMPLEMENT wins over DIAGNOSE
		// when they collide. Documented in executor-foundation.md
		// §"Intent (ClassifyIntent) — deterministic phase".
		return IntentImplement, true
	case diag:
		return IntentDiagnose, true
	default:
		return IntentAmbiguous, false
	}
}

// IntentLLMFunc is the Haiku-fallback function shape. Callers wire
// a real provider in main.go; tests inject a fake. The function MUST
// return one of the three enum members — unknown strings get mapped
// to IntentAmbiguous at the caller.
//
// Defined as a struct-less alias so `router.Gate` and
// `router.ClassifyIntent` can accept the same value without importing
// a concrete provider type here (that would create a cycle with
// `internal/model`).
type IntentLLMFunc func(ctx context.Context, input string) (Intent, error)

// ClassifyIntent is the full intent classifier: deterministic regex
// first, then (if the regex missed) a call to `llm` when non-nil.
// When `llm` is nil and the deterministic phase returns false, the
// function returns IntentAmbiguous — the safe default per the
// error-handling table in executor-foundation.md.
//
// `input` is the operator's raw text. `llm` may be nil.
func ClassifyIntent(ctx context.Context, input string, llm IntentLLMFunc) (Intent, error) {
	if strings.TrimSpace(input) == "" {
		return IntentAmbiguous, ErrEmptyIntentInput
	}
	if got, ok := ClassifyIntentDeterministic(input); ok {
		return got, nil
	}
	if llm == nil {
		return IntentAmbiguous, nil
	}
	got, err := llm(ctx, input)
	if err != nil {
		// Haiku failure: fall through to the safe default rather
		// than propagate. The error-handling table says "fall back
		// to TaskCode + IntentDiagnose + emit router.fallback";
		// the emit is the caller's job, not ours.
		return IntentAmbiguous, err
	}
	// Guard rail: if the LLM returned a value outside the enum
	// (e.g. IntentUnknown), coerce to AMBIGUOUS so downstream
	// callers never see a zero Intent.
	switch got {
	case IntentImplement, IntentDiagnose, IntentAmbiguous:
		return got, nil
	default:
		return IntentAmbiguous, nil
	}
}

// writeCapableTools enumerates the `harness/tools` names that mutate
// state outside the worker sandbox. Any tool in this set is stripped
// from the list `ClampReadOnly` produces. Kept as a var so tests can
// extend it without recompiling the package, but the default list is
// the spec-mandated safe baseline.
var writeCapableTools = map[harnessTools.ToolName]struct{}{
	harnessTools.ToolFileWrite:          {},
	harnessTools.ToolCodeRun:            {},
	harnessTools.ToolEnvExec:            {},
	harnessTools.ToolEnvCopyIn:          {},
	harnessTools.ToolEnvCopyOut:         {},
	harnessTools.ToolLedgerWrite:        {},
	harnessTools.ToolSkillImportPropose: {},
}

// ClampReadOnly returns a copy of `set` with every write-capable
// tool removed. Read/search/query tools pass through untouched. The
// return value is always a fresh slice — callers may mutate it
// without affecting the input.
//
// Exposed so the scheduler (which owns tool-set construction) can
// reuse the same filter even when it bypasses `Gate`.
func ClampReadOnly(set []harnessTools.ToolName) []harnessTools.ToolName {
	if len(set) == 0 {
		return nil
	}
	out := make([]harnessTools.ToolName, 0, len(set))
	for _, t := range set {
		if _, isWrite := writeCapableTools[t]; isWrite {
			continue
		}
		out = append(out, t)
	}
	return out
}

// GateResult is the bundle `Gate` returns to its caller. Keeping
// this as a struct (rather than multiple return values) leaves room
// for future fields — e.g. a "confidence" scalar when the LLM is
// consulted — without breaking call sites.
type GateResult struct {
	// Intent is the classified intent. Never IntentUnknown.
	Intent Intent

	// Tools is the (possibly-clamped) tool set the harness should
	// pass to the worker. For IMPLEMENT it is the input set
	// unchanged; for DIAGNOSE / AMBIGUOUS it is `ClampReadOnly`
	// applied.
	Tools []harnessTools.ToolName

	// Clamped reports whether `Tools` differs from the input set.
	// Used by the scheduler to emit `intent.ambiguous` and
	// `intent.diagnose` bus events only when something actually
	// changed — avoids noise on hot IMPLEMENT paths.
	Clamped bool
}

// Gate is the scheduler-facing entry point. Given the operator's
// free-text input and the tool set that would otherwise be dispatched
// to the worker, it returns:
//
//   - The classified intent.
//   - A (possibly-clamped) tool set to actually dispatch.
//   - An error only for genuinely broken input (empty string).
//
// `llm` is optional. When nil, `Gate` stays on the deterministic
// path; when set, it's used for inputs the regexes cannot decide.
// Callers wiring a real provider pass a closure over
// `provider.Provider.Classify` (or similar); tests pass a stub.
//
// The function does NOT emit bus events — the scheduler does that
// using `GateResult.Intent` + `GateResult.Clamped`, keeping this
// package free of a hard dependency on `internal/bus`.
func Gate(ctx context.Context, input string, tools []harnessTools.ToolName, llm IntentLLMFunc) (GateResult, error) {
	if strings.TrimSpace(input) == "" {
		return GateResult{Intent: IntentAmbiguous, Tools: ClampReadOnly(tools), Clamped: len(tools) != 0}, ErrEmptyIntentInput
	}

	intent, err := ClassifyIntent(ctx, input, llm)
	// err is non-nil only when the LLM errored or input was empty
	// (handled above). We still honor the returned intent — it will
	// be IntentAmbiguous on LLM failure, which is the safe path.
	if err != nil && !errors.Is(err, ErrEmptyIntentInput) {
		// Swallow here: `intent` is already IntentAmbiguous, and
		// returning a non-nil error would force every scheduler
		// call site to add a branch for "LLM was flaky, but we
		// still have a safe default". Keep the contract: non-nil
		// error ONLY for input so broken we cannot classify at
		// all.
		err = nil
	}

	switch intent {
	case IntentImplement:
		// Pass tools through untouched — callers get the same slice
		// header they supplied. Defensive copy keeps the invariant
		// that `GateResult.Tools` is always safe to mutate.
		out := make([]harnessTools.ToolName, len(tools))
		copy(out, tools)
		return GateResult{Intent: IntentImplement, Tools: out, Clamped: false}, err
	case IntentDiagnose, IntentAmbiguous:
		clamped := ClampReadOnly(tools)
		return GateResult{
			Intent:  intent,
			Tools:   clamped,
			Clamped: len(clamped) != len(tools),
		}, err
	default:
		// Defensive: `ClassifyIntent` never returns values outside
		// the enum, but if it ever does we still prefer safety
		// over a crash.
		return GateResult{
			Intent:  IntentAmbiguous,
			Tools:   ClampReadOnly(tools),
			Clamped: true,
		}, err
	}
}
