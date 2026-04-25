# Task 06 — MCP client tool call

**Ability under test:** MCP client (row #18 in parity matrix)  
**Reference product:** Claude Code (MCP client, `ListMcpResourcesTool`, `ReadMcpResourceTool`)  
**R1 equivalent:** `internal/mcp/client.go`, `internal/mcp/registry.go`, `internal/mcp/discovery.go`

## Task description

This task verifies R1's MCP client package is functional by inspecting
the code and confirming the transport implementations exist.

**Part A — Code inspection:**

1. Read `internal/mcp/client.go` and confirm it implements:
   - An `initialize` handshake method
   - A `call_tool` or equivalent method
   - At least one transport (stdio or SSE)

2. Read `internal/mcp/registry.go` and confirm:
   - It can register MCP servers
   - It can list registered tools

3. Check whether the MCP server binary exposes tools:
   ```bash
   grep -n "Name:" /home/eric/repos/stoke/cmd/stoke-mcp/backends.go | head -20
   ```

**Part B — MCP server smoke test:**

4. Check if the MCP server compiles:
   ```bash
   go build -C /home/eric/repos/stoke ./cmd/stoke-mcp/ 2>&1
   ```
   Report: PASS (exits 0) or FAIL (compile error).

**Part C — Gap assessment:**

5. Check whether R1 has `ListMcpResourcesTool` equivalent:
   ```bash
   grep -r "resource" /home/eric/repos/stoke/internal/mcp/ --include="*.go" -l
   ```
   If no resource listing found, confirm rows #20 and #21 as GAP.

## Acceptance criteria

- [ ] `client.go` has initialize + tool-call method
- [ ] `registry.go` has register + list functionality
- [ ] MCP server binary compiles cleanly
- [ ] MCP resource gap (rows #20, #21) confirmed or corrected

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: code inspection passes but compile fails
- FAIL: MCP package missing or unreadable
