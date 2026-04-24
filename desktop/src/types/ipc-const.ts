// SPDX-License-Identifier: MIT
//
// Runtime-value companion to `ipc.d.ts`.
//
// `.d.ts` files may only declare types. The canonical scope and tier
// arrays are consumed at runtime by panel code (e.g., the memory
// inspector iterates over ALL_MEMORY_SCOPES to render one row per
// scope), so they live in this `.ts` file.
//
// Ordering in each array mirrors the Go-side `AllMemoryScopes()` /
// `AllDescentTiers()` helpers so UI order matches backend order.

import type { DescentTier, MemoryScope } from "./ipc";

export const ALL_MEMORY_SCOPES: MemoryScope[] = [
  "Session",
  "Worker",
  "AllSessions",
  "Global",
  "Always",
];

export const ALL_DESCENT_TIERS: DescentTier[] = [
  "T1",
  "T2",
  "T3",
  "T4",
  "T5",
  "T6",
  "T7",
  "T8",
];
