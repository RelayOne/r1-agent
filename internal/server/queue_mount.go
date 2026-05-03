package server

// queue_mount.go — TASK-35: mount internal/daemon queue/WAL endpoints
// under /v1/queue/ on the `r1 serve` daemon's loopback mux, with the
// daemon's `Authorization: Bearer <tok>` flowing through.
//
// Two routes are registered:
//
//   /v1/queue/...   canonical. requireBearer enforces auth.
//   /api/...        alias for one minor version. Same auth gate;
//                   adds `Deprecation: true` per RFC 8594.
//
// Path semantics. The daemon.Daemon handler exposes its endpoints as
// flat routes: /enqueue, /status, /workers, /pause, /resume,
// /tasks, /tasks/get, /tasks/cancel, /wal, /rules, /rules/{id},
// /hooks/install, /agent/* (managed by agent_endpoints.go). The
// /v1/queue/ canonical mount uses StripPrefix so /v1/queue/enqueue
// arrives at the inner handler as /enqueue.
//
// The /api/ alias is narrower: it covers only the queue-engine surface
// (queue/wal/workers/tasks/pause/resume) so we don't accidentally
// claim ownership of /api/capabilities or /api/task — those belong to
// the agentserve mount (TASK-34). When mounted on the same mux,
// http.ServeMux's longest-prefix match means the agentserve mount's
// /api/task takes precedence over /api/ here, which is the desired
// outcome.

import (
	"net/http"

	"github.com/RelayOne/r1/internal/daemon"
)

// MountDaemonQueue registers the daemon's HTTP handler under
// /v1/queue/ (canonical) and a small set of /api/<verb> aliases
// (deprecated) on the supplied mux. The bearer token is enforced by
// the outer requireBearer middleware on each route.
//
// d must be non-nil (the daemon must be constructed but does NOT need
// to be Started — Handler() returns the registered mux either way).
// Returns the registered prefixes for tests/diagnostics.
func MountDaemonQueue(mux *http.ServeMux, d *daemon.Daemon, bearer string) (canonical string, aliases []string) {
	if mux == nil || d == nil {
		return "", nil
	}
	inner := d.Handler()

	// Canonical: /v1/queue/<rest> → strip /v1/queue → forward.
	stripped := http.StripPrefix("/v1/queue", inner)
	canonicalAuth := requireBearerOrPassthrough(bearer)(stripped)
	mux.Handle("/v1/queue/", canonicalAuth)

	// Deprecated alias paths. We register each one explicitly so the
	// alias mount cannot swallow paths that belong to the agentserve
	// mount (e.g. /api/capabilities, /api/task). This is a closed list,
	// not a prefix wildcard.
	queueAliasPaths := []string{
		"/api/enqueue",
		"/api/status",
		"/api/workers",
		"/api/pause",
		"/api/resume",
		"/api/tasks",
		"/api/tasks/get",
		"/api/tasks/cancel",
		"/api/wal",
		"/api/rules",
		"/api/rules/",
		"/api/hooks/install",
	}

	// The aliases must reach the inner handler with the leading
	// /api stripped. Build a per-alias rewriter so /api/enqueue →
	// /enqueue when forwarded.
	aliasHandler := http.StripPrefix("/api", inner)
	aliasAuth := requireBearerOrPassthrough(bearer)(deprecationHeader(aliasHandler))
	for _, p := range queueAliasPaths {
		mux.Handle(p, aliasAuth)
	}

	return "/v1/queue/", queueAliasPaths
}
