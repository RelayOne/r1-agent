# Integrations Architecture Reference

## Source

Derived from the "Building the central nervous system for AI coding agents" research document. This is architectural research about how an event-bus-driven AI coding tool can integrate with development ecosystem protocols.

**This is NOT a skill.** It is reference material for future Stoke integration work.

## Potential Integration Points

### Language Server Protocol (LSP)
- Real-time diagnostics, completions, and code actions
- Stoke could expose an LSP server for IDE integration
- Agent loop could consume LSP diagnostics instead of running separate lint passes

### Debug Adapter Protocol (DAP)
- Programmatic debugging sessions for test failure investigation
- Agent could set breakpoints and inspect state during verification

### Server-Sent Events (SSE) / WebSocket
- Real-time progress streaming to web dashboards
- The hub event bus naturally maps to SSE event streams

### OpenTelemetry
- Distributed tracing across agent turns, tool executions, and API calls
- Cost attribution per trace span
- Export to Jaeger/Tempo for debugging multi-agent sessions

### GitHub Integration
- GitHub Checks API for CI/CD status
- GitHub App for PR automation
- Code scanning alerts integration with security hub subscribers

### Prometheus / Grafana
- Metrics exporter for bench framework dashboards
- Cost tracking, honesty scores, and task throughput gauges
- SLO-based alerting on agent behavior regression

## Implementation Priority

1. SSE streaming (hub → web dashboard) — most value for TUI/web UX
2. OpenTelemetry tracing — debugging multi-turn agent sessions
3. GitHub App — PR-based workflow automation
4. LSP — IDE integration for live agent assistance
