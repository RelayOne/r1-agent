// SPDX-License-Identifier: MIT
// <TileGrid> — pinned lane grid with auto-layout + reorder. Spec item 36/55.
//
// Behavior summary:
//   - Auto layout: 1 tile → 1 col, 2 → 1×2, 3 → 1×3, 4+ → 2×2 (with
//     overflow tiles wrapping into new rows). Driven by Tailwind grid
//     classes derived from tileIds.length.
//   - Per-tile collapse toggle: chevron in tile header. Collapsed tile
//     shows only the 32 px header strip; the grid uses
//     `grid-auto-rows: minmax(min-content, 1fr)` so collapsed rows
//     don't claim equal vertical space.
//   - HTML5 drag-and-drop reorder: dragging a tile header swaps with
//     the drop target. `aria-grabbed` / `aria-dropeffect` are set on
//     the source / target during the drag for SR users.
//   - Cmd+Shift+←/→ moves the focused tile left/right (keyboard
//     alternative to drag-and-drop, per WCAG 2.1.1).
//   - Double-click a header → onFocusLane(laneId) (route pop-out).
//   - Unpin button per tile (delegates to onUnpin).
//
// Persisted state (zustand `ui` slice):
//   - tilePinnedBySession (the order — written via reorderTiles).
//   - tileCollapsedBySession (the per-lane collapsed flag).
import { useEffect, useRef, useState } from "react";
import type { ReactElement, KeyboardEvent } from "react";
import { useStore } from "zustand";
import { ChevronDown, ChevronRight, PinOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { LaneTile } from "./LaneTile";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { LaneId, SessionId } from "@/lib/api/types";
import { cn } from "@/lib/utils";

function gridColsClass(n: number): string {
  if (n <= 1) return "grid-cols-1";
  if (n === 2) return "grid-cols-1 md:grid-cols-2";
  if (n === 3) return "grid-cols-1 md:grid-cols-3";
  return "grid-cols-2";
}

export interface TileGridProps {
  store: DaemonStore;
  sessionId: SessionId;
  /** Caller usually wires this to react-router's navigate(`/sessions/.../lanes/...`). */
  onFocusLane?: (laneId: LaneId) => void;
  onKill: (laneId: LaneId) => void;
}

export function TileGrid({
  store,
  sessionId,
  onFocusLane,
  onKill,
}: TileGridProps): ReactElement {
  const tileIds = useStore(
    store,
    (s) => s.ui.tilePinnedBySession[sessionId] ?? [],
  );
  const collapsedMap = useStore(
    store,
    (s) => s.ui.tileCollapsedBySession[sessionId] ?? {},
  );
  const reorderTiles = useStore(store, (s) => s.reorderTiles);
  const toggleCollapsed = useStore(store, (s) => s.toggleTileCollapsed);
  const unpinLane = useStore(store, (s) => s.unpinLane);

  const [draggingId, setDraggingId] = useState<LaneId | null>(null);
  const [dropTargetId, setDropTargetId] = useState<LaneId | null>(null);
  const [focusedIndex, setFocusedIndex] = useState<number>(0);

  const tileRefs = useRef<Record<LaneId, HTMLElement | null>>({});

  // Keep focused index in range when tiles change.
  useEffect(() => {
    if (tileIds.length === 0) return;
    if (focusedIndex >= tileIds.length) setFocusedIndex(tileIds.length - 1);
  }, [tileIds.length, focusedIndex]);

  const moveTile = (from: number, to: number): void => {
    if (to < 0 || to >= tileIds.length) return;
    if (from === to) return;
    const next = tileIds.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    reorderTiles(sessionId, next);
    setFocusedIndex(to);
    queueMicrotask(() => {
      tileRefs.current[moved]?.focus();
    });
  };

  const onHeaderKeyDown = (
    e: KeyboardEvent<HTMLElement>,
    laneId: LaneId,
    index: number,
  ): void => {
    const cmd = e.metaKey || e.ctrlKey;
    if (cmd && e.shiftKey && e.key === "ArrowLeft") {
      e.preventDefault();
      moveTile(index, index - 1);
      return;
    }
    if (cmd && e.shiftKey && e.key === "ArrowRight") {
      e.preventDefault();
      moveTile(index, index + 1);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      onFocusLane?.(laneId);
    }
  };

  if (tileIds.length === 0) {
    return (
      <div
        data-testid="tile-grid-empty"
        role="status"
        className="h-full w-full flex items-center justify-center text-sm text-muted-foreground"
      >
        Pin a lane from the right rail to start a tile view.
      </div>
    );
  }

  return (
    <div
      data-testid="tile-grid"
      data-tile-count={tileIds.length}
      role="region"
      aria-label="Pinned lanes"
      className={cn(
        "h-full w-full grid gap-2 p-2",
        gridColsClass(tileIds.length),
      )}
      style={{ gridAutoRows: "minmax(min-content, 1fr)" }}
    >
      {tileIds.map((laneId, index) => {
        const collapsed = collapsedMap[laneId] === true;
        const isDragging = draggingId === laneId;
        const isDropTarget = dropTargetId === laneId && draggingId !== laneId;
        return (
          <article
            key={laneId}
            ref={(el) => {
              tileRefs.current[laneId] = el;
            }}
            data-testid={`tile-grid-tile-${laneId}`}
            data-collapsed={collapsed ? "true" : "false"}
            data-dragging={isDragging ? "true" : "false"}
            data-drop-target={isDropTarget ? "true" : "false"}
            tabIndex={0}
            onFocus={() => setFocusedIndex(index)}
            className={cn(
              "rounded-md border border-border overflow-hidden flex flex-col",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              collapsed && "h-8",
              isDragging && "opacity-50",
              isDropTarget && "ring-2 ring-primary",
            )}
          >
            <header
              draggable
              onDragStart={(e) => {
                e.dataTransfer.effectAllowed = "move";
                e.dataTransfer.setData("text/plain", laneId);
                setDraggingId(laneId);
              }}
              onDragEnd={() => {
                setDraggingId(null);
                setDropTargetId(null);
              }}
              onDragOver={(e) => {
                if (draggingId && draggingId !== laneId) {
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                  setDropTargetId(laneId);
                }
              }}
              onDragLeave={() => {
                if (dropTargetId === laneId) setDropTargetId(null);
              }}
              onDrop={(e) => {
                e.preventDefault();
                const sourceId = e.dataTransfer.getData("text/plain");
                const fromIdx = tileIds.indexOf(sourceId);
                const toIdx = tileIds.indexOf(laneId);
                if (fromIdx >= 0 && toIdx >= 0) moveTile(fromIdx, toIdx);
                setDraggingId(null);
                setDropTargetId(null);
              }}
              onDoubleClick={() => onFocusLane?.(laneId)}
              onKeyDown={(e) => onHeaderKeyDown(e, laneId, index)}
              data-testid={`tile-grid-tile-${laneId}-header`}
              aria-grabbed={isDragging ? "true" : "false"}
              aria-dropeffect={isDropTarget ? "move" : "none"}
              aria-label={`Tile ${laneId}, double-click to focus, Cmd+Shift+Arrow to move`}
              tabIndex={-1}
              className={cn(
                "flex items-center gap-2 px-2 h-8 text-xs bg-muted/40 cursor-grab select-none",
                isDragging && "cursor-grabbing",
              )}
            >
              <button
                type="button"
                onClick={(ev) => {
                  ev.stopPropagation();
                  toggleCollapsed(sessionId, laneId);
                }}
                data-testid={`tile-grid-tile-${laneId}-collapse`}
                aria-label={collapsed ? `Expand tile ${laneId}` : `Collapse tile ${laneId}`}
                aria-expanded={!collapsed}
                className="p-0.5 rounded hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {collapsed ? (
                  <ChevronRight className="w-3 h-3" aria-hidden="true" />
                ) : (
                  <ChevronDown className="w-3 h-3" aria-hidden="true" />
                )}
              </button>
              <span className="font-mono font-semibold truncate">{laneId}</span>
              <span className="ml-auto" />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                onClick={(ev) => {
                  ev.stopPropagation();
                  unpinLane(sessionId, laneId);
                }}
                data-testid={`tile-grid-tile-${laneId}-unpin`}
                aria-label={`Unpin tile ${laneId}`}
                className="h-6 w-6"
              >
                <PinOff className="w-3 h-3" aria-hidden="true" />
              </Button>
            </header>
            {!collapsed ? (
              <div
                className="flex-1 min-h-0"
                data-testid={`tile-grid-tile-${laneId}-body`}
              >
                <LaneTile
                  store={store}
                  sessionId={sessionId}
                  laneId={laneId}
                  onUnpin={(id) => unpinLane(sessionId, id)}
                  onKill={onKill}
                  onFocus={onFocusLane}
                />
              </div>
            ) : null}
          </article>
        );
      })}
    </div>
  );
}
