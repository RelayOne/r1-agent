// SPDX-License-Identifier: MIT
// <DiffCard> — consolidated per-lane diff. Spec item 31/55.
//
// Renders a unified-diff string through `react-diff-view`. The card
// header shows the lane label, file count, and added/removed line
// counts. Each file is collapsible (default expanded; toggle persists
// per-card). Empty diffs render an explicit "no changes" notice so
// the surface is never silently blank.
//
// We accept the diff in unified-diff format (`patch` string). The
// parent (typically a per-lane summary panel) is responsible for
// concatenating each file's patch into one string before passing it
// in; we do not fetch.
import { useMemo, useState } from "react";
import type { ReactElement } from "react";
import { Diff, Hunk, parseDiff, type FileData, type HunkData } from "react-diff-view";
import "react-diff-view/style/index.css";
import { ChevronDown, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

export interface DiffCardProps {
  /** Required for stable testids when multiple cards are mounted. */
  id: string;
  /** Lane label shown in the header (e.g., "lane-a"). */
  laneLabel?: string;
  /** Unified-diff text. May contain multiple files; pass empty/undefined
   *  for the "no changes" state. */
  patch?: string | null;
  /** Theme override for storybook / test isolation. */
  viewType?: "split" | "unified";
}

interface ParsedFile extends FileData {
  hunks: HunkData[];
}

function safeParse(patch: string | null | undefined): ParsedFile[] {
  if (!patch || patch.trim().length === 0) return [];
  try {
    return parseDiff(patch) as ParsedFile[];
  } catch {
    return [];
  }
}

function countChanges(files: ParsedFile[]): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const f of files) {
    for (const h of f.hunks ?? []) {
      for (const c of h.changes ?? []) {
        if (c.type === "insert") add += 1;
        else if (c.type === "delete") del += 1;
      }
    }
  }
  return { add, del };
}

export function DiffCard({
  id,
  laneLabel,
  patch,
  viewType = "unified",
}: DiffCardProps): ReactElement {
  const files = useMemo(() => safeParse(patch), [patch]);
  const counts = useMemo(() => countChanges(files), [files]);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  const testid = `diff-card-${id}`;

  if (files.length === 0) {
    return (
      <section
        data-testid={testid}
        data-empty="true"
        aria-label={`Diff for ${laneLabel ?? "lane"}, no changes`}
        className="rounded-md border border-border"
      >
        <header className="flex items-center gap-2 px-2 py-1.5 text-xs bg-muted/40">
          <span className="font-semibold uppercase tracking-wide text-muted-foreground">
            diff
          </span>
          {laneLabel ? (
            <span className="font-mono text-muted-foreground">{laneLabel}</span>
          ) : null}
        </header>
        <p
          className="p-3 text-xs text-muted-foreground"
          data-testid={`${testid}-empty`}
        >
          No changes.
        </p>
      </section>
    );
  }

  return (
    <section
      data-testid={testid}
      data-files={files.length}
      data-add={counts.add}
      data-del={counts.del}
      aria-label={`Diff for ${laneLabel ?? "lane"}, ${files.length} files, ${counts.add} additions, ${counts.del} deletions`}
      className="rounded-md border border-border overflow-hidden"
    >
      <header
        className="flex items-center gap-3 px-2 py-1.5 text-xs bg-muted/40"
        data-testid={`${testid}-header`}
      >
        <span className="font-semibold uppercase tracking-wide text-muted-foreground">
          diff
        </span>
        {laneLabel ? (
          <span className="font-mono text-muted-foreground">{laneLabel}</span>
        ) : null}
        <span className="ml-auto" />
        <span data-testid={`${testid}-files`}>{files.length} files</span>
        <span
          className="text-emerald-500"
          data-testid={`${testid}-additions`}
        >
          +{counts.add}
        </span>
        <span className="text-rose-500" data-testid={`${testid}-deletions`}>
          −{counts.del}
        </span>
      </header>

      <ol className="m-0 p-0 list-none divide-y divide-border">
        {files.map((file, i) => {
          const fileId = file.newPath || file.oldPath || `file-${i}`;
          const isCollapsed = collapsed[fileId] === true;
          const fileTestId = `${testid}-file-${i}`;
          return (
            <li
              key={fileId}
              data-testid={fileTestId}
              data-file-path={fileId}
              data-collapsed={isCollapsed ? "true" : "false"}
              className={cn(
                file.type === "delete" && "bg-rose-500/5",
                file.type === "add" && "bg-emerald-500/5",
              )}
            >
              <div className="flex items-center gap-2 px-2 py-1 text-xs">
                <button
                  type="button"
                  onClick={() =>
                    setCollapsed((prev) => ({
                      ...prev,
                      [fileId]: !prev[fileId],
                    }))
                  }
                  data-testid={`${fileTestId}-toggle`}
                  aria-label={
                    isCollapsed ? `Expand ${fileId}` : `Collapse ${fileId}`
                  }
                  aria-expanded={!isCollapsed}
                  className="p-0.5 rounded hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {isCollapsed ? (
                    <ChevronRight className="w-3 h-3" aria-hidden="true" />
                  ) : (
                    <ChevronDown className="w-3 h-3" aria-hidden="true" />
                  )}
                </button>
                <span className="font-mono">{fileId}</span>
                {file.type ? (
                  <span className="text-muted-foreground">[{file.type}]</span>
                ) : null}
              </div>
              {!isCollapsed && file.hunks?.length > 0 ? (
                <div className="text-xs overflow-x-auto">
                  <Diff
                    viewType={viewType}
                    diffType={file.type ?? "modify"}
                    hunks={file.hunks}
                  >
                    {(hunks: HunkData[]) =>
                      hunks.map((h: HunkData) => <Hunk key={h.content} hunk={h} />)
                    }
                  </Diff>
                </div>
              ) : null}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
