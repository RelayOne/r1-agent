# tests/agent/cli/session-start-stop.agent.feature.md

<!-- TAGS: smoke, cli -->
<!-- DEPENDS: r1d-server -->

## Scenario: Headless CLI can start and stop a session via r1.cli.invoke

- Given a fresh r1d daemon at "stdio"
- When r1 cli invoke with args ["session", "start", "--workdir", "/tmp/agentic-cli-1"] writes the session_id to stdout
- Then exit_code is 0
- And r1.session.list reports a session with workdir "/tmp/agentic-cli-1"

## Scenario: CLI invoke returns process-group-isolated exit codes

- Given a worktree at "/tmp/agentic-cli-2"
- When r1 cli invoke with args ["bogus-subcommand"] runs
- Then exit_code is non-zero
- And stderr contains "unknown" (the Cobra-shaped diagnostic)
- And duration_ms is below 5000 (process did not hang)

## Scenario: CLI invoke honors the timeout_sec parameter

- Given a worktree at "/tmp/agentic-cli-3"
- When r1 cli invoke with args ["sleep", "60"] and timeout_sec=1 runs
- Then exit_code is non-zero
- And duration_ms is below 2000 (timeout was enforced)
- And the spawned process group was terminated cleanly (no orphan PIDs)

## Tool mapping (informative)
- "r1 cli invoke" -> r1.cli.invoke
- "r1.session.list reports" -> r1.session.list
