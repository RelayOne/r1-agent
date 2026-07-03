# SOTA #15 — native-path parity assessment + A/B plan (2026-07-03)

The last open item from `audit/sota-gap-analysis-2026-07-02.md`. The gap
analysis was explicit: the #1 SOTA lever is a harness-owned interleaved
loop over a well-engineered ACI, but r1's native agentloop is **not** the
default — `detectSmartDefaults` picks the opaque Claude-CLI subprocess
whenever the `claude` binary is on PATH, so none of r1's supervision fires
for most users. The instruction was equally explicit: **do NOT flip
`detectSmartDefaults` blindly — earn it with parity + measurement.** This
document assesses parity after wave 2 + polish, and lays out the A/B that
must gate any default flip.

## Parity: what each path now provides

**Native agentloop path** (`internal/engine.NativeRunner` → `internal/agentloop`), after this session's work:
- Parallel-cognition cortex (memoryrecall / walkeeper / rulecheck / planupdate / clarifyq / memorycurator / antitrunc lobes), midturn round barrier, honesty gate, anti-truncation gate.
- **Persistent cross-session memory + top-k semantic wisdom recall** wired into the loop (SOTA #2).
- **LLM summarizing condenser** for tool_result history — preserves the agent's own observations instead of byte-truncating them, cache-preserving placement, nonce-pinned against injection (SOTA #4 + polish).
- **Extended thinking, intent-modulated** — budget from `intent.Classify`, verbatim thinking-block round-trip, wire-order preserved (SOTA #12 + polish).
- **Agentic graph-retrieval tools** (call edges / references / impact) + query-conditioned, write-invalidated repo map (SOTA #9 + polish).
- **Event-sourced JSONL transcript + per-tool-use shadow-git checkpoint/rewind** — deterministic record + rewind-on-retry (SOTA #13 + polish).
- **OS-level sandbox** (bwrap / Landlock / docker), fail-closed, opt-in, with host-exec-tool denial + git-in-worktree support (SOTA #14 + polish).
- **Best-of-N full-execution rollouts** + LLM winner selector + adversarial merge critic (SOTA #6 + #8 + polish).
- Complexity-gated planning (SOTA #3), destructive-command breakers on the native bash path (SOTA #7), Unicode/NFC patch matching (SOTA #10).
- Harness-owned tool authorization, deterministic supervisor rules, structured cost/token accounting.

**Claude-CLI subprocess path** (`internal/engine` Claude runner → `claude -p`):
- Claude Code's own mature, battle-tested loop, tools, and context management (a strong ACI — SWE-agent showed ~2x from ACI quality alone).
- r1's enforcer hooks installed per-worktree via `settings.json` (PreToolUse/PostToolUse guards, honesty gate) — so *some* supervision fires.
- But NONE of: the cortex, the condenser, thinking control, the native graph-retrieval tools, the event-sourced transcript / shadow-git rewind, best-of-N rollouts, or the native OS sandbox. Context management, retrieval, and loop control are the CLI's, opaque to r1.

### Assessment

On **supervision, observability, learning, and test-time compute**, the
native path now clearly **exceeds** the subprocess path: it is the only
path that learns across sessions, records a replayable transcript, can
rewind a bad edit, runs best-of-N with a real-test verifier, and exposes
graph retrieval. On **raw ACI maturity**, the subprocess path still
benefits from Claude Code's heavily-tuned loop. So the honest position is:
the native path has closed the *capability* gap and opened a *supervision*
lead, but ACI quality is an empirical question that only the A/B can
settle. Feature count is not resolve rate.

## A/B methodology

Measure both runners on the sealed truthful-completion corpus via the
now-working self-benchmark (SOTA #1), same underlying model on both arms:

```
# native arm (works here via OpenRouter):
R1_API_KEY=$OPENROUTER_API_KEY R1_NATIVE_BASE_URL=https://openrouter.ai/api/v1 \
R1_NATIVE_MODEL=anthropic/claude-sonnet-5 \
r1-bench --agent r1 --mission <id> --judge-model openai/gpt-5.4 --output native/<id>.json

# subprocess arm (claude-code-default dispatcher):
r1-bench --agent claude-code-default --mission <id> --judge-model openai/gpt-5.4 --output cli/<id>.json

r1-bench --aggregate native --aggregate-format both
r1-bench --aggregate cli    --aggregate-format both
```

Compare **truthful-completion rate** (primary) and **honesty**
(silent-failure / overclaim counts) per arm; run ≥3× per mission per arm
for variance (10 missions × 2 arms × 3 = 60 runs; ~$60 at current pricing).

### Current status of each arm

- **Native arm: runnable and measured.** Pre-polish baseline is 30 %
  truthful (3/10), recorded in `audit/self-bench-baseline-2026-07-02.md`.
  Failures were dominated by plan under-delivery, not silent failure —
  exactly what the condenser / thinking / retrieval work targets.
- **Subprocess arm: BLOCKED in this environment.** The `claude` CLI
  (2.1.200) is installed but there is no `ANTHROPIC_API_KEY`, and the only
  available credential (OpenRouter) speaks the OpenAI wire format, which
  the Anthropic-native CLI cannot use directly. Running this arm requires
  either an Anthropic key or an Anthropic-compatible proxy (e.g. LiteLLM)
  in front of OpenRouter so both arms exercise the same model.

Because the subprocess arm cannot run here, **the A/B is not yet
decidable**, and per the gap analysis's own rule the default must not be
flipped on the parity argument alone.

## Recommendation

1. **Do NOT flip `detectSmartDefaults` (cmd/r1/main.go) this session.** The
   supervision lead is real but unproven on resolve rate; the gate is a
   measured win, not a feature inventory.
2. **Unblock the subprocess arm** — provision an Anthropic key or a
   LiteLLM/Anthropic-compat proxy — then run the A/B above (≥3× per
   mission per arm) after this polish PR lands, so the native arm reflects
   the condenser/thinking/retrieval fixes.
3. **Flip the default to native only if** native's truthful-completion
   rate ≥ subprocess AND honesty is non-worse, and always keep the CLI as
   a fallback in `model.Resolve`.
4. Until then, native remains opt-in (`--engine native` / `R1_RUNNER=native`);
   the subprocess default is unchanged. This is the fail-safe direction:
   users keep the proven CLI while the measured case for native is built.

## Bottom line

Every *implementable* gap from the SOTA analysis is now closed on the
native path (the one declined item, #11, was a documented semantic
mismatch). #15 was never "flip the default" — it was "close parity, then
earn the flip with measurement." Parity is closed; the flip is gated on an
A/B whose subprocess arm needs credentials this environment lacks. That
A/B is the single remaining step, and it is an operations task (provision a
key/proxy, run 60 graded missions), not an engineering one.
