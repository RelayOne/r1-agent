// SPDX-License-Identifier: MIT
// <StopButton> — interrupt swap for the Send button. Spec item 33/55.
//
// While the daemon is streaming a turn, the chat surface swaps the
// Composer's Send affordance for a Stop button. Clicking it triggers
// the spec'd "interrupt" envelope on the WS — the consumer wires that
// via the `onInterrupt` callback. The `dropPartial` prop is forwarded
// to the callback so the parent can implement RT-CANCEL-INTERRUPT
// (drop-partial) semantics from the cortex spec.
//
// The component itself is presentational: it renders nothing when
// `streaming` is false, so chat surfaces can mount it next to Send
// and show whichever is appropriate at any moment.
import type { ReactElement } from "react";
import { Square } from "lucide-react";
import { Button } from "@/components/ui/button";

export interface StopButtonProps {
  /** True while a turn is streaming. The button only renders when true. */
  streaming: boolean;
  /** Called when the user clicks Stop. The boolean argument indicates
   *  whether the partial assistant turn should be dropped (default true,
   *  matching cortex RT-CANCEL-INTERRUPT semantics). */
  onInterrupt: (dropPartial: boolean) => void;
  /** When true, the partial turn is dropped on stop (default). When
   *  false, the partial output is preserved (e.g. user wants to keep
   *  what's been said and resume manually). */
  dropPartial?: boolean;
  /** Override label (default "Stop"). */
  label?: string;
}

export function StopButton({
  streaming,
  onInterrupt,
  dropPartial = true,
  label = "Stop",
}: StopButtonProps): ReactElement | null {
  if (!streaming) return null;
  return (
    <Button
      type="button"
      variant="destructive"
      size="sm"
      onClick={() => onInterrupt(dropPartial)}
      data-testid="stop-button"
      data-drop-partial={dropPartial ? "true" : "false"}
      aria-label="Stop streaming response"
      aria-keyshortcuts="Escape"
    >
      <Square className="w-3 h-3 mr-1" aria-hidden="true" fill="currentColor" />
      {label}
    </Button>
  );
}
