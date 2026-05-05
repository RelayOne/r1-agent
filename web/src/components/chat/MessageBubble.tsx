// SPDX-License-Identifier: MIT
// <MessageBubble> — renders one ChatMessage row. Spec item 27/55.
//
// Each message carries an ordered list of `parts` (text | tool |
// reasoning | plan). The bubble walks the array and routes each part
// to the right child:
//
//   - text       → <Markdown> (item 20).
//   - tool       → <ToolCard> (item 28).
//   - reasoning  → <ReasoningCard> (item 29).
//   - plan       → <PlanCard> (item 30).
//
// The cards themselves are accepted via callback props so the bubble
// stays decoupled from their concrete implementations and the unit
// test can verify routing without standing up Streamdown / Shiki.
//
// Visual treatment:
//   - role=user bubbles align right with the accent border.
//   - role=assistant bubbles align left with a muted background.
//   - role=system / tool render as compact monospaced strips.
import type { ReactElement, ReactNode } from "react";
import { Markdown } from "@/lib/render/markdown";
import type { ChatMessage } from "@/lib/store/daemonStore";
import type { MessagePart } from "@/lib/api/types";
import { cn } from "@/lib/utils";

export interface MessageBubbleProps {
  message: ChatMessage;
  /** True when this is the latest streaming message in the log. */
  streaming: boolean;
  /** Renders one tool part. */
  renderTool: (
    part: Extract<MessagePart, { kind: "tool" }>,
    streaming: boolean,
  ) => ReactNode;
  /** Renders one reasoning part. */
  renderReasoning: (
    part: Extract<MessagePart, { kind: "reasoning" }>,
    streaming: boolean,
  ) => ReactNode;
  /** Renders one plan part. */
  renderPlan: (
    part: Extract<MessagePart, { kind: "plan" }>,
  ) => ReactNode;
}

function roleClasses(role: ChatMessage["role"]): string {
  switch (role) {
    case "user":
      return "border-primary/40 ml-auto";
    case "assistant":
      return "bg-muted/40";
    case "system":
      return "bg-muted/20 text-muted-foreground text-xs";
    case "tool":
      return "bg-muted/30 font-mono text-xs";
  }
}

export function MessageBubble({
  message,
  streaming,
  renderTool,
  renderReasoning,
  renderPlan,
}: MessageBubbleProps): ReactElement {
  return (
    <article
      data-testid={`message-bubble-${message.id}`}
      data-role={message.role}
      data-streaming={streaming ? "true" : "false"}
      aria-label={`${message.role} message`}
      className={cn(
        "max-w-[85%] my-2 mx-3 rounded-md border border-border p-3 space-y-2",
        roleClasses(message.role),
      )}
    >
      <header
        className="flex items-baseline gap-2 text-xs text-muted-foreground"
        data-testid={`message-bubble-${message.id}-header`}
      >
        <span className="font-semibold text-foreground capitalize">
          {message.role}
        </span>
        <time dateTime={message.createdAt}>
          {new Date(message.createdAt).toLocaleTimeString()}
        </time>
        {typeof message.costUsd === "number" ? (
          <span aria-label={`Cost ${message.costUsd.toFixed(4)} dollars`}>
            ${message.costUsd.toFixed(4)}
          </span>
        ) : null}
      </header>

      <div
        className="space-y-2"
        data-testid={`message-bubble-${message.id}-parts`}
      >
        {message.parts.map((part, idx) => {
          switch (part.kind) {
            case "text":
              return (
                <Markdown
                  key={`p-${idx}`}
                  streaming={streaming}
                >
                  {part.text}
                </Markdown>
              );
            case "tool":
              return (
                <div key={`p-${idx}`} data-testid={`message-part-tool-${part.toolCallId}`}>
                  {renderTool(part, streaming)}
                </div>
              );
            case "reasoning":
              return (
                <div key={`p-${idx}`} data-testid={`message-part-reasoning-${idx}`}>
                  {renderReasoning(part, streaming)}
                </div>
              );
            case "plan":
              return (
                <div key={`p-${idx}`} data-testid={`message-part-plan-${idx}`}>
                  {renderPlan(part)}
                </div>
              );
          }
        })}
      </div>
    </article>
  );
}
