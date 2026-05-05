// SPDX-License-Identifier: MIT
// <ChatPane> — center column. Spec item 25/55.
//
// Renders one of two layouts based on per-session tile pinning state:
//
//   - When `ui.tilePinnedBySession[sessionId]` has at least one lane,
//     the pane shows a <TileGrid>-style region (item 36 ships the real
//     grid; we slot it via the `renderTileGrid` callback so this item
//     stays focused on routing logic).
//
//   - Otherwise the pane shows a vertical stack of <MessageLog> on top
//     and <Composer> at the bottom (items 26 and 32 ship the real
//     components; we slot them via callbacks for the same reason).
//
// The component is intentionally a thin router. Owning the conditional
// here means the ChatPane test pins the contract (which view appears
// for which state) without coupling to the heavyweight subcomponents.
import { useStore } from "zustand";
import type { ReactElement, ReactNode } from "react";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { SessionId } from "@/lib/api/types";

export interface ChatPaneProps {
  store: DaemonStore;
  sessionId: SessionId;
  /** Renders the message log + composer column. */
  renderMessageColumn: (sessionId: SessionId) => ReactNode;
  /** Renders the tile grid for this session's pinned lanes. */
  renderTileGrid: (sessionId: SessionId, laneIds: ReadonlyArray<string>) => ReactNode;
}

export function ChatPane({
  store,
  sessionId,
  renderMessageColumn,
  renderTileGrid,
}: ChatPaneProps): ReactElement {
  const tileIds = useStore(
    store,
    (s) => s.ui.tilePinnedBySession[sessionId] ?? [],
  );
  const tileMode = tileIds.length > 0;

  return (
    <section
      data-testid="chat-pane"
      data-tile-mode={tileMode ? "true" : "false"}
      data-session-id={sessionId}
      aria-label={tileMode ? "Lane tile grid" : "Chat conversation"}
      className="flex flex-col h-full w-full"
    >
      {tileMode ? (
        <div
          className="flex-1 min-h-0"
          data-testid="chat-pane-tile-region"
          role="region"
          aria-label="Pinned lanes"
        >
          {renderTileGrid(sessionId, tileIds)}
        </div>
      ) : (
        <div
          className="flex-1 min-h-0 flex flex-col"
          data-testid="chat-pane-message-region"
        >
          {renderMessageColumn(sessionId)}
        </div>
      )}
    </section>
  );
}
