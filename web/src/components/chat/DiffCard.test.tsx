// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { DiffCard } from "@/components/chat/DiffCard";

// Replace the styles import so jsdom doesn't try to parse the CSS.
vi.mock("react-diff-view/style/index.css", () => ({}));

// Replace react-diff-view itself: we only need the React calls + parseDiff.
// Children for Diff/Hunk are minimal so the test stays focused on
// routing/structure rather than diff rendering.
vi.mock("react-diff-view", () => {
  return {
    parseDiff: (text: string) => {
      const files: Array<{
        oldPath: string;
        newPath: string;
        type: string;
        hunks: Array<{ content: string; changes: Array<{ type: string }> }>;
      }> = [];
      const re = /^diff --git a\/(\S+) b\/(\S+)/gm;
      let match: RegExpExecArray | null;
      while ((match = re.exec(text)) !== null) {
        files.push({
          oldPath: match[1],
          newPath: match[2],
          type: "modify",
          hunks: [
            {
              content: `@@ ${match[1]} @@`,
              changes: [{ type: "insert" }, { type: "delete" }],
            },
          ],
        });
      }
      return files;
    },
    Diff: (props: { children?: (h: unknown[]) => React.ReactNode }) => {
      const ch =
        typeof props.children === "function" ? props.children([]) : null;
      return <div data-testid="diff-stub">{ch as React.ReactNode}</div>;
    },
    Hunk: () => <div data-testid="hunk-stub">hunk</div>,
  };
});

describe("<DiffCard>", () => {
  const TWO_FILE_PATCH = [
    "diff --git a/foo.ts b/foo.ts",
    "index 1234567..89abcde 100644",
    "--- a/foo.ts",
    "+++ b/foo.ts",
    "@@ -1 +1 @@",
    "-old",
    "+new",
    "diff --git a/bar.ts b/bar.ts",
    "index abcdef0..fedcba9 100644",
    "--- a/bar.ts",
    "+++ b/bar.ts",
    "@@ -1 +1 @@",
    "-old2",
    "+new2",
  ].join("\n");

  it("renders the empty-state notice when patch is null", () => {
    render(<DiffCard id="d1" laneLabel="lane-a" patch={null} />);
    expect(screen.getByTestId("diff-card-d1-empty")).toBeTruthy();
    expect(
      screen.getByTestId("diff-card-d1").getAttribute("data-empty"),
    ).toBe("true");
  });

  it("renders one row per file with stable per-file testids", () => {
    render(<DiffCard id="d1" laneLabel="lane-a" patch={TWO_FILE_PATCH} />);
    expect(screen.getByTestId("diff-card-d1-file-0")).toBeTruthy();
    expect(screen.getByTestId("diff-card-d1-file-1")).toBeTruthy();
    expect(
      screen.getByTestId("diff-card-d1-file-0").getAttribute("data-file-path"),
    ).toBe("foo.ts");
  });

  it("counts adds + dels in the header from the parsed hunks", () => {
    render(<DiffCard id="d1" patch={TWO_FILE_PATCH} />);
    // Stub creates 1 insert + 1 delete per file => 2 + 2 across 2 files.
    expect(screen.getByTestId("diff-card-d1-additions").textContent).toBe("+2");
    expect(screen.getByTestId("diff-card-d1-deletions").textContent).toBe("−2");
    expect(screen.getByTestId("diff-card-d1-files").textContent).toBe(
      "2 files",
    );
  });

  it("toggles per-file collapse state on header click", () => {
    render(<DiffCard id="d1" patch={TWO_FILE_PATCH} />);
    const file0 = screen.getByTestId("diff-card-d1-file-0");
    expect(file0.getAttribute("data-collapsed")).toBe("false");
    fireEvent.click(screen.getByTestId("diff-card-d1-file-0-toggle"));
    expect(file0.getAttribute("data-collapsed")).toBe("true");
    expect(
      screen.getByTestId("diff-card-d1-file-0-toggle").getAttribute(
        "aria-expanded",
      ),
    ).toBe("false");
  });

  it("aria-label reports lane + counts to SR users", () => {
    render(<DiffCard id="d1" laneLabel="research" patch={TWO_FILE_PATCH} />);
    const card = screen.getByTestId("diff-card-d1");
    expect(card.getAttribute("aria-label")).toBe(
      "Diff for research, 2 files, 2 additions, 2 deletions",
    );
  });
});
