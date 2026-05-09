// SPDX-License-Identifier: MIT
// <App> — top-level surface that composes the global providers and
// mounts the router. Until this commit `main.tsx` rendered only a
// landing line; the audit flagged that every shipped component
// (ChatPane / Composer / SessionList / LanesSidebar / TileGrid /
// SettingsPage / StatusBar / ConnectionLostBanner / ...) was dark
// code with no path from the SPA entry point.
//
// Spec ref: web-chat-ui Spec 6 (item 41/55) — App shell + router
// mount.
//
// Provider order:
//   <ThemeProvider>             — dark / hc class on <html>
//     <R1dClientProvider>       — per-daemon HTTP+WS client registry
//       <DaemonStoreProvider>   — per-daemon zustand store registry
//         <GlobalKeybindings>   — / and Mod+1..9 shortcuts (no-ops
//                                  here; route components hand in
//                                  real handlers via context if they
//                                  need them).
//         <RouterProvider>      — react-router v7 browser router
import { useMemo } from "react";
import type { ReactElement } from "react";
import { RouterProvider } from "react-router-dom";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import { GlobalKeybindings } from "@/components/GlobalKeybindings";
import { buildRouter, type RouteRenderers } from "@/routes";
import { DaemonStoreProvider } from "@/app/DaemonStoreProvider";
import { R1dClientProvider } from "@/app/R1dClientProvider";
import { DaemonsLanding } from "@/routes/pages/DaemonsLanding";
import { DaemonHome } from "@/routes/pages/DaemonHome";
import { SessionView } from "@/routes/pages/SessionView";
import { LaneFocus } from "@/routes/pages/LaneFocus";
import { SettingsRoute } from "@/routes/pages/SettingsRoute";
import { NotFound } from "@/routes/pages/NotFound";

export function App(): ReactElement {
  // Build the router once. The renderers reference React components
  // imported above; they call into the providers via hooks. Building
  // the router inside useMemo keeps a stable reference across HMR.
  const router = useMemo(() => {
    const renderers: RouteRenderers = {
      DaemonsLanding: () => <DaemonsLanding />,
      DaemonHome: ({ daemonId }) => <DaemonHome daemonId={daemonId} />,
      SessionView: ({ daemonId, sessionId }) => (
        <SessionView daemonId={daemonId} sessionId={sessionId} />
      ),
      LaneFocus: ({ daemonId, sessionId, laneId }) => (
        <LaneFocus daemonId={daemonId} sessionId={sessionId} laneId={laneId} />
      ),
      SettingsRoute: () => <SettingsRoute />,
      NotFound: () => <NotFound />,
    };
    return buildRouter(renderers);
  }, []);

  return (
    <ThemeProvider>
      <R1dClientProvider>
        <DaemonStoreProvider>
          <GlobalKeybindings />
          <RouterProvider router={router} />
        </DaemonStoreProvider>
      </R1dClientProvider>
    </ThemeProvider>
  );
}
