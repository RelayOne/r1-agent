// SPDX-License-Identifier: MIT
//
// lane-rail error-surfacing test (audit gap: "Lane subscription failure is
// silently swallowed — rail stays stuck on 'No lanes yet.'").
//
// The rail subscribes to lanes via the imperative subscribeLanes() helper.
// On a hard subscribe rejection the previous code only console-swallowed the
// error in .catch, so the rail rendered the normal empty "No lanes yet."
// message forever with zero indication that anything failed. These tests pin
// the fixed behaviour: a rejected subscription surfaces an "unavailable"
// message; a healthy subscription shows the normal empty message.

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import { act } from "@testing-library/react";

vi.mock("../lib/laneSubscription", () => ({
  subscribeLanes: vi.fn(),
}));

import { subscribeLanes } from "../lib/laneSubscription";
import { mountLaneRail } from "./lane-rail";

async function flush(): Promise<void> {
  await act(async () => {
    // Two microtask turns: subscribeLanes' promise settles, then the rail's
    // .then/.catch runs and calls render().
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("lane-rail — subscribe failure surfacing", () => {
  let container: HTMLElement;

  beforeEach(() => {
    (subscribeLanes as Mock).mockReset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
  });

  it("surfaces a hard subscribe failure in the rail instead of silently freezing", async () => {
    (subscribeLanes as Mock).mockRejectedValue(new Error("host verb missing"));

    const handle = mountLaneRail(container);
    await act(async () => {
      handle.attach("sess-1");
    });
    await flush();

    expect(container.textContent).toContain("Lane updates unavailable");
    expect(container.textContent).toContain("host verb missing");
    // The misleading normal-empty message must NOT be shown on failure.
    expect(container.textContent).not.toContain("No lanes yet.");

    handle.dispose();
  });

  it("shows the normal empty message when the subscription succeeds", async () => {
    (subscribeLanes as Mock).mockResolvedValue(async () => {
      /* teardown */
    });

    const handle = mountLaneRail(container);
    await act(async () => {
      handle.attach("sess-ok");
    });
    await flush();

    expect(container.textContent).toContain("No lanes yet.");
    expect(container.textContent).not.toContain("Lane updates unavailable");

    handle.dispose();
  });

  it("clears a prior error when re-attaching to a healthy session", async () => {
    (subscribeLanes as Mock).mockRejectedValueOnce(new Error("channel drop"));

    const handle = mountLaneRail(container);
    await act(async () => {
      handle.attach("sess-bad");
    });
    await flush();
    expect(container.textContent).toContain("Lane updates unavailable");

    (subscribeLanes as Mock).mockResolvedValue(async () => {
      /* teardown */
    });
    await act(async () => {
      handle.attach("sess-good");
    });
    await flush();

    expect(container.textContent).not.toContain("Lane updates unavailable");
    expect(container.textContent).toContain("No lanes yet.");

    handle.dispose();
  });
});
