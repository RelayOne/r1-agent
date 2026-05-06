// stoke_server.go — Deprecated shim per specs/agentic-test-harness.md §12 item 2.
//
// Background: in #122 the canonical implementation was renamed
// stoke_server.go → r1_server.go. External callers that imported
// stoke_server.go directly (e.g. via go-mod path-aware tooling) had no
// re-export. This file restores the file-level export surface so that:
//
//   - mcp.NewStokeServer continues to be the public constructor name.
//   - Anyone grepping for `stoke_server.go` in the repo finds a clearly
//     marked deprecated shim that points to the canonical r1_server.go.
//
// Per the spec, both names dispatch through the SAME StokeServer struct
// (the alias is at the tool-name layer via canonicalStokeServerToolName,
// not at the Go-symbol layer). This file therefore contains no logic; it
// only documents the rename and provides the StokeServerLegacyAlias type
// alias for any consumer that pinned the old type name in a build tag.
//
// Removal scheduled in v2.0.0 per CHANGELOG.
package mcp

// StokeServerLegacyAlias is an alias for *StokeServer kept for backward
// compatibility with code that imported "stoke_server.StokeServer". New
// code should use *StokeServer directly. This alias is removed in v2.0.0.
//
// Deprecated: use *StokeServer.
type StokeServerLegacyAlias = StokeServer
