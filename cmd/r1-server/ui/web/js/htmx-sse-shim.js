// cmd/r1-server/ui/web/js/htmx-sse-shim.js
//
// Spec 4 §8 + §10 T20: client-side glue for the two htmx-ext-sse 2.2.4
// reconnect gotchas surfaced by RT-HTMX-SSE-DATA-ATTRS.
//
// Half 1 — last_event_id URL fallback.
//   htmx-ext-sse constructs a fresh EventSource on its own backoff
//   path, which resets EventSource.lastEventId to "" — meaning the
//   server's Last-Event-ID header arrives empty after a reconnect.
//   We hook htmx.createEventSource (the extension's documented
//   override point) and rebuild the URL to include
//   `?last_event_id=<cached>` whenever we have a cached cursor.
//
// Half 2 — `event: resync` listener.
//   The Go handler emits `event: resync` with `data: {oldest_available:N}`
//   when the supplied cursor has fallen below the bus retention floor.
//   Reload the page so the client re-reads the full backlog from
//   the new floor; without this, the SSE feed silently drops events.
//
// Loaded from base.html via:
//   <script type="module" src="/ui/web/js/htmx-sse-shim.js" defer></script>

const cursorByURL = new Map(); // base-URL → last-seen-event-id

function appendCursor(url, cursor) {
  if (!cursor) return url;
  // Use URL constructor — handles existing query params correctly.
  let u;
  try {
    u = new URL(url, window.location.origin);
  } catch (_) {
    return url;
  }
  u.searchParams.set('last_event_id', String(cursor));
  // Return relative path + query if the original was relative,
  // otherwise the full URL.
  if (url.startsWith('/')) return u.pathname + u.search;
  return u.toString();
}

function baseURL(url) {
  try {
    const u = new URL(url, window.location.origin);
    u.searchParams.delete('last_event_id');
    return u.pathname + u.search;
  } catch (_) {
    return url;
  }
}

function installShim() {
  const htmx = window.htmx;
  if (!htmx || typeof htmx.createEventSource !== 'function') {
    // Spec 4 §10 T20 — htmx may not be loaded yet (defer ordering).
    // Retry after the next paint; if it's still not there, give up
    // silently (the page works without the shim, just less reliably
    // on reconnect).
    return false;
  }

  const original = htmx.createEventSource;
  htmx.createEventSource = function (url) {
    const base = baseURL(url);
    const cursor = cursorByURL.get(base);
    const finalURL = appendCursor(url, cursor);
    const es = original.call(this, finalURL);
    if (!es || typeof es.addEventListener !== 'function') return es;

    es.addEventListener('message', (ev) => {
      if (ev && ev.lastEventId) cursorByURL.set(base, ev.lastEventId);
    });
    es.addEventListener('resync', () => {
      // Server signalled cursor_pruned. Reload from the new floor.
      // Use replace() so the back button doesn't snap to the
      // pre-reload state.
      window.location.replace(window.location.href);
    });
    return es;
  };
  return true;
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => {
      if (!installShim()) {
        // One retry on next macrotask in case htmx-ext-sse loads later.
        setTimeout(installShim, 50);
      }
    }, { once: true });
  } else {
    if (!installShim()) setTimeout(installShim, 50);
  }
}

// Exports for vitest (Spec 5 will exercise these).
export { appendCursor, baseURL, installShim };
