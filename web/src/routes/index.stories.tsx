// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { MemoryRouter, useRoutes } from "react-router-dom";
import { buildRoutes, type RouteRenderers } from "./index";

const renderers: RouteRenderers = {
  DaemonsLanding: () => (
    <main className="p-6 space-y-2">
      <h1 className="text-lg font-semibold">Pick a daemon</h1>
      <ul className="text-sm space-y-1">
        <li>
          <a className="underline" href="/d/local">
            local daemon
          </a>
        </li>
        <li>
          <a className="underline" href="/d/remote">
            remote daemon
          </a>
        </li>
      </ul>
    </main>
  ),
  DaemonHome: ({ daemonId }) => (
    <header className="px-6 pt-6 pb-2 text-sm text-muted-foreground">
      daemon: <span className="font-mono">{daemonId}</span>
    </header>
  ),
  SessionView: ({ daemonId, sessionId }) => (
    <section className="p-6 text-sm">
      <p>
        session <span className="font-mono">{sessionId}</span> on{" "}
        <span className="font-mono">{daemonId}</span>
      </p>
    </section>
  ),
  LaneFocus: ({ daemonId, sessionId, laneId }) => (
    <section className="p-6 text-sm">
      <p>
        focused lane <span className="font-mono">{laneId}</span> on session{" "}
        <span className="font-mono">{sessionId}</span> ({daemonId})
      </p>
    </section>
  ),
  SettingsRoute: () => (
    <main className="p-6 text-sm">
      <h1 className="text-lg font-semibold">Settings</h1>
    </main>
  ),
  NotFound: () => (
    <main className="p-6 text-sm">
      <h1 className="text-lg font-semibold">404</h1>
      <p className="text-muted-foreground">No such route.</p>
    </main>
  ),
};

function App(): JSX.Element | null {
  return useRoutes(buildRoutes(renderers));
}

const meta: Meta<typeof App> = {
  title: "core/Router",
  component: App,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof App>;

function At({ path }: { path: string }): JSX.Element {
  return (
    <MemoryRouter initialEntries={[path]}>
      <div className="w-[860px] border border-border rounded-md overflow-hidden">
        <App />
      </div>
    </MemoryRouter>
  );
}

export const Landing: Story = { render: () => <At path="/" /> };
export const DaemonHome: Story = { render: () => <At path="/d/local" /> };
export const SessionView: Story = {
  render: () => <At path="/d/local/sessions/s-1" />,
};
export const LaneFocus: Story = {
  render: () => <At path="/d/local/sessions/s-1/lanes/lane-a" />,
};
export const Settings: Story = { render: () => <At path="/settings" /> };
export const NotFound: Story = { render: () => <At path="/missing" /> };
