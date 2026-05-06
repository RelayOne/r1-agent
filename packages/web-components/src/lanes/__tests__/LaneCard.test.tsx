// LaneCard test — spec desktop-cortex-augmentation §11.1 + item 30.
//
// Asserts the 6 statuses each render their paired glyph + colour
// token (D-S1 paired-glyph-a11y mandate). No mocking: LaneCard is
// pure presentational, so React Testing Library renders it directly.

import * as React from "react";
import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

import { LaneCard } from "../LaneCard";
import type { LaneStatus } from "../../types/LaneEvent";
import { statusGlyph, statusColorToken } from "../statusTokens";

const STATUSES: readonly LaneStatus[] = [
  "pending",
  "running",
  "blocked",
  "done",
  "errored",
  "cancelled",
];

describe("LaneCard", () => {
  it("renders the title for every status", () => {
    for (const status of STATUSES) {
      const { container, unmount } = render(
        <LaneCard
          laneId={`L-${status}`}
          title={`Lane ${status}`}
          status={status}
        />,
      );
      const titleEl = container.querySelector(".lane-card__title");
      expect(titleEl?.textContent).toBe(`Lane ${status}`);
      unmount();
    }
  });

  it("emits the spec-mandated paired glyph for each status", () => {
    for (const status of STATUSES) {
      const { container, unmount } = render(
        <LaneCard
          laneId={`L-${status}`}
          title="t"
          status={status}
        />,
      );
      const glyphEl = container.querySelector(".lane-card__glyph");
      expect(glyphEl?.textContent).toBe(statusGlyph(status));
      unmount();
    }
  });

  it("emits the spec-mandated colour token for each status", () => {
    for (const status of STATUSES) {
      const { container, unmount } = render(
        <LaneCard laneId="L" title="t" status={status} />,
      );
      const card = container.querySelector(".lane-card");
      const expected = `lane-card--${statusColorToken(status)}`;
      const classes = Array.from(card?.classList ?? []);
      expect(classes).toContain(expected);
      unmount();
    }
  });

  it("paired glyph + colour are unique per status (no two statuses collide)", () => {
    const glyphs = new Set<string>();
    const colors = new Set<string>();
    for (const status of STATUSES) {
      glyphs.add(statusGlyph(status));
      colors.add(statusColorToken(status));
    }
    expect(glyphs.size).toBe(STATUSES.length);
    expect(colors.size).toBe(STATUSES.length);
  });

  it("calls onSelect with the lane id when clicked", () => {
    const onSelect = vi.fn();
    render(
      <LaneCard
        laneId="L-7"
        title="seven"
        status="running"
        onSelect={onSelect}
      />,
    );
    fireEvent.click(screen.getByRole("listitem"));
    expect(onSelect).toHaveBeenCalledWith("L-7");
  });

  it("hides the kill button for done lanes", () => {
    render(
      <LaneCard
        laneId="L-1"
        title="t"
        status="done"
        onKill={() => undefined}
      />,
    );
    expect(screen.queryByLabelText(/kill lane/i)).toBeNull();
  });

  it("hides the kill button for cancelled lanes", () => {
    render(
      <LaneCard
        laneId="L-1"
        title="t"
        status="cancelled"
        onKill={() => undefined}
      />,
    );
    expect(screen.queryByLabelText(/kill lane/i)).toBeNull();
  });

  it("renders the kill button for non-terminal statuses and routes the click", () => {
    const onKill = vi.fn();
    render(
      <LaneCard
        laneId="L-9"
        title="nine"
        status="running"
        onKill={onKill}
      />,
    );
    const killBtn = screen.getByLabelText(/kill lane nine/i);
    fireEvent.click(killBtn);
    expect(onKill).toHaveBeenCalledWith("L-9");
  });

  it("kill click does NOT propagate to the row's onSelect", () => {
    const onSelect = vi.fn();
    const onKill = vi.fn();
    render(
      <LaneCard
        laneId="L-9"
        title="nine"
        status="running"
        onSelect={onSelect}
        onKill={onKill}
      />,
    );
    fireEvent.click(screen.getByLabelText(/kill lane nine/i));
    expect(onKill).toHaveBeenCalledTimes(1);
    // stopPropagation on the kill click; onSelect must not fire.
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("renders last-event preview when supplied", () => {
    render(
      <LaneCard
        laneId="L"
        title="t"
        status="running"
        lastEventPreview="reading file"
      />,
    );
    expect(screen.getByText("reading file")).toBeTruthy();
  });

  it("marks the card aria-selected when selected", () => {
    const { container } = render(
      <LaneCard laneId="L" title="t" status="running" selected />,
    );
    const card = container.querySelector(".lane-card");
    expect(card?.getAttribute("aria-selected")).toBe("true");
  });
});
