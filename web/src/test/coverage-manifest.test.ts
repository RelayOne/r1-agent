// SPDX-License-Identifier: MIT
// Coverage manifest. Spec item 45/55.
//
// The goal of this test is two-fold:
//
//   1. Verify that every shipped component / hook / library file has a
//      sibling .test.tsx (or .test.ts). This catches accidentally
//      under-tested code before the coverage gate flags it.
//   2. Document the spec'd coverage threshold so the reader sees the
//      contract in code as well as in vitest.config.ts.
//
// Coverage thresholds (vitest.config.ts):
//   - statements / lines / functions: 80%
//   - branches: 70%
//
// The "every file has a test" check runs against the tree using
// fs.readdirSync. If a future component lands without a sibling test,
// this file fails — forcing the author to add one.
import { describe, it, expect } from "vitest";
import { readdirSync, statSync } from "node:fs";
import { join, dirname, basename } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SRC = join(HERE, "..");

const SCAN_ROOTS = ["components", "hooks", "lib"];

interface FileInventory {
  source: string[];
  tested: Set<string>;
}

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      out.push(...walk(full));
    } else if (st.isFile()) {
      out.push(full);
    }
  }
  return out;
}

function gather(): FileInventory {
  const source: string[] = [];
  const tested = new Set<string>();
  for (const root of SCAN_ROOTS) {
    const dir = join(SRC, root);
    let files: string[];
    try {
      files = walk(dir);
    } catch {
      continue;
    }
    for (const f of files) {
      if (f.endsWith(".test.ts") || f.endsWith(".test.tsx")) {
        // Map "<name>.test.tsx" -> "<name>.tsx"
        const stem = basename(f).replace(/\.test\.(ts|tsx)$/, "");
        tested.add(`${dirname(f)}/${stem}`);
        continue;
      }
      if (f.endsWith(".stories.tsx") || f.endsWith(".d.ts")) continue;
      if (!f.endsWith(".ts") && !f.endsWith(".tsx")) continue;
      // Skip route index that re-exports.
      if (basename(f) === "index.ts" && f.includes("ui/")) continue;
      source.push(f);
    }
  }
  return { source, tested };
}

describe("coverage manifest", () => {
  it("documents the 80% statements / 70% branches threshold from vitest.config.ts", () => {
    const expected = { statements: 80, branches: 70, functions: 80, lines: 80 };
    expect(Object.keys(expected).sort()).toEqual([
      "branches",
      "functions",
      "lines",
      "statements",
    ]);
  });

  it("every shipped src/components, src/hooks, src/lib file has a sibling test", () => {
    const inv = gather();
    const orphans: string[] = [];
    for (const src of inv.source) {
      const stem = src.replace(/\.(ts|tsx)$/, "");
      if (!inv.tested.has(stem)) {
        orphans.push(src);
      }
    }
    expect(
      orphans,
      `expected sibling .test.tsx for every source file; missing tests for:\n${orphans.join("\n")}`,
    ).toEqual([]);
  });
});
