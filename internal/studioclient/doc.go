// Package studioclient implements the R1 side of the Actium Studio
// skill pack wire — the Transport that carries a skill invocation from
// R1 over to Studio. Two transports are supported, as specified by
// work-r1-actium-studio-skills.md §3:
//
//   - HTTPTransport: calls Studio's REST API directly.
//   - StdioMCPTransport: spawns Studio's bundled MCP server as a
//     subprocess per R1 session and speaks JSON-RPC over stdin/stdout.
//
// Resolve(cfg) picks the right implementation from StudioConfig so
// skills stay transport-agnostic.
//
// # Degradation stance
//
// When Studio is unreachable, the caller gets a typed error
// ErrStudioUnavailable. IsUnavailable(err) returns true for that case
// and for any context-cancellation / DNS / dial / connection-refused
// failure the transport couldn't classify more precisely. Skills
// surface this to the agent as "Studio endpoint not reachable — check
// studio_config or disable this step"; the session stays alive. No
// cross-product hard requirement per work order §Degradation-stance.
//
// # Observability
//
// Each invocation emits a minimal record to the injected EventPublisher
// (nil-safe). The record carries tool name, HTTP status (where
// meaningful), duration, and a boolean `ok`. No PII, no payload body,
// no token echo.
package studioclient
