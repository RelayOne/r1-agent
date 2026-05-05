// SPDX-License-Identifier: MIT
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, useRoutes } from "react-router-dom";
import { buildRoutes, type RouteRenderers } from "@/routes";

const renderers: RouteRenderers = {
  DaemonsLanding: () => <div data-testid="r-landing">landing</div>,
  DaemonHome: ({ daemonId }) => (
    <div data-testid="r-daemon">daemon:{daemonId}</div>
  ),
  SessionView: ({ daemonId, sessionId }) => (
    <div data-testid="r-session">
      session:{daemonId}:{sessionId}
    </div>
  ),
  LaneFocus: ({ daemonId, sessionId, laneId }) => (
    <div data-testid="r-lane">
      lane:{daemonId}:{sessionId}:{laneId}
    </div>
  ),
  SettingsRoute: () => <div data-testid="r-settings">settings</div>,
  NotFound: () => <div data-testid="r-404">not found</div>,
};

function App(): JSX.Element | null {
  const tree = useRoutes(buildRoutes(renderers));
  return tree;
}

function renderAt(path: string): void {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

describe("router", () => {
  it("matches the landing route at '/'", () => {
    renderAt("/");
    expect(screen.getByTestId("r-landing")).toBeTruthy();
  });

  it("matches DaemonHome at /d/:daemonId", () => {
    renderAt("/d/d1");
    expect(screen.getByTestId("r-daemon").textContent).toBe("daemon:d1");
  });

  it("renders SessionView nested inside DaemonHome", () => {
    renderAt("/d/d1/sessions/s1");
    expect(screen.getByTestId("r-daemon")).toBeTruthy();
    expect(screen.getByTestId("r-session").textContent).toBe(
      "session:d1:s1",
    );
  });

  it("renders LaneFocus nested inside SessionView", () => {
    renderAt("/d/d1/sessions/s1/lanes/l1");
    expect(screen.getByTestId("r-lane").textContent).toBe(
      "lane:d1:s1:l1",
    );
  });

  it("matches the settings route", () => {
    renderAt("/settings");
    expect(screen.getByTestId("r-settings")).toBeTruthy();
  });

  it("falls through to NotFound for unknown paths", () => {
    renderAt("/no/such/path");
    expect(screen.getByTestId("r-404")).toBeTruthy();
  });
});
