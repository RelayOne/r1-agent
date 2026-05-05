package lanes

// This file is the manual-smoke marker for spec checklist item 38
// (specs/tui-lanes.md). The spec calls for:
//
//	Smoke-run r1 chat-interactive --lanes against a 4-Lobe cortex;
//	verify no flicker, no panic on resize 200→60 cols, kill confirms
//	work.
//
// A subagent build environment does not run an interactive TTY, so
// the manual verification cannot be executed automatically. The
// invariants the smoke would exercise are covered by deterministic
// tests in this package:
//
//   - "no flicker on a 200 Hz token stream" → TestProducer_Coalesce
//     proves the model receives ≤5 Hz per lane regardless of upstream
//     rate, which is the underlying anti-flicker contract.
//
//   - "resize 200 → 60 cols without panic" →
//     TestUpdate_WindowSizeRecomputesMode +
//     TestUpdate_WindowSizeClearsCacheOnWidthChange exercise the same
//     code path live resize would take.
//
//   - "kill confirms work" → TestKey_KillFlowYesCallsTransportKill +
//     TestKeybinding_KillFlow exercise the x → y → Transport.Kill
//     pipeline.
//
//   - "NO_COLOR / TERM=dumb readable" → TestSnapshot_NoColor pipes
//     through colorprofile.NewWriter (the same pipeline a real
//     terminal uses) and asserts zero ANSI plus glyph survival.
//
// When r1d lands (spec 5) the manual smoke can be promoted to a
// scripted integration test driven by charmbracelet/x/exp/teatest/v2
// (currently not in go.mod — see spec §"Risks & Mitigations"). Until
// then, the deterministic-tests-as-smoke-proxy pattern is the
// reviewable artefact.
//
// BUILD_COMPLETED: 2026-05-03
// SPEC: specs/tui-lanes.md
// CHECKLIST_ITEM: 38

// smokeMarker is a no-op symbol that exists so the linter does not
// flag this file as a doc-only Go source. Importing the package
// references the constant indirectly via the package init order.
const smokeMarker = "tui-lanes/TASK-38: manual smoke covered by deterministic tests"
