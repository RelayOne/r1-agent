// SPDX-License-Identifier: MIT
// <Composer> — message-entry surface. Spec item 32/55.
//
// One textarea + Send button. Behavior:
//   - Cmd/Ctrl+Enter sends. Plain Enter inserts a newline (long-form
//     editing is the common case for r1's intents).
//   - The textarea + Send button disable while `streaming` is true so
//     the user can't fire a second turn before the daemon is ready;
//     the StopButton (item 33) is the affordance during streaming.
//   - Empty input (whitespace-only) blocks send.
//   - aria-label="Compose message" on the textarea per spec.
//   - data-testid="composer", "composer-textarea", "composer-send",
//     "composer-charcount" for Playwright reach.
//
// Auto-resize: the textarea grows from 1 line up to a soft cap (8
// lines) by mutating its `rows` attribute as the user types. Beyond
// the cap we let the textarea scroll internally.
import { useEffect, useRef, useState } from "react";
import type { ReactElement, KeyboardEvent } from "react";
import { Send } from "lucide-react";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const MAX_LINES = 8;

export interface ComposerProps {
  value: string;
  onChange: (next: string) => void;
  onSend: (value: string) => void;
  /** True while the session is streaming a response. Disables input + send. */
  streaming?: boolean;
  /** Soft character limit; surfaced as a counter beside Send. */
  characterLimit?: number;
}

function approximateRowCount(text: string): number {
  if (!text) return 1;
  const newlines = (text.match(/\n/g) || []).length;
  return Math.min(MAX_LINES, Math.max(1, newlines + 1));
}

export function Composer({
  value,
  onChange,
  onSend,
  streaming = false,
  characterLimit,
}: ComposerProps): ReactElement {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const [rows, setRows] = useState<number>(() => approximateRowCount(value));

  useEffect(() => {
    setRows(approximateRowCount(value));
  }, [value]);

  const trimmed = value.trim();
  const canSend = !streaming && trimmed.length > 0;

  const handleSend = (): void => {
    if (!canSend) return;
    onSend(trimmed);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>): void => {
    const isCmdOrCtrl = e.metaKey || e.ctrlKey;
    if (e.key === "Enter" && isCmdOrCtrl) {
      e.preventDefault();
      handleSend();
    }
  };

  return (
    <form
      data-testid="composer"
      data-streaming={streaming ? "true" : "false"}
      aria-label="Composer"
      onSubmit={(e) => {
        e.preventDefault();
        handleSend();
      }}
      className="border-t border-border p-2 flex flex-col gap-2"
    >
      <Textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        rows={rows}
        disabled={streaming}
        spellCheck
        data-testid="composer-textarea"
        aria-label="Compose message"
        aria-busy={streaming ? "true" : "false"}
        aria-keyshortcuts="Meta+Enter Control+Enter"
        className={cn(
          "resize-none font-mono text-sm",
          streaming && "opacity-60 cursor-not-allowed",
        )}
      />
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <span data-testid="composer-hint" className="select-none">
          {streaming
            ? "Streaming a response — use the Stop button to interrupt."
            : "Cmd/Ctrl+Enter to send"}
        </span>
        {typeof characterLimit === "number" ? (
          <span
            data-testid="composer-charcount"
            aria-label={`${value.length} of ${characterLimit} characters`}
            className={cn(
              value.length > characterLimit && "text-destructive font-semibold",
            )}
          >
            {value.length}/{characterLimit}
          </span>
        ) : null}
        <span className="ml-auto" />
        <Button
          type="submit"
          size="sm"
          data-testid="composer-send"
          aria-label="Send message"
          aria-keyshortcuts="Meta+Enter Control+Enter"
          disabled={!canSend}
        >
          <Send className="w-3 h-3 mr-1" aria-hidden="true" />
          Send
        </Button>
      </div>
    </form>
  );
}
