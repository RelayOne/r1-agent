# R1 Agent — JetBrains plugin

IntelliJ Platform plugin that talks to the local r1-agent daemon
(`stoke agent-serve`) over HTTP. Compatible with IntelliJ IDEA, PyCharm,
WebStorm, GoLand, RustRover and every other JetBrains 2024.1+ IDE.

## Actions

| Action | What it does |
| --- | --- |
| **R1: Open Chat Panel** | Surfaces the `R1 Chat` tool window. |
| **R1: Run Task...** | Prompts for an objective, submits, surfaces result via balloon notification. |
| **R1: Explain Selection** | Sends current editor selection + filename to the daemon. |

All three are also reachable from the right-click context menu on the
editor and from the **Tools** menu.

## Settings

`Settings | Tools | R1 Agent`

| Field | Default | Notes |
| --- | --- | --- |
| Daemon URL | `http://127.0.0.1:7777` | Override per-IDE. |
| API key | (empty) | Sent as `X-Stoke-Bearer`. Falls back to `R1_API_KEY` env var. |
| Default task_type | `explain` | Must be advertised by `/api/capabilities`. |
| Timeout (ms) | `120000` | HTTP request timeout. |

Persisted via `PersistentStateComponent` to `r1-agent.xml` in the
IDE's options directory.

## Build

```bash
./gradlew buildPlugin
```

Produces `build/distributions/r1-agent-jetbrains-0.1.0.zip`. Install
via **Settings | Plugins | (gear) | Install Plugin from Disk**.

First run downloads the IntelliJ Platform SDK (~1.5 GB); subsequent
runs are incremental.

## Test

```bash
./gradlew test
```

The test suite hits a local `com.sun.net.httpserver.HttpServer` and
exercises the same wire format documented in `../PROTOCOL.md`.

## Protocol

See `../PROTOCOL.md` — the JetBrains plugin and the VS Code extension
share zero code but stay aligned through that document.
