# MCP IDE Bundles

`r1 ide install <ide>` registers R1's stdio MCP server with your IDE
so the local R1 toolset is available alongside any other MCP servers
you already use. Spec: `specs/mcp-ide-bundles.md` (C4).

Four IDEs are supported in this release: Cursor, Windsurf, VS Code,
and JetBrains. Zed / Helix / Neovim / Emacs are deferred.

## Quickstart

### Cursor

```bash
# Workspace-scope (default for Cursor): writes ./.cursor/mcp.json.
r1 ide install cursor --workspace .

# User-scope: writes ~/.cursor/mcp.json.
r1 ide install cursor --global
```

Expected confirmation:

```
installed: r1 -> /home/u/proj/.cursor/mcp.json
note: open Cursor and toggle the MCP panel; r1 should appear in the list.
```

Where R1 surfaces: Cursor's settings panel under "MCP".

### Windsurf (Codeium)

Windsurf is global-only (`--workspace` is rejected).

```bash
r1 ide install windsurf
```

Expected confirmation:

```
installed: r1 -> /home/u/.codeium/windsurf/mcp_config.json
note: open Windsurf and the Cascade panel lists r1 under MCP servers.
```

Where R1 surfaces: the Cascade side panel, MCP servers section.

### VS Code

VS Code uses the JSON root key `servers` (not `mcpServers` — common
gotcha). MCP tools fire only in Copilot Agent mode.

```bash
r1 ide install vscode --workspace .   # writes ./.vscode/mcp.json
r1 ide install vscode --global        # writes per-platform user dir
```

Expected confirmation:

```
installed: r1 -> /home/u/proj/.vscode/mcp.json
note: VS Code MCP tools are only invoked in Copilot Agent mode;
      switch the Copilot panel to "Agent" to use R1.
```

Where R1 surfaces: Copilot Agent mode tool list.

### JetBrains

Drops `r1-mcp-bridge.jar` into the IDE's plugins directory; restart
the IDE to load the plugin.

```bash
r1 ide install jetbrains                          # auto-detects highest IntelliJ IDEA version
r1 ide install jetbrains --ide GoLand             # target GoLand
r1 ide install jetbrains --ide PyCharm --version 2026.2
```

The bundled jar is searched in three locations:

1. `$R1_JETBRAINS_JAR` (operator override)
2. `<r1-install-prefix>/share/r1/plugins/r1-mcp-bridge.jar`
3. `<repo>/ide/jetbrains/build/distributions/r1-mcp-bridge.jar`

If none is present and `--skip-download` is not passed, the CLI falls
back to downloading from
`https://github.com/RelayOne/r1/releases/download/v<ver>/r1-mcp-bridge-<ver>.jar`.

Where R1 surfaces: a new tool window "R1 MCP Bridge" and the AI
Assistant settings panel.

## Verify

```bash
r1 ide verify
```

Output:

```
cursor    | registered   | /home/u/.cursor/mcp.json
windsurf  | not-found    | /home/u/.codeium/windsurf/mcp_config.json
vscode    | unregistered | /home/u/.config/Code/User/mcp.json
jetbrains | registered   | /home/u/.local/share/JetBrains/IntelliJIdea2026.2/plugins/
```

Exit code is always 0 — verify is a report, not a gate. Three
status words: `registered`, `unregistered`, `not-found`.

## Uninstall

```bash
r1 ide uninstall cursor
r1 ide uninstall windsurf
r1 ide uninstall vscode
r1 ide uninstall jetbrains
```

Behavior:

- For JSON-config IDEs: if `<file>.r1-backup` exists, the backup is
  atomically restored. Otherwise the `r1` entry is removed in place
  and the rest of the config is preserved.
- For JetBrains: deletes `r1-mcp-bridge.jar` from the plugins
  directory; other jars are untouched.

Exit 1 with `error: r1 is not installed in <ide>` if the entry / jar
is already absent.

## Troubleshooting

### "config file not found"

Run `r1 ide verify` to see exactly where R1 looked.

### "IDE not picking up R1"

- Cursor / Windsurf / VS Code: restart the IDE; MCP server lists
  are read at startup.
- VS Code only: confirm the Copilot panel is in "Agent" mode — MCP
  tools are silent in Copilot Chat mode.
- JetBrains: restart the IDE; check `Help → About → Installed
  Plugins` for "R1 Agent".

### "JetBrains plugin not loading"

- Check `idea.log` for `ClassNotFoundException` — usually a JDK
  mismatch (the plugin targets Java 17+).
- Confirm `r1 --version` works on PATH; the plugin probes for it on
  startup and shows a balloon notification if `r1` is missing.

### "r1 not found on PATH" (JetBrains plugin)

Install R1 from https://github.com/RelayOne/r1/releases and restart
the IDE. The plugin re-probes on startup.

### "config-merge conflict" (exit 2)

The IDE's config file is corrupt JSON. R1 refuses to overwrite a
file it cannot parse. Fix the JSON by hand or delete the file and
re-run `r1 ide install`.

### "JetBrains jar missing" (exit 4)

The bundled jar wasn't found in any of the three search paths and
either `--skip-download` was passed or the GitHub release download
failed. Either:

- Set `R1_JETBRAINS_JAR=/path/to/r1-mcp-bridge.jar` and retry, or
- Drop network restrictions and let R1 download from GitHub, or
- Build the plugin locally: `cd ide/jetbrains && ./gradlew buildPlugin`.

## CI considerations

The Go side of the installer (`internal/ideinstall/`) builds and
tests on Linux, macOS, and Windows. Per-platform path tests are
build-tagged; per-OS CI runners exercise the relevant subset.

The JetBrains plugin is built separately by a CI job with JDK 17 +
Gradle wrapper — see
`docs/integrations/jetbrains-plugin-signing.md` for the signing
flow. The signed jar is a release artifact published alongside the
`r1` binary.

## First-run prompt

`r1 chat` (the interactive front door) calls
`MaybePromptIDEInstall` once per user account. If exactly one
supported IDE is detected but unregistered, R1 asks:

```
R1 detected cursor — install R1 MCP server in cursor? [y/N]
```

The answer is recorded in `~/.r1/ide-prompt-acked` so the prompt
never fires twice. Non-TTY stdin (`r1 chat <task` or
`--non-interactive`) skips the prompt entirely.

Sources: this section was derived from the four research queries
documented in `specs/mcp-ide-bundles.md` §Research summary
(2026-05-11).
