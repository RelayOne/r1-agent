// SPDX-License-Identifier: MIT
// <ReasoningCard> — assistant inner-monologue surface. Spec item 29/55.
//
// Reasoning parts are dimmed (less visual weight than the answer) and
// collapsible. While `state === "streaming"` the card pulses a soft
// shimmer over the body so the reader sees that thinking is in
// progress; when the user prefers reduced motion we drop the shimmer
// and instead show a static "thinking…" caption next to the header.
//
// The card defaults to expanded while streaming and collapsed once
// the part is complete (mirrors ToolCard semantics from item 28). The
// user's manual toggle takes a sticky override.
import { useEffect, useRef, useState } from "react";
import type { ReactElement } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Markdown } from "@/lib/render/markdown";
import { useTheme } from "@/components/layout/ThemeProvider";
import type { MessagePart } from "@/lib/api/types";
import { cn } from "@/lib/utils";

type ReasoningPart = Extract<MessagePart, { kind: "reasoning" }>;

export interface ReasoningCardProps {
  part: ReasoningPart;
  /** True while the parent message is still streaming overall. Distinct
   *  from `part.state` because a finished reasoning block can sit inside
   *  a still-streaming message. */
  streaming?: boolean;
  /** Index inside the message (used to namespace the testid when the
   *  same message has multiple reasoning blocks). */
  index?: number;
  /** Test override: force-disable shimmer regardless of theme. */
  reducedMotionOverride?: boolean;
}

export function ReasoningCard({
  part,
  index = 0,
  reducedMotionOverride,
}: ReasoningCardProps): ReactElement {
  const isStreaming = part.state === "streaming";
  // Ask theme for prefers-reduced-motion. ThemeProvider may not be
  // present in isolated component renders / tests; fall back to "no
  // preference" in that case.
  let themedReducedMotion = false;
  try {
    themedReducedMotion = useTheme().reducedMotion;
  } catch {
    themedReducedMotion = false;
  }
  const reducedMotion = reducedMotionOverride ?? themedReducedMotion;

  const [expanded, setExpanded] = useState<boolean>(isStreaming);
  const userOverride = useRef<boolean>(false);
  useEffect(() => {
    if (userOverride.current) return;
    setExpanded(isStreaming);
  }, [isStreaming]);

  const onToggle = (): void => {
    userOverride.current = true;
    setExpanded((v) => !v);
  };

  const testid = `reasoning-card-${index}`;
  const stateLabel = isStreaming ? "thinking" : "complete";

  return (
    <section
      data-testid={testid}
      data-state={part.state}
      data-expanded={expanded ? "true" : "false"}
      data-reduced-motion={reducedMotion ? "true" : "false"}
      aria-label={`Reasoning, ${stateLabel}`}
      className="rounded-md border border-dashed border-border opacity-80"
    >
      <header
        className="flex items-center gap-2 px-2 py-1.5 text-xs"
        data-testid={`${testid}-header`}
      >
        <button
          type="button"
          onClick={onToggle}
          data-testid={`${testid}-toggle`}
          aria-label={expanded ? "Collapse reasoning" : "Expand reasoning"}
          aria-expanded={expanded}
          className="p-0.5 rounded hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {expanded ? (
            <ChevronDown className="w-3 h-3" aria-hidden="true" />
          ) : (
            <ChevronRight className="w-3 h-3" aria-hidden="true" />
          )}
        </button>
        <span className="font-semibold uppercase tracking-wide text-muted-foreground">
          reasoning
        </span>
        {isStreaming ? (
          <span
            className="text-muted-foreground italic"
            data-testid={`${testid}-status`}
          >
            thinking…
          </span>
        ) : null}
      </header>

      {expanded ? (
        <div
          className={cn(
            "p-2 pt-0 text-xs text-muted-foreground italic",
            isStreaming && !reducedMotion && "reasoning-shimmer",
          )}
          data-testid={`${testid}-body`}
        >
          <Markdown streaming={isStreaming}>{part.text}</Markdown>
        </div>
      ) : null}
    </section>
  );
}
