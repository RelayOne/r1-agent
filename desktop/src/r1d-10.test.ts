// SPDX-License-Identifier: MIT
//
// R1D-10 tests — approval/scheduler truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderPanel as renderApprovalQueue } from "./panels/approval-queue";
import { renderPanel as renderScheduler } from "./panels/scheduler";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanup(root: HTMLElement): void {
  root.remove();
}

describe("R1D-10 desktop parity", () => {
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
  });

  it("renders the approval queue as unavailable without invoking IPC", () => {
    const root = makeRoot();
    renderApprovalQueue(root);

    const unavailable = root.querySelector('[data-role="approval-unavailable"]');
    expect(unavailable?.textContent).toContain(
      "does not implement approval queue IPC",
    );
    expect(root.querySelector('[data-role="approval-approve"]')).toBeNull();
    expect(root.querySelector('[data-role="approval-reject"]')).toBeNull();
    expect(console.info).not.toHaveBeenCalled();

    cleanup(root);
  });

  it("renders the scheduler as unavailable without invoking IPC", () => {
    const root = makeRoot();
    renderScheduler(root);

    const unavailable = root.querySelector('[data-role="scheduler-unavailable"]');
    expect(unavailable?.textContent).toContain(
      "does not implement scheduler IPC",
    );
    expect(root.querySelector('[data-role="schedule-new"]')).toBeNull();
    expect(root.querySelector('[data-role="schedule-run-now"]')).toBeNull();
    expect(console.info).not.toHaveBeenCalled();

    cleanup(root);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });
});
