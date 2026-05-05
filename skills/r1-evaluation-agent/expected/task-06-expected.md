# Expected Output — Task 06 (MCP tool call)

## Pass criteria (pinned assertions)

### Part A — client.go inspection
- `client.go` exists under `internal/mcp/`
- Contains an initialize method or function
- Contains a tool invocation method or function
- Transport implementation present (at minimum stdio)

### Part B — MCP server build
- `go build ./cmd/r1-mcp/` exits with code 0
- No compile errors

### Part C — resource gap
- `grep` for "resource" in `internal/mcp/` either:
  - Returns zero matches (confirming rows #20, #21 as GAP), OR
  - Returns resource-list/read methods (updating rows #20, #21 to PARITY)
- Either outcome is PASS; honesty is the criterion

## Allowed variance

- MCP client transport names may vary (stdio/sse/http naming conventions)
- Discovery.go may implement resource listing — check before reporting gap

## Failure indicators

- Claiming PARITY on MCP resources without grep evidence
- MCP server failing to compile (would be a real regression)
