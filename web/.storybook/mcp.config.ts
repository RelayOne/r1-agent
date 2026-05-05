// web/.storybook/mcp.config.ts — Storybook MCP server configuration
// per specs/agentic-test-harness.md §7 + §12 item 31.
//
// Exposes the Storybook story registry (with a11y metadata and
// interaction graphs) as an MCP server. Agents query it via:
//
//   npx storybook-mcp@^9 validate web/.storybook/mcp.config.ts \
//     --fail-on-missing-a11y
//
// to confirm every component declares the parameters.agentic.
// actionables contract from §7 AND every actionable references an
// MCP tool that exists in the r1.* catalog.
//
// Port 6007 is the operator-conventional Storybook MCP port; the
// stdio transport is the default for CI runs (no firewall surface).
export default {
  port: 6007,
  transport: "stdio" as const,
  expose: ["stories", "a11y", "interactions"] as const,
  // Validation rule names per storybook-mcp@^9. Each rule corresponds
  // to a specific failure mode the lint scanner depends on.
  rules: {
    requireA11yParameters: true,
    requireAgenticActionables: true,
    requireMCPToolReference: true,
    rejectUnknownMCPTool: true,
  },
};
