# R1 Agent — VS Code extension

Invoke the local r1-agent daemon from inside VS Code.

## Commands

| Command | ID | What it does |
| --- | --- | --- |
| R1: Open Chat Panel | `r1.chat.open` | Opens a webview chat that POSTs to `/api/task`. |
| R1: Run Task... | `r1.run.task` | Prompts for an objective, submits, streams result to an output channel. |
| R1: Explain Selection | `r1.explain.selection` | Sends the active editor's selection + filename to the daemon. |

## Settings

| Key | Default | Purpose |
| --- | --- | --- |
| `r1.daemonUrl` | `http://127.0.0.1:7777` | Base URL of the daemon. |
| `r1.apiKey` | (empty) | Bearer token; falls back to `R1_API_KEY` env var. |
| `r1.taskType` | `explain` | Default task_type for run/explain commands. |
| `r1.timeoutMs` | `120000` | HTTP request timeout. |

## Build

```bash
npm install
npm run compile
npx @vscode/vsce package
```

Produces `r1-agent-<version>.vsix`. Install with
`code --install-extension r1-agent-<version>.vsix`.

## Protocol

See `../PROTOCOL.md` for the daemon HTTP contract this extension speaks.

## Tests

```bash
npm test
```

Tests run as plain mocha against the daemon-client wrapper using a
local `http.createServer` mock — no `@vscode/test-electron` round-trip
required for the unit suite.
