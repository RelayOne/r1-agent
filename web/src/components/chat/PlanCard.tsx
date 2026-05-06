// SPDX-License-Identifier: MIT
// <PlanCard> — live-updating plan view. Spec item 30/55.
//
// Renders a "plan" MessagePart whose `items` array is the current
// snapshot from the PlanUpdateLobe. Each item shows:
//   - a status pill (pending / in-progress / completed / blocked /
//     skipped) with a matching colored dot,
//   - the item text,
//   - a stable `data-testid="plan-item-<id>"` so the agentic test
//     harness (spec 8) can assert on individual items as the lobe
//     updates them in place.
//
// The card itself updates live: because the part is part of a
// MessageBubble's `parts` array, MessageBubble re-renders the
// PlanCard whenever the lobe publishes a new snapshot. We do not
// animate item additions / removals — the goal is calm, glanceable
// progress for an operator who is also watching tool output.
import type { ReactElement } from "react";
import { Check, Circle, AlertTriangle, Clock, MinusCircle } from "lucide-react";
import type { MessagePart } from "@/lib/api/types";
import { cn } from "@/lib/utils";

type PlanPart = Extract<MessagePart, { kind: "plan" }>;
type PlanStatus = PlanPart["items"][number]["status"];

const STATUS_DOT: Record<PlanStatus, string> = {
  pending: "bg-slate-400",
  "in-progress": "bg-amber-500 animate-pulse",
  completed: "bg-emerald-500",
  blocked: "bg-rose-500",
  skipped: "bg-slate-300 opacity-60",
};

const STATUS_LABEL: Record<PlanStatus, string> = {
  pending: "pending",
  "in-progress": "in progress",
  completed: "completed",
  blocked: "blocked",
  skipped: "skipped",
};

function StatusIcon({ status }: { status: PlanStatus }): ReactElement {
  const className = "w-3 h-3";
  switch (status) {
    case "completed":
      return <Check className={className} aria-hidden="true" />;
    case "in-progress":
      return <Clock className={className} aria-hidden="true" />;
    case "blocked":
      return <AlertTriangle className={className} aria-hidden="true" />;
    case "skipped":
      return <MinusCircle className={className} aria-hidden="true" />;
    case "pending":
    default:
      return <Circle className={className} aria-hidden="true" />;
  }
}

export interface PlanCardProps {
  part: PlanPart;
  /** Index inside the message; namespaces the card testid. */
  index?: number;
}

export function PlanCard({ part, index = 0 }: PlanCardProps): ReactElement {
  const items = part.items;
  const counts = items.reduce<Record<PlanStatus, number>>(
    (acc, it) => {
      acc[it.status] += 1;
      return acc;
    },
    {
      pending: 0,
      "in-progress": 0,
      completed: 0,
      blocked: 0,
      skipped: 0,
    },
  );
  const completed = counts.completed;
  const total = items.length;
  const testid = `plan-card-${index}`;

  return (
    <section
      data-testid={testid}
      data-total={total}
      data-completed={completed}
      aria-label={`Plan, ${completed} of ${total} completed`}
      className="rounded-md border border-border"
    >
      <header
        className="flex items-center gap-2 px-2 py-1.5 text-xs bg-muted/40"
        data-testid={`${testid}-header`}
      >
        <span className="font-semibold uppercase tracking-wide text-muted-foreground">
          plan
        </span>
        <span className="text-muted-foreground" data-testid={`${testid}-progress`}>
          {completed} / {total}
        </span>
        {counts.blocked > 0 ? (
          <span
            className="text-rose-500 font-semibold"
            data-testid={`${testid}-blocked-count`}
          >
            {counts.blocked} blocked
          </span>
        ) : null}
        {counts["in-progress"] > 0 ? (
          <span
            className="text-amber-500"
            data-testid={`${testid}-in-progress-count`}
          >
            {counts["in-progress"]} in progress
          </span>
        ) : null}
      </header>
      {items.length === 0 ? (
        <p
          className="p-3 text-xs text-muted-foreground"
          data-testid={`${testid}-empty`}
        >
          The plan is empty.
        </p>
      ) : (
        <ol
          className="m-0 p-2 space-y-1 list-none"
          data-testid={`${testid}-items`}
        >
          {items.map((item) => {
            const skipped = item.status === "skipped";
            const completedItem = item.status === "completed";
            return (
              <li
                key={item.id}
                data-testid={`plan-item-${item.id}`}
                data-status={item.status}
                className={cn(
                  "flex items-start gap-2 text-sm",
                  skipped && "opacity-60",
                  completedItem && "text-muted-foreground line-through",
                )}
              >
                <span
                  className={cn(
                    "mt-1 inline-block w-2 h-2 rounded-full shrink-0",
                    STATUS_DOT[item.status],
                  )}
                  aria-hidden="true"
                />
                <span className="flex items-center gap-1 min-w-0">
                  <StatusIcon status={item.status} />
                  <span
                    className="truncate"
                    aria-label={`${item.text}, ${STATUS_LABEL[item.status]}`}
                  >
                    {item.text}
                  </span>
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
