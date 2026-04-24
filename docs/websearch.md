# Websearch — Domain Allowlist and Body Cap

The `internal/websearch` package is the adapter layer R1's feasibility
gate uses to retrieve external API documentation. The gate refuses to
build code against a service that has no documentation coverage, so
websearch is on the critical path for any SOW that references a
third-party API. That makes it an attacker-controllable intake surface:
a malicious doc page can carry payloads designed to hijack the LLM that
reads the fetched body. Track A Task 4 adds two mitigations.

## What shipped

### `FetchConfig.DomainAllowlist`

A list of glob patterns (matched via `path.Match`) against the
request URL's host. When non-empty, a request to any host that does
not match at least one pattern is refused with an error containing
the literal substring `not in allowlist`.

Examples:

```go
cfg := websearch.FetchConfig{
    DomainAllowlist: []string{
        "*.github.com",          // any github.com subdomain
        "docs.anthropic.com",    // exact match
        "*.readthedocs.io",      // any readthedocs project
    },
}
body, err := websearch.Fetch(ctx, url, cfg)
```

Matching is **case-insensitive** (both host and pattern are
lower-cased). Patterns use `path.Match` semantics — `*` matches a
single path segment, so `*.github.com` matches `api.github.com` but
NOT bare `github.com`. Add `github.com` explicitly when you want to
allow the apex.

### `FetchConfig.MaxBodyBytes`

A per-fetch body cap in bytes. When the response exceeds the cap,
the returned byte slice is truncated to exactly `MaxBodyBytes` and
the literal marker `\n\n[truncated at N bytes]` is appended so
downstream consumers (and any LLM that reads the payload) can see
the cap fired.

## Defaults (backward compatibility)

| Field             | Zero value | Meaning                                     |
| ----------------- | ---------- | ------------------------------------------- |
| `DomainAllowlist` | `nil`      | All hosts are allowed (no restriction).     |
| `MaxBodyBytes`    | `0`        | `DefaultMaxBodyBytes` (100 KB) is applied.  |
| `HTTPClient`      | `nil`      | A `http.Client` with a 20 s timeout is used.|

Zero-value `FetchConfig{}` therefore preserves existing dev + CI
behavior: any host, 100 KB cap.

## Recommendation for production deployments

**Configure an allowlist in production.** The empty-allowlist default
is safe for dev/CI, where blocking a new domain would be operator-
surprising. But for any deployment that processes real SOWs against
third-party services, the operator should enumerate the doc domains
the mission actually needs:

```go
websearch.FetchConfig{
    DomainAllowlist: []string{
        "docs.stripe.com",
        "developer.twilio.com",
        "*.github.com",
        "*.readthedocs.io",
        "docs.anthropic.com",
    },
    MaxBodyBytes: 200_000, // bump if you have legitimately large doc pages
}
```

The cap can also be tuned upward (e.g. 200 KB) when legitimate doc
pages are known to be larger than the default, but leave it bounded —
the goal is to bound the memory an attacker can force R1 to
allocate by pointing a feasibility search at a never-ending stream.

## Where this runs

`websearch.Fetch` is the single entry point for full-body fetches.
Callers that need content past what a `Searcher` already returned
(Tavily's `raw_content`, Shell's stdout JSON) should go through
`Fetch` so the allowlist + cap are enforced uniformly.

## Failure modes

| Condition                             | Behavior                                              |
| ------------------------------------- | ----------------------------------------------------- |
| Host not in non-empty allowlist       | Error with `"not in allowlist"` substring, no fetch.  |
| Body exceeds `MaxBodyBytes`           | Truncated to cap + `[truncated at N bytes]` marker.   |
| Body at or below `MaxBodyBytes`       | Returned verbatim, no marker.                         |
| URL unparseable                       | Error, no fetch.                                      |
| HTTP transport error                  | Error wraps the underlying error with URL context.    |

## Related

- `internal/promptguard` — intake-time injection-shape scanner. The
  allowlist + cap bounds *what* gets fetched; promptguard scans *what
  the LLM reads* for injection phrasing.
- `internal/plan/feasibility.go` — the feasibility gate that invokes
  the websearch chain. Web-search result bodies are also routed
  through promptguard before landing in task briefings.
