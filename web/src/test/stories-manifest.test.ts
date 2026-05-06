// SPDX-License-Identifier: MIT
// Storybook stories manifest. Spec item 47/55.
//
// Asserts that every shipped component (under src/components, except
// the shadcn primitives in src/components/ui/) has a sibling
// .stories.tsx file. The Storybook MCP runner shipped by spec 8
// reads these stories to drive component contracts; without them,
// the harness lint-view-without-api tool flags the surface as
// untested.
//
// Hooks (src/hooks) and library code (src/lib) do not need stories
// because they don't render. We intentionally scope this to
// src/components and explicitly exclude src/components/ui (shadcn
// primitives — the upstream registry already ships their stories).
import { describe, it, expect } from "vitest";
import { readdirSync, statSync } from "node:fs";
import { join, dirname, basename } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const COMPONENTS = join(HERE, "..", "components");

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

interface Inventory {
  source: string[];
  withStories: Set<string>;
}

function gather(): Inventory {
  let files: string[];
  try {
    files = walk(COMPONENTS);
  } catch {
    return { source: [], withStories: new Set() };
  }
  const source: string[] = [];
  const withStories = new Set<string>();
  for (const f of files) {
    // Skip shadcn primitives
    if (f.includes(`${"/"}ui${"/"}`)) continue;
    if (f.endsWith(".stories.tsx")) {
      const stem = basename(f).replace(/\.stories\.tsx$/, "");
      withStories.add(`${dirname(f)}/${stem}`);
      continue;
    }
    if (f.endsWith(".test.tsx") || f.endsWith(".test.ts")) continue;
    if (!f.endsWith(".tsx")) continue;
    source.push(f);
  }
  return { source, withStories };
}

describe("stories manifest", () => {
  it("every src/components/**/*.tsx (excluding shadcn ui/) has a sibling .stories.tsx", () => {
    const inv = gather();
    const orphans: string[] = [];
    for (const src of inv.source) {
      const stem = src.replace(/\.tsx$/, "");
      if (!inv.withStories.has(stem)) orphans.push(src);
    }
    expect(
      orphans,
      `expected sibling .stories.tsx for every component; missing stories for:\n${orphans.join("\n")}`,
    ).toEqual([]);
  });
});
