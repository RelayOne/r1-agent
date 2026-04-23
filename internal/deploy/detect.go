package deploy

import (
	"os"
	"path/filepath"
	"sort"
)

// DetectResult is returned by Detect — names the chosen provider,
// lists all signals observed, and flags when the signal set is
// ambiguous so the caller can fall back to Operator.Ask instead of
// silently picking one.
//
// Provider is empty ("") when either no signals were found OR the
// signals are sufficiently conflicting (see Detect's ambiguity
// rules) that the caller must surface a choice. Ambiguous is true
// whenever multiple provider signals coexist, even if we still
// nominated a winner; the Note explains why.
type DetectResult struct {
	// Provider is the chosen provider ("fly", "vercel",
	// "cloudflare") or "" when the directory has no signals or the
	// signals are too ambiguous to resolve without operator input.
	Provider string

	// Signals are the raw signal filenames that matched during
	// detection, in deterministic sorted order so callers can diff
	// DetectResult across runs in golden tests or operator output.
	Signals []string

	// Ambiguous is true when multiple providers' signals were
	// present simultaneously. Callers running in `--auto` mode
	// should prompt (Operator.Ask) on ambiguous results even when
	// Provider is non-empty, so the operator can override our
	// tie-breaker when they disagree with it.
	Ambiguous bool

	// Note is a human-readable explanation of the tie-breaker we
	// applied (or why we gave up). Empty when no conflict was
	// observed. Intended for operator log lines, not for machine
	// parsing — the Provider + Ambiguous + Signals triplet carries
	// the structured signal.
	Note string
}

// detectSignal describes one filesystem marker we look for during
// stack detection. Keeping the table data-driven lets the test suite
// cover "every signal detects" without hand-crafting a case per
// provider, and lets DP2-7 evolve without surgery on Detect itself.
type detectSignal struct {
	// provider is the lower-case provider name emitted in
	// DetectResult.Provider / Signals when this signal matches.
	provider string

	// path is the relative file or directory path that, when
	// present inside the detect root, satisfies this signal.
	path string

	// dir is true when the signal's path is expected to be a
	// directory rather than a regular file. A trailing slash on
	// path is insufficient on Windows, so we carry this as an
	// explicit flag.
	dir bool
}

// flySignals / vercelSignals / cloudflareSignals describe the minimal
// DP2-7 signal set. The spec's §Stack Detection Extensions covers a
// richer set (Next.js config inspection, package.json dependency
// introspection, etc.); those land in a follow-up commit. This pass
// restricts itself to filesystem-only markers so Detect stays cheap,
// side-effect-free, and easy to test.
var detectSignals = []detectSignal{
	// Vercel — highest-specificity intent first. vercel.json
	// indicates the operator has explicitly configured Vercel;
	// .vercel/project.json is dropped by `vercel link` and is an
	// equally strong positive signal.
	{provider: "vercel", path: "vercel.json"},
	{provider: "vercel", path: filepath.Join(".vercel", "project.json")},

	// Cloudflare — wrangler config files (toml, json, jsonc) and
	// the .wrangler/ state directory dropped by `wrangler deploy`.
	// We accept all three config extensions because wrangler
	// itself reads whichever is present; preferring one would
	// silently hide a real signal from operators on the others.
	{provider: "cloudflare", path: "wrangler.toml"},
	{provider: "cloudflare", path: "wrangler.json"},
	{provider: "cloudflare", path: "wrangler.jsonc"},
	{provider: "cloudflare", path: ".wrangler", dir: true},

	// Fly — fly.toml is the canonical marker. If an operator has
	// Fly configured they will always have fly.toml checked in; a
	// missing file on a Fly-deployed repo is a genuine bug and
	// should fall through to "no signals" so the caller re-asks.
	{provider: "fly", path: "fly.toml"},
}

// Detect walks dir looking for provider signal files and returns the
// chosen provider with full signal provenance.
//
// Precedence per specs/deploy-phase2.md §Provider Selection Matrix:
//
//  1. Vercel (vercel.json / .vercel/project.json) beats Fly. Cloudflare
//     + Vercel together is genuinely ambiguous — both are serverless
//     edge platforms with overlapping use-cases and we refuse to
//     guess.
//  2. Cloudflare (wrangler.* / .wrangler/) beats Fly. wrangler config
//     is explicit intent; a leftover fly.toml from an old deploy
//     should not override it.
//  3. Fly (fly.toml) is the fallback whenever no higher-precedence
//     signals are present.
//
// Detect does no subprocess I/O, no network I/O, and no filesystem
// writes — only os.Stat on candidate paths. It is safe to call from
// any context, in parallel, and on a read-only directory.
//
// Callers pass an empty dir to detect "here"; Detect resolves that to
// the process cwd via filepath.Abs so diagnostics show a concrete
// path. A non-existent dir yields a zero-value DetectResult with
// empty Provider rather than an error — callers keep the simple
// "nothing detected" branch instead of switching on io/fs error
// kinds.
func Detect(dir string) DetectResult {
	if dir == "" {
		dir = "."
	}

	// presence[provider] → sorted signal paths that matched.
	presence := map[string][]string{}

	for _, sig := range detectSignals {
		full := filepath.Join(dir, sig.path)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if sig.dir && !info.IsDir() {
			continue
		}
		if !sig.dir && info.IsDir() {
			// A file-expected signal that resolved to a
			// directory (e.g. someone created a vercel.json/
			// directory by mistake) is not a real match; we
			// skip rather than silently promote the miss.
			continue
		}
		presence[sig.provider] = append(presence[sig.provider], sig.path)
	}

	hasVercel := len(presence["vercel"]) > 0
	hasCloudflare := len(presence["cloudflare"]) > 0
	hasFly := len(presence["fly"]) > 0

	result := DetectResult{Signals: flattenSignals(presence)}

	switch {
	case !hasVercel && !hasCloudflare && !hasFly:
		// Empty directory (or directory with no provider
		// markers). Leave Provider="" so the caller falls back
		// to Operator.Ask; Note stays empty because there is
		// nothing to explain.
		return result

	case hasVercel && hasCloudflare:
		// Vercel + Cloudflare together: both are explicit,
		// high-specificity intent and we have no tiebreaker
		// strong enough to pick one without asking. We do NOT
		// guess; the caller must Ask. hasFly is not called out
		// in the Note because it is strictly lower-precedence
		// noise in this state.
		result.Ambiguous = true
		result.Note = "both present — caller must Ask"
		return result

	case hasVercel && hasFly:
		// Vercel beats Fly per §Provider Selection Matrix, but
		// we flag Ambiguous=true so the CLI can warn that the
		// operator may have intended Fly. The note names the
		// winning file and the ignored file so operators can
		// delete one or the other to silence the warning.
		result.Provider = "vercel"
		result.Ambiguous = true
		result.Note = "vercel.json takes precedence; fly.toml present but ignored"
		return result

	case hasVercel:
		// Vercel alone — unambiguous.
		result.Provider = "vercel"
		return result

	case hasCloudflare && hasFly:
		// Wrangler config is explicit Cloudflare intent and
		// beats a leftover fly.toml. Ambiguous=false here
		// because the spec treats this case as deterministic
		// (unlike vercel+fly which also warns).
		result.Provider = "cloudflare"
		result.Note = "wrangler config takes precedence"
		return result

	case hasCloudflare:
		result.Provider = "cloudflare"
		return result

	case hasFly:
		result.Provider = "fly"
		return result
	}

	// Unreachable — the switch above is exhaustive over the
	// three boolean signals. Return zero-value rather than panic
	// so an unexpected code path in the future degrades gracefully.
	return result
}

// flattenSignals collects the per-provider signal slices into a
// single sorted slice for DetectResult.Signals. Sorting keeps the
// field diff-stable across runs even as map iteration order in the
// presence map shifts.
func flattenSignals(presence map[string][]string) []string {
	var out []string
	for _, sigs := range presence {
		out = append(out, sigs...)
	}
	sort.Strings(out)
	return out
}
