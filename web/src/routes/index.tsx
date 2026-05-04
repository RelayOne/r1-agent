// SPDX-License-Identifier: MIT
// Router config — react-router v7. Spec item 41/55.
//
// Route structure (nested daemon → session → lane):
//
//   /                                 → DaemonsLanding (pick a daemon)
//   /d/:daemonId                      → DaemonHome (sessions list + new)
//   /d/:daemonId/sessions/:sessionId  → SessionView (chat / tile grid)
//   /d/:daemonId/sessions/:sessionId/lanes/:laneId
//                                     → LaneFocus (single-lane focus view)
//   /settings                         → SettingsRoute
//   *                                 → NotFound (404 + ConnectionLostBanner)
//
// Each route component is a small wrapper that pulls its data via
// useParams + the per-daemon zustand store. Concrete page bodies are
// the components from items 22-39 (ThreeColumnShell + ChatPane + …).
// We keep the wiring component-only here; data hydration belongs in
// useDaemonSocket / data loaders, not in routes.
import { createBrowserRouter, Outlet, useParams } from "react-router-dom";
import type { ReactElement } from "react";

// Route component types are exported separately so test code can
// mount them inside a MemoryRouter without importing the router
// directly.
export type RouteParams = {
  daemonId?: string;
  sessionId?: string;
  laneId?: string;
} & Record<string, string | undefined>;

export interface RouteRenderers {
  DaemonsLanding: () => ReactElement;
  DaemonHome: (params: { daemonId: string }) => ReactElement;
  SessionView: (params: { daemonId: string; sessionId: string }) => ReactElement;
  LaneFocus: (params: {
    daemonId: string;
    sessionId: string;
    laneId: string;
  }) => ReactElement;
  SettingsRoute: () => ReactElement;
  NotFound: () => ReactElement;
}

function DaemonsLandingRoute({ R }: { R: RouteRenderers }): ReactElement {
  return <R.DaemonsLanding />;
}

function DaemonHomeRoute({ R }: { R: RouteRenderers }): ReactElement {
  const { daemonId } = useParams<RouteParams>();
  if (!daemonId) return <R.NotFound />;
  return <R.DaemonHome daemonId={daemonId} />;
}

function SessionViewRoute({ R }: { R: RouteRenderers }): ReactElement {
  const { daemonId, sessionId } = useParams<RouteParams>();
  if (!daemonId || !sessionId) return <R.NotFound />;
  return <R.SessionView daemonId={daemonId} sessionId={sessionId} />;
}

function LaneFocusRoute({ R }: { R: RouteRenderers }): ReactElement {
  const { daemonId, sessionId, laneId } = useParams<RouteParams>();
  if (!daemonId || !sessionId || !laneId) return <R.NotFound />;
  return <R.LaneFocus daemonId={daemonId} sessionId={sessionId} laneId={laneId} />;
}

function SettingsRouteWrap({ R }: { R: RouteRenderers }): ReactElement {
  return <R.SettingsRoute />;
}

function NotFoundRoute({ R }: { R: RouteRenderers }): ReactElement {
  return <R.NotFound />;
}

/**
 * Build the route tree given a set of renderers. Exposed as a pure
 * factory so tests can mount the same tree under a MemoryRouter.
 */
export function buildRoutes(R: RouteRenderers): Parameters<typeof createBrowserRouter>[0] {
  return [
    { path: "/", element: <DaemonsLandingRoute R={R} /> },
    {
      path: "/d/:daemonId",
      element: (
        <>
          <DaemonHomeRoute R={R} />
          <Outlet />
        </>
      ),
      children: [
        {
          path: "sessions/:sessionId",
          element: <SessionViewRoute R={R} />,
          children: [
            {
              path: "lanes/:laneId",
              element: <LaneFocusRoute R={R} />,
            },
          ],
        },
      ],
    },
    { path: "/settings", element: <SettingsRouteWrap R={R} /> },
    { path: "*", element: <NotFoundRoute R={R} /> },
  ];
}

export function buildRouter(R: RouteRenderers): ReturnType<typeof createBrowserRouter> {
  return createBrowserRouter(buildRoutes(R));
}
