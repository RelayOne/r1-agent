// SPDX-License-Identifier: MIT
// 404 fallback. Spec item 41/55 (NotFound slot in buildRouter).
//
// Audit ref: closes the routing gap surfaced by the audit — every
// route slot in `routes/index.tsx` previously needed a real renderer
// or the entire app failed to mount.
import type { ReactElement } from "react";
import { Link } from "react-router-dom";

export function NotFound(): ReactElement {
  return (
    <main
      data-testid="route-not-found"
      role="main"
      aria-label="Page not found"
      className="flex min-h-screen flex-col items-center justify-center gap-3 bg-background text-foreground p-6"
    >
      <h1 className="text-2xl font-semibold">404 — Page not found</h1>
      <p className="text-sm text-muted-foreground">
        The route you tried to open is not registered with the SPA.
      </p>
      <Link
        to="/"
        className="rounded-md border border-border px-3 py-1 text-sm hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="route-not-found-home-link"
      >
        Back to daemon list
      </Link>
    </main>
  );
}
