// SPDX-License-Identifier: MIT
// <ThreeColumnShell> — top-level page layout. Spec item 22/55.
//
// Three regions: left rail (sessions), center (chat / tile grid), and
// right rail (lanes). Either rail collapses to a 40 px strip with a
// single expand button. Collapse state is read from the per-daemon
// zustand store (`ui.leftRailCollapsed` / `ui.rightRailCollapsed`),
// so it persists across mounts the same way the rest of the daemon
// state does and survives a daemon switch.
//
// We use CSS grid rather than a true draggable Resizable; the spec
// calls for "shadcn Resizable-style" which the shadcn registry models
// as a thin wrapper over `react-resizable-panels`. We avoid pulling in
// that dep from this item — pin policy from items 9-10 — and instead
// give two explicit widths plus the collapse toggle. A future item can
// promote this to a draggable splitter without touching consumers.
import { useStore } from "zustand";
import type { ReactElement, ReactNode } from "react";
import type { DaemonStore } from "@/lib/store/daemonStore";
import { cn } from "@/lib/utils";

export interface ThreeColumnShellProps {
  store: DaemonStore;
  /** Left rail content — typically <SessionList>. */
  left: ReactNode;
  /** Center content — typically <ChatPane>. */
  center: ReactNode;
  /** Right rail content — typically <LanesSidebar>. */
  right: ReactNode;
  /** Optional override for left rail expanded width (Tailwind size class). */
  leftWidthClass?: string;
  /** Optional override for right rail expanded width (Tailwind size class). */
  rightWidthClass?: string;
}

const COLLAPSED_WIDTH = "w-10";
const DEFAULT_LEFT_WIDTH = "w-64";
const DEFAULT_RIGHT_WIDTH = "w-72";

export function ThreeColumnShell({
  store,
  left,
  center,
  right,
  leftWidthClass = DEFAULT_LEFT_WIDTH,
  rightWidthClass = DEFAULT_RIGHT_WIDTH,
}: ThreeColumnShellProps): ReactElement {
  const leftCollapsed = useStore(store, (s) => s.ui.leftRailCollapsed);
  const rightCollapsed = useStore(store, (s) => s.ui.rightRailCollapsed);
  const setLeft = useStore(store, (s) => s.setLeftRailCollapsed);
  const setRight = useStore(store, (s) => s.setRightRailCollapsed);

  return (
    <div
      className="flex h-full w-full bg-background text-foreground"
      data-testid="three-column-shell"
    >
      <aside
        role="navigation"
        aria-label="Sessions rail"
        data-testid="three-column-shell-left"
        data-collapsed={leftCollapsed ? "true" : "false"}
        className={cn(
          "flex flex-col border-r border-border shrink-0 transition-[width] duration-150",
          leftCollapsed ? COLLAPSED_WIDTH : leftWidthClass,
        )}
      >
        <div className="flex items-center justify-between p-1 border-b border-border">
          <button
            type="button"
            onClick={() => setLeft(!leftCollapsed)}
            data-testid="left-rail-toggle"
            aria-label={
              leftCollapsed ? "Expand sessions rail" : "Collapse sessions rail"
            }
            aria-expanded={!leftCollapsed}
            className="px-2 py-1 text-sm rounded-md hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {leftCollapsed ? "›" : "‹"}
          </button>
        </div>
        {leftCollapsed ? null : (
          <div className="flex-1 overflow-hidden">{left}</div>
        )}
      </aside>

      <main
        role="main"
        aria-label="Chat"
        data-testid="three-column-shell-center"
        className="flex-1 min-w-0 overflow-hidden"
      >
        {center}
      </main>

      <aside
        role="complementary"
        aria-label="Lanes rail"
        data-testid="three-column-shell-right"
        data-collapsed={rightCollapsed ? "true" : "false"}
        className={cn(
          "flex flex-col border-l border-border shrink-0 transition-[width] duration-150",
          rightCollapsed ? COLLAPSED_WIDTH : rightWidthClass,
        )}
      >
        <div className="flex items-center justify-between p-1 border-b border-border">
          <button
            type="button"
            onClick={() => setRight(!rightCollapsed)}
            data-testid="right-rail-toggle"
            aria-label={
              rightCollapsed ? "Expand lanes rail" : "Collapse lanes rail"
            }
            aria-expanded={!rightCollapsed}
            className="px-2 py-1 text-sm rounded-md hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {rightCollapsed ? "‹" : "›"}
          </button>
        </div>
        {rightCollapsed ? null : (
          <div className="flex-1 overflow-hidden">{right}</div>
        )}
      </aside>
    </div>
  );
}
