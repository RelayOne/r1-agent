// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent, screen } from "@testing-library/react";
import { ThreeColumnShell } from "@/components/layout/ThreeColumnShell";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

describe("<ThreeColumnShell>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("daemon-a", NOOP);
  });

  it("renders all three regions with proper landmarks and testids", () => {
    render(
      <ThreeColumnShell
        store={store}
        left={<div data-testid="left-content">L</div>}
        center={<div data-testid="center-content">C</div>}
        right={<div data-testid="right-content">R</div>}
      />,
    );
    expect(screen.getByTestId("three-column-shell")).toBeTruthy();
    expect(screen.getByRole("navigation", { name: /sessions rail/i })).toBeTruthy();
    expect(screen.getByRole("main", { name: /chat/i })).toBeTruthy();
    expect(screen.getByRole("complementary", { name: /lanes rail/i })).toBeTruthy();
    expect(screen.getByTestId("left-content")).toBeTruthy();
    expect(screen.getByTestId("center-content")).toBeTruthy();
    expect(screen.getByTestId("right-content")).toBeTruthy();
  });

  it("hides left rail content and updates aria-expanded when toggle is clicked", () => {
    render(
      <ThreeColumnShell
        store={store}
        left={<div data-testid="left-content">L</div>}
        center={null}
        right={null}
      />,
    );
    const toggle = screen.getByTestId("left-rail-toggle");
    const railEl = screen.getByTestId("three-column-shell-left");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(railEl.getAttribute("data-collapsed")).toBe("false");
    expect(screen.queryByTestId("left-content")).not.toBeNull();

    fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(railEl.getAttribute("data-collapsed")).toBe("true");
    expect(screen.queryByTestId("left-content")).toBeNull();

    fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(railEl.getAttribute("data-collapsed")).toBe("false");
    expect(screen.queryByTestId("left-content")).not.toBeNull();
  });

  it("collapses right rail without affecting left rail", () => {
    render(
      <ThreeColumnShell
        store={store}
        left={<div data-testid="left-content">L</div>}
        center={null}
        right={<div data-testid="right-content">R</div>}
      />,
    );
    fireEvent.click(screen.getByTestId("right-rail-toggle"));
    expect(screen.queryByTestId("right-content")).toBeNull();
    expect(screen.queryByTestId("left-content")).not.toBeNull();
    expect(
      screen.getByTestId("three-column-shell-right").getAttribute("data-collapsed"),
    ).toBe("true");
    expect(
      screen.getByTestId("three-column-shell-left").getAttribute("data-collapsed"),
    ).toBe("false");
  });

  it("respects pre-existing collapsed state from the store", () => {
    store.getState().setLeftRailCollapsed(true);
    store.getState().setRightRailCollapsed(true);
    render(
      <ThreeColumnShell
        store={store}
        left={<div data-testid="left-content">L</div>}
        center={null}
        right={<div data-testid="right-content">R</div>}
      />,
    );
    expect(screen.queryByTestId("left-content")).toBeNull();
    expect(screen.queryByTestId("right-content")).toBeNull();
    expect(
      screen.getByTestId("left-rail-toggle").getAttribute("aria-expanded"),
    ).toBe("false");
    expect(
      screen.getByTestId("right-rail-toggle").getAttribute("aria-expanded"),
    ).toBe("false");
  });

  it("flips toggle aria-label between Collapse and Expand", () => {
    render(
      <ThreeColumnShell
        store={store}
        left={null}
        center={null}
        right={null}
      />,
    );
    expect(screen.getByLabelText(/Collapse sessions rail/i)).toBeTruthy();
    expect(screen.getByLabelText(/Collapse lanes rail/i)).toBeTruthy();
    fireEvent.click(screen.getByTestId("left-rail-toggle"));
    expect(screen.getByLabelText(/Expand sessions rail/i)).toBeTruthy();
    fireEvent.click(screen.getByTestId("right-rail-toggle"));
    expect(screen.getByLabelText(/Expand lanes rail/i)).toBeTruthy();
  });
});
