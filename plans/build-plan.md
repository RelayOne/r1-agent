# Build Plan — lanes-protocol (BUILD_ORDER 3)

Spec: specs/lanes-protocol.md. Branch: build/lanes-protocol. Started 2026-05-02.

40 items grouped:
- B1 hub + cortex types (1-7): event consts, LaneEvent payload, LaneStatus, LaneKind, Lane struct, Transition validation, seq allocation, ULID
- B2 streamjson + fixtures (8-12): lane.go, isCriticalType, golden fixtures, tests
- B3 server transport (13-17): HTTP+SSE, WS upgrade, JSON-RPC subscribe, WAL replay, session.bound
- B4 MCP tools (18-25): lanes_server.go, 5 tools, registry, lane_rpc_test.go
- B5 desktop IPC + docs (26-32): IPC-CONTRACT.md additions, version handshake, gap-detection guidance, dual-emission window
- B6 testing + verify (33-40): backward-compat tests, golden replay, soak, agentic harness hooks

## Status

In progress.
