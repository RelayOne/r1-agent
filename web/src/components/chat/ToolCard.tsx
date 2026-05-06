// SPDX-License-Identifier: MIT
// <ToolCard> — collapsible tool-call viewer. Spec item 28/55.
//
// Renders one MessagePart of kind "tool". Behaviour:
//   - Header always visible: status pill + tool name + collapse toggle.
//   - Body shows input (always) + output (when present); both rendered
//     through <Markdown> as fenced JSON blocks so Streamdown's Shiki
//     pipeline applies syntax highlighting + handles partial blocks.
//   - Auto-collapses once the call transitions into `output-available`
//     (or `error`). The user can still expand at any time; manual
//     toggles override the auto-collapse and stick.
//   - Copy button copies the output (or input if no output yet) to
//     the clipboard via navigator.clipboard.
//
// `data-testid="tool-card-<toolCallId>"` + nested testids on the
// header / toggle / copy / body for Playwright reach.
import { useEffect, useRef, useState } from "react";
import type { ReactElement } from "react";
import { ChevronDown, ChevronRight, Copy, Check } from "lucide-react";
import { Markdown } from "@/lib/render/markdown";
import type { MessagePart } from "@/lib/api/types";
import { cn } from "@/lib/utils";

type ToolPart = Extract<MessagePart, { kind: "tool" }>;

const STATE_LABEL: Record<ToolPart["state"], string> = {
  "input-streaming": "input streaming",
  "input-available": "ready",
  "output-streaming": "running",
  "output-available": "done",
  error: "error",
};

const STATE_DOT: Record<ToolPart["state"], string> = {
  "input-streaming": "bg-slate-400 animate-pulse",
  "input-available": "bg-sky-500",
  "output-streaming": "bg-amber-500 animate-pulse",
  "output-available": "bg-emerald-500",
  error: "bg-rose-500",
};

function asJsonBlock(value: unknown): string {
  let text: string;
  try {
    text = JSON.stringify(value, null, 2);
  } catch {
    text = String(value);
  }
  return `\`\`\`json\n${text}\n\`\`\``;
}

export interface ToolCardProps {
  part: ToolPart;
  /** True while the parent message is still streaming. Forwarded to
   *  <Markdown> so partial fences render gracefully. */
  streaming?: boolean;
  /** Override the clipboard for tests. */
  clipboard?: { writeText: (s: string) => Promise<void> };
}

export function ToolCard({
  part,
  streaming = false,
  clipboard,
}: ToolCardProps): ReactElement {
  const isTerminal = part.state === "output-available" || part.state === "error";

  // `expanded` follows auto-collapse-on-terminal until the user toggles
  // manually; afterwards `userOverride` pins their choice.
  const [expanded, setExpanded] = useState<boolean>(!isTerminal);
  const userOverride = useRef<boolean>(false);
  useEffect(() => {
    if (userOverride.current) return;
    setExpanded(!isTerminal);
  }, [isTerminal]);

  const [copied, setCopied] = useState<boolean>(false);
  const onCopy = async (): Promise<void> => {
    const payload = part.output !== undefined ? part.output : part.input;
    let text: string;
    try {
      text = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
    } catch {
      text = String(payload);
    }
    const cb = clipboard ?? (typeof navigator !== "undefined" ? navigator.clipboard : undefined);
    if (cb) {
      try {
        await cb.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      } catch {
        // clipboard may be unavailable; UI keeps working without feedback.
      }
    }
  };

  const onToggle = (): void => {
    userOverride.current = true;
    setExpanded((v) => !v);
  };

  const stateLabel = STATE_LABEL[part.state];
  const dotClass = STATE_DOT[part.state];

  return (
    <section
      data-testid={`tool-card-${part.toolCallId}`}
      data-state={part.state}
      data-expanded={expanded ? "true" : "false"}
      aria-label={`Tool call ${part.toolName}, ${stateLabel}`}
      className="rounded-md border border-border overflow-hidden"
    >
      <header
        className="flex items-center gap-2 px-2 py-1.5 bg-muted/40 text-xs"
        data-testid={`tool-card-${part.toolCallId}-header`}
      >
        <button
          type="button"
          onClick={onToggle}
          data-testid={`tool-card-${part.toolCallId}-toggle`}
          aria-label={expanded ? "Collapse tool call" : "Expand tool call"}
          aria-expanded={expanded}
          className="p-0.5 rounded hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {expanded ? (
            <ChevronDown className="w-3 h-3" aria-hidden="true" />
          ) : (
            <ChevronRight className="w-3 h-3" aria-hidden="true" />
          )}
        </button>
        <span
          className={cn("inline-block w-2 h-2 rounded-full shrink-0", dotClass)}
          aria-hidden="true"
        />
        <span className="font-mono font-semibold">{part.toolName}</span>
        <span
          className="text-muted-foreground"
          data-testid={`tool-card-${part.toolCallId}-state`}
        >
          {stateLabel}
        </span>
        <span className="ml-auto" />
        <button
          type="button"
          onClick={onCopy}
          data-testid={`tool-card-${part.toolCallId}-copy`}
          aria-label={`Copy ${part.output !== undefined ? "output" : "input"} to clipboard`}
          className="p-0.5 rounded hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {copied ? (
            <Check className="w-3 h-3" aria-hidden="true" />
          ) : (
            <Copy className="w-3 h-3" aria-hidden="true" />
          )}
        </button>
      </header>

      {expanded ? (
        <div
          className="p-2 space-y-2 text-xs"
          data-testid={`tool-card-${part.toolCallId}-body`}
        >
          <div data-testid={`tool-card-${part.toolCallId}-input`}>
            <p className="text-muted-foreground mb-1">input</p>
            <Markdown streaming={streaming}>{asJsonBlock(part.input)}</Markdown>
          </div>
          {part.output !== undefined ? (
            <div data-testid={`tool-card-${part.toolCallId}-output`}>
              <p className="text-muted-foreground mb-1">output</p>
              <Markdown streaming={streaming}>{asJsonBlock(part.output)}</Markdown>
            </div>
          ) : null}
          {part.errorText ? (
            <div
              data-testid={`tool-card-${part.toolCallId}-error`}
              role="alert"
              className="rounded border border-destructive/40 bg-destructive/10 p-2"
            >
              <p className="text-destructive font-semibold mb-1">error</p>
              <pre className="whitespace-pre-wrap font-mono text-xs">
                {part.errorText}
              </pre>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
