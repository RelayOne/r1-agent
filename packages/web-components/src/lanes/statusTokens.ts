// Shared status token table.
// One copy per status (per docs/decisions/index.md D-S1). Mirrored by
// the Tailwind preset in tailwind.preset.js (item 8/40).
//
// Using paired glyph + colour means colour-blind users still distinguish
// states by glyph alone — see RT-SURFACES "paired-glyph a11y" mandate.

import type { LaneStatus } from "../types/LaneEvent";

const GLYPH_TABLE: Readonly<Record<LaneStatus, string>> = Object.freeze({
  pending: "○",
  running: "◐",
  blocked: "◼",
  done: "●",
  errored: "✕",
  cancelled: "⊘",
});

const COLOR_TABLE: Readonly<Record<LaneStatus, string>> = Object.freeze({
  pending: "lane-pending",
  running: "lane-running",
  blocked: "lane-blocked",
  done: "lane-done",
  errored: "lane-errored",
  cancelled: "lane-cancelled",
});

export function statusGlyph(status: LaneStatus): string {
  return GLYPH_TABLE[status];
}

export function statusColorToken(status: LaneStatus): string {
  return COLOR_TABLE[status];
}
