<!-- STATUS: ready -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 43 -->

# mcp-ide-bundles — Native MCP server bundles for Cursor, Windsurf, VS Code, JetBrains

## Overview

R1 already ships a stdio MCP JSON-RPC server (`r1 mcp serve`, spec
`specs/r1-mcp-serve.md`, STATUS:done) exposing the 38-tool `r1.*`
catalog across 10 categories (`internal/mcp/r1_server_catalog.go`).
Operators today must hand-author per-IDE config files to expose those
tools to Cursor, Windsurf, VS Code, or JetBrains.

This spec adds `r1 ide install <ide-name>` that performs the
IDE-native registration automatically. The four IDE install flows
share ~80% of the same code (detect → read config → merge R1
stanza → atomic write → confirm). JetBrains is the outlier — it
needs a Kotlin plugin (`.jar`), not just a JSON edit — and is the
heaviest workstream.

## Research summary (2026-05-11)

Anchor facts from the four research queries. Per-IDE tasks reference
this section; do not re-derive paths.

**Cursor** — config at `~/.cursor/mcp.json` (global) or
`<workspace>/.cursor/mcp.json` (workspace). Root key `mcpServers`,
per-server shape `{"command", "args", "env"}`.

**Windsurf (Codeium)** — global only. Config at
`~/.codeium/windsurf/mcp_config.json` on macOS/Linux,
`%USERPROFILE%\.codeium\windsurf\mcp_config.json` on Windows.
Root key `mcpServers`, same shape as Cursor.

**VS Code** — config at `<workspace>/.vscode/mcp.json` (workspace)
or per-platform user dir (see T5). Root key is **`servers`** (not
`mcpServers` — common gotcha). Per-server shape adds
`"type": "stdio"`. MCP tools fire only in Copilot Agent mode.

**JetBrains** — since 2025.2 the IDEs ship a bundled MCP Server
plugin that turns the IDE *into* a server. That bundled plugin does
NOT auto-register external MCP servers (like R1) via a JSON file —
the AI Assistant settings panel is the only documented surface.
Therefore R1 ships its own plugin (`ide/jetbrains/`) that spawns
`r1 mcp serve`, proxies MCP traffic to the AI Assistant, and adds a
"Configure R1" tool-window panel. Distributed as a `.jar` dropped
into `~/.{platform}/{IntelliJIdea,GoLand,PyCharm,WebStorm}<ver>/plugins/`
by `r1 ide install jetbrains`. Plugin assumes `r1` is on PATH.

## Existing R1 surface (verified 2026-05-11 on dev)

- `cmd/r1/mcp.go:103-143` — `runMCPServe`, the stdio MCP JSON-RPC
  server entry. Reads stdin, writes stdout, logs to stderr.
- `cmd/r1/mcp_serve_runtime.go` — server runtime + lifecycle.
- `internal/mcp/r1_server.go` — `StokeServer`, `WithCortex`,
  `WithAuthKey`.
- `internal/mcp/r1_server_catalog.go` — 38-tool `R1ToolCatalog`.
- `cmd/r1/serve_install.go` — the one-shot lifecycle pattern
  (`runServeInstall` / `runServeUninstall` / `runServeStatus`)
  that `r1 ide install` mirrors.
- `internal/serviceunit/` — platform-detection + atomic-write
  pattern reused by `internal/ideinstall/`.

## Stack & Versions

- **Go 1.25.5** for CLI + detection + installer (stdlib only:
  `encoding/json`, `os`, `path/filepath`, `runtime`).
- **Kotlin 1.9.x + Gradle 8.x** for the JetBrains plugin; output
  `r1-mcp-bridge-<ver>.jar` in `ide/jetbrains/build/libs/`.
- **IntelliJ Platform Plugin SDK** 2026.1+ — minimum supported
  IDE version is IntelliJ IDEA 2026.1.

## Public surface

CLI commands (new top-level group `r1 ide …`):

- `r1 ide install cursor [--workspace .|--global]`
- `r1 ide install windsurf` (workspace not supported)
- `r1 ide install vscode [--workspace .|--global]`
- `r1 ide install jetbrains [--ide IntelliJIdea|GoLand|PyCharm|WebStorm] [--version <ver>]`
- `r1 ide uninstall <ide-name>`
- `r1 ide verify`

Exit codes (install): 0 ok / 1 IDE not detected or usage error /
2 config-merge conflict / 3 write failure / 4 JetBrains jar
missing. Uninstall: 0 ok / 1 not installed. Verify: always 0.

### File layout

```
cmd/r1/ide_install_cmd.go              # CLI dispatch (~200 LOC)
internal/ideinstall/
    detect.go config.go
    cursor.go windsurf.go vscode.go jetbrains.go
    *_test.go (+ build-tagged per-platform companion files)
ide/jetbrains/
    build.gradle.kts settings.gradle.kts
    src/main/kotlin/com/relayone/r1/
        R1McpBridgePlugin.kt R1McpBridgeProcess.kt R1McpBridgeToolWindow.kt
    src/main/resources/META-INF/plugin.xml
docs/integrations/
    ide-bundles.md jetbrains-plugin-signing.md
```

## Design decisions

**D-1. Shared skeleton.** All four installers share
`internal/ideinstall/config.go` for read-merge-backup-write. Per-IDE
files differ only in path resolution + JSON shape projection.
JetBrains copies a `.jar` instead of editing JSON but uses the same
`Detector` → `Installer` → `Verifier` interface.

**D-2. Merge preserves unrelated entries.** Parse existing config
into `map[string]any`, upsert the `r1` key under `mcpServers` (or
`servers` for VS Code), re-encode. Unrelated entries (operator's
GitHub MCP server, etc.) preserved. Original backed up to
`<file>.r1-backup` before write.

**D-3. Atomic write via tmpfile + rename.** Pattern from
`internal/atomicfs/`: write `<file>.tmp`, then `os.Rename`. A
corrupt `mcp.json` can break the IDE's MCP subsystem entirely.

**D-4. Detect by config-dir, not PATH.** Probe canonical config-dir
parents (`~/.cursor`, `~/.codeium/windsurf`, etc.); do NOT call
`which <ide>`. Flatpak/Snap/Toolbox installs have the binary off
PATH but the config dir in the standard location.

**D-5. Bundled `.jar`, no install-time compilation.** R1 releases
ship `r1-mcp-bridge-<ver>.jar`. `r1 ide install jetbrains` copies
the bundled jar (or downloads from the matching GitHub release).
Operators do not need a JDK + Gradle to install.

**D-6. `--workspace` / `--global` mutually exclusive.** Default
scope: Cursor + VS Code → workspace; Windsurf + JetBrains → global
(both are global-only — passing `--workspace` exits 1).

**D-7. Signing out-of-band.** R1's CI signs the release jar using
keys documented in `docs/integrations/jetbrains-plugin-signing.md`.
Plugin distributed via R1 release tarball; Marketplace publish
deferred.

## Tasks

### T1 — Shared installer subcommand

File: `cmd/r1/ide_install_cmd.go` (NEW, ~200 LOC).

#### T1.1 — Dispatch `r1 ide` to subcommand runners

MUST:
- Add a top-level case `"ide"` to the dispatcher in
  `cmd/r1/main.go` alongside the existing `"mcp"` case.
- New `cmd/r1/ide_install_cmd.go` exports `ideCmd(args []string)`
  matching the signature of `mcpCmd`.
- Subcommand routing: `install`, `uninstall`, `verify`,
  `-h`/`--help`.
- Each `install` invocation requires a second positional in
  `{cursor, windsurf, vscode, jetbrains}`. Missing/invalid →
  usage + exit 1.

VERIFY:
- `cmd/r1/ide_install_cmd_test.go` table-tests every invocation
  (missing args, unknown IDE name, valid IDE names dispatch to
  the right runner) using a `runnerLoader` injection mirroring
  `registryLoader` in `mcp.go`.

#### T1.2 — Flags `--workspace` / `--global` mutually exclusive

MUST:
- Both flags set → exit 1 with
  `error: --workspace and --global are mutually exclusive`.
- Default scope: Cursor → workspace; Windsurf → global (no
  workspace support); VS Code → workspace; JetBrains → global.
- `--workspace .` resolves against `os.Getwd()` (no symlink
  resolution).

VERIFY:
- Tests: both flags → exit 1; only `--workspace` → workspace;
  only `--global` → global; neither → per-IDE default.

### T2 — Detection logic

File: `internal/ideinstall/detect.go` (NEW, ~150 LOC).

#### T2.1 — `Detector` interface + per-IDE implementations

MUST:
- Define `type Scope string` with `ScopeWorkspace`, `ScopeGlobal`
  constants and:
  ```go
  type Detector interface {
      IDE() string  // cursor|windsurf|vscode|jetbrains
      DetectConfig(scope Scope, workspaceDir string) (path string, found bool, err error)
      SupportsScope(scope Scope) bool
  }
  ```
- Four concrete detectors (one per IDE, each in its own file per
  T3–T6).
- `func DetectorFor(ide string) (Detector, error)` returns the
  detector or an error enumerating valid IDE names.

VERIFY:
- `detect_test.go`: every IDE returns the correct path on a fake
  HOME; `Windsurf.SupportsScope(ScopeWorkspace) == false`; unknown
  IDE name returns `error: unknown ide` with all 4 valid names.

#### T2.2 — Platform-aware path resolution

MUST:
- `func userConfigDir(ide string) (string, error)` switches on
  `runtime.GOOS` and uses `os.UserHomeDir()`.
- Required mappings:
  - Cursor global: `<HOME>/.cursor/mcp.json` (all platforms).
  - Windsurf global: `<HOME>/.codeium/windsurf/mcp_config.json`
    (macOS/Linux); `%USERPROFILE%\.codeium\windsurf\mcp_config.json`
    (Windows).
  - VS Code user: `<HOME>/.config/Code/User/mcp.json` (Linux);
    `<HOME>/Library/Application Support/Code/User/mcp.json`
    (macOS); `%APPDATA%\Code\User\mcp.json` (Windows).
  - VS Code workspace: `<workspace>/.vscode/mcp.json`.
  - JetBrains plugins dir: per-IDE, per-version (see T6.3).
- All path joins via `filepath.Join` (no string concatenation).

VERIFY:
- Build-tagged tests (`detect_linux_test.go`,
  `detect_darwin_test.go`, `detect_windows_test.go`) each set HOME
  (or USERPROFILE + APPDATA) to `t.TempDir()` and assert the
  returned path matches the per-platform spec verbatim.

### T3 — Cursor installer

File: `internal/ideinstall/cursor.go` (NEW, ~80 LOC).

#### T3.1 — Cursor `InstallCursor` API + R1 stanza shape

MUST:
- `func InstallCursor(scope Scope, workspaceDir string) (Result, error)`
  resolves path via `cursorDetector.DetectConfig`.
- If the config file doesn't exist, create parent dir
  (`os.MkdirAll(filepath.Dir(p), 0o755)`) and seed
  `{"mcpServers": {}}`, then merge.
- R1 stanza:
  ```json
  {"r1": {"command": "r1", "args": ["mcp", "serve"], "env": {}}}
  ```

VERIFY:
- No existing config → installer creates parent dir + file with
  one `r1` entry; `Result.Action == ResultAdded`.
- Existing config with one unrelated `github` entry → installer
  preserves `github` and adds `r1`; output JSON has exactly two
  keys under `mcpServers`.

#### T3.2 — Cursor merge semantics (noop / update / add)

MUST:
- Identical existing `r1` entry (deep-equal on parsed
  `map[string]any`) → `ResultNoop`, no file change, no backup.
- Different existing `r1` entry → `ResultUpdated`, backup written.
- New entry → `ResultAdded`, backup written if file existed.
- Backup always written to `<p>.r1-backup` *before* write
  (overwrites prior backup).

VERIFY:
- Identical → `ResultNoop`, mtime unchanged within 100ms, no
  backup file.
- Different → `ResultUpdated`, original copied to `.r1-backup`
  byte-identical to pre-write content.
- Unreadable (chmod 000) → error
  `read cursor config %q: %w`; CLI exit 2.

### T4 — Windsurf installer

File: `internal/ideinstall/windsurf.go` (NEW, ~70 LOC).

#### T4.1 — Windsurf merge-and-write (global-only)

MUST:
- `func InstallWindsurf() (Result, error)` (no scope param —
  Windsurf is global-only).
- Same `mcpServers` schema and R1 stanza as Cursor.
- Target path per T2.2.
- If `~/.codeium/windsurf/` doesn't exist, create it. If
  `~/.codeium/` also doesn't exist, print to stderr:
  `warning: ~/.codeium/ did not exist; Windsurf may not be installed`,
  then proceed with creation.
- Backup + merge + atomic write via shared `Merge` (T11.2).

VERIFY:
- `windsurf_test.go` mirrors Cursor cases 1–4 with Windsurf paths.
- Workspace-scope at CLI layer → dispatcher (T1.2) rejects with
  exit 1 before reaching `InstallWindsurf`.

### T5 — VS Code installer

File: `internal/ideinstall/vscode.go` (NEW, ~90 LOC).

#### T5.1 — VS Code `InstallVSCode` API + `servers` root key

MUST:
- `func InstallVSCode(scope Scope, workspaceDir string) (Result, error)`.
- **Distinct from Cursor/Windsurf:** root key is `servers`, not
  `mcpServers`. Merge primitive (T11.2) takes the root key as a
  parameter.
- R1 stanza:
  ```json
  {"r1": {"command": "r1", "args": ["mcp", "serve"], "env": {}, "type": "stdio"}}
  ```
- Target paths per T2.2.

VERIFY:
- `vscode_test.go` cases mirror Cursor cases 1, 2, 3, 4 but assert
  root key `servers`. A config using `mcpServers` is treated as if
  `servers` is empty (installer adds `servers`; does NOT migrate).

#### T5.2 — VS Code post-install confirmation + Copilot Agent reminder

MUST:
- Print to stdout:
  ```
  installed: r1 → <path>
  note: VS Code MCP tools are only invoked in Copilot Agent mode;
        switch the Copilot panel to "Agent" to use R1.
  ```
- The reminder is mandatory — VS Code's MCP subsystem is silent
  in Copilot Chat mode.

VERIFY:
- `vscode_test.go` captures stdout, asserts both the `installed:`
  line and the Copilot-Agent reminder are present.

### T6 — JetBrains installer

Two files: `internal/ideinstall/jetbrains.go` (~100 LOC) and the
plugin sources under `ide/jetbrains/`.

#### T6.1 — JetBrains plugin descriptor + Gradle build

Files: `build.gradle.kts` (Gradle build using
`org.jetbrains.intellij` v1.17.x targeting IntelliJ Platform 2026.1),
`settings.gradle.kts` (`rootProject.name = "r1-mcp-bridge"`),
`META-INF/plugin.xml` descriptor with `id`
`com.relayone.r1.mcp-bridge`, name `R1 MCP Bridge`, vendor
`RelayOne`, `since-build="261.0"`, depends on
`com.intellij.modules.platform`.

MUST:
- Gradle tasks: `runIde`, `buildPlugin`, `signPlugin`,
  `verifyPlugin`.
- `signPlugin` reads `R1_JETBRAINS_PRIVATE_KEY` /
  `R1_JETBRAINS_CERT_CHAIN` env vars (T11.1).

VERIFY:
- `./gradlew check buildPlugin` produces
  `ide/jetbrains/build/distributions/r1-mcp-bridge-<ver>.zip`.
- `./gradlew verifyPlugin` reports zero errors against declared
  `since-build`.

#### T6.2 — JetBrains plugin runtime (process + tool window)

Files: `R1McpBridgePlugin.kt` (plugin entry),
`R1McpBridgeProcess.kt` (spawns `r1 mcp serve` via
`ProcessBuilder`; reads/writes JSON-RPC frames; bridges to AI
Assistant MCP client API), `R1McpBridgeToolWindow.kt` (status
panel: running/stopped, last error, restart button).

MUST:
- Plugin assumes `r1` on PATH. On startup, run `r1 --version`;
  failure → notification "R1 not found on PATH; install R1 from
  https://github.com/RelayOne/r1" and tool window shows
  `not-installed`.
- Child `r1 mcp serve` started lazily on first MCP invocation;
  SIGTERM on IDE shutdown.

VERIFY:
- Manual gate: plugin loads in IntelliJ IDEA 2026.1+ via
  `./gradlew runIde`; invokes `r1.session.start` end-to-end.
- JVM tests: `parseFrame` / `serializeFrame` covered by
  `FramingTest.kt` with 4 cases (valid, truncated, oversize,
  malformed).

#### T6.3 — `r1 ide install jetbrains` API + version detection

File: `internal/ideinstall/jetbrains.go`.

MUST:
- `func InstallJetBrains(ide string, version string) (Result, error)`.
- `ide` in `{IntelliJIdea, GoLand, PyCharm, WebStorm}`; invalid →
  exit 1 with all 4 valid names enumerated.
- Empty `version` → list `~/.config/JetBrains/` (Linux) /
  `~/Library/Application Support/JetBrains/` (macOS) /
  `%APPDATA%\JetBrains\` (Windows); pick highest semver-comparable
  version matching `ide` (e.g. `IntelliJIdea2026.2` over `.1`).
- Target plugins dir:
  - Linux: `~/.local/share/JetBrains/<IDE><Version>/plugins/`
  - macOS: `~/Library/Application Support/JetBrains/<IDE><Version>/plugins/`
  - Windows: `%APPDATA%\JetBrains\<IDE><Version>\plugins\`

VERIFY:
- Invalid `ide` name → exit 1 with 4 valid names enumerated.
- Auto-detect picks highest version when multiple present.

#### T6.4 — JetBrains jar source + atomic copy

MUST:
- Source jar: `<r1-install-prefix>/share/r1/plugins/r1-mcp-bridge.jar`.
  If absent, fall back to GitHub release download:
  `https://github.com/RelayOne/r1/releases/download/v<ver>/r1-mcp-bridge-<ver>.jar`.
  Download failure → exit 4 with URL in error.
- Copy atomic: `<dest>.tmp` → `os.Rename`.
- Print confirmation:
  ```
  installed: r1-mcp-bridge.jar → <plugins-dir>
  restart <IDE> to load the plugin.
  ```

VERIFY:
- Atomic copy — fault-injection on `os.Rename` leaves no partial
  jar in the plugins dir.
- Missing source AND no network → exit 4 with expected URL.

### T7 — Uninstall

File: `internal/ideinstall/config.go` (uninstall helper) +
per-IDE files (dispatch).

#### T7.1 — Uninstall for JSON-config IDEs (Cursor / Windsurf / VS Code)

MUST:
- `func Uninstall(ide string) (Result, error)` dispatches via the
  per-IDE detector to resolve the file path.
- If `<file>.r1-backup` exists, atomic-restore
  (`os.Rename(backup, file)`); `Result.Action = ResultRestored`.
- Otherwise parse the file, remove the `r1` key, write back. If
  the map becomes empty, leave the empty shell — don't delete.
- Never-installed (no backup AND no `r1` key) → exit 1 with
  `error: r1 is not installed in <ide>`.

VERIFY:
- Round-trip test in `config_test.go`: install → record bytes →
  uninstall → JSON-canonical compare to original.
- Never-installed path returns exit 1 with expected error string.

#### T7.2 — Uninstall for JetBrains

MUST:
- Delete `<plugins-dir>/r1-mcp-bridge.jar` if present;
  `Result.Action = ResultRemoved`.
- If jar absent → exit 1 with
  `error: r1 is not installed in jetbrains (<plugins-dir>)`.
- Do NOT touch other files in the plugins dir.

VERIFY:
- Jar present → removed, returns `ResultRemoved`, other jars
  untouched.
- Jar absent → exit 1 with expected error.

### T8 — Verify

File: `internal/ideinstall/config.go` (verify helper) +
`cmd/r1/ide_install_cmd.go` (CLI surface).

#### T8.1 — `Verify` API + per-IDE detection

MUST:
- `func Verify() ([]VerifyResult, error)` returns one entry per
  IDE with fields `IDE`, `Found`, `Registered`, `Path`, `Error`.
- `Registered` for JSON-config IDEs: parse file, look for `r1`
  key under the appropriate root. For JetBrains: probe for
  `<plugins-dir>/r1-mcp-bridge.jar`.
- `Found == false` → IDE config dir absent (or for JetBrains, no
  installed version detected).

VERIFY:
- `verify_test.go` exercises a fake HOME with mixed IDE states
  (Cursor registered, Windsurf missing, VS Code unregistered,
  JetBrains registered) and asserts each `VerifyResult` field.

#### T8.2 — `r1 ide verify` CLI output format

MUST:
- One line per IDE in stable order (cursor, windsurf, vscode,
  jetbrains):
  ```
  cursor    | registered   | /home/u/.cursor/mcp.json
  windsurf  | not-found    | /home/u/.codeium/windsurf/mcp_config.json
  vscode    | unregistered | /home/u/.config/Code/User/mcp.json
  jetbrains | registered   | /home/u/.local/share/JetBrains/IntelliJIdea2026.2/plugins/
  ```
- Exit always 0 — verify is a report, not a gate.
- Three status words exactly: `registered`, `unregistered`,
  `not-found`. Columns padded so pipes align.

VERIFY:
- Test captures stdout, asserts byte-for-byte match against the
  expected table (fixed HOME, fixed column widths).

### T9 — First-run prompt

File: `cmd/r1/ide_install_cmd.go` (helper exported via package main)
+ wire site in `cmd/r1/chat_cmd.go` (or whichever file owns the
`r1 chat` entry — locate via grep `func.*chatCmd` at build time).

#### T9.1 — `MaybePromptIDEInstall` helper + ack file

MUST:
- `func MaybePromptIDEInstall(stdout, stderr, stdin)` in
  `cmd/r1/ide_install_cmd.go`:
  1. If `~/.r1/ide-prompt-acked` exists, return immediately.
  2. Run `ideinstall.Verify()`. If no IDE is `Found && !Registered`,
     write ack `none|none|none` and return.
  3. Print `R1 detected <IDE> — install R1 MCP server in <IDE>? [y/N]`.
  4. Read line from stdin. On `y|Y|yes`, invoke installer (default
     scope per IDE). Else no-op.
  5. Write ack: `<RFC3339-timestamp>|<ide>|<accepted|declined>`.

VERIFY:
- Ack exists → helper returns immediately (no stdin read, no
  installer call).
- Cursor unregistered, stdin `y\n` → cursor installer called once
  with default scope; ack contains `<timestamp>|cursor|accepted`.
- Stdin `n\n` → installer NOT called; ack contains
  `<timestamp>|cursor|declined`.

#### T9.2 — Non-TTY skip + chat-cmd wire site

MUST:
- Non-TTY or `--non-interactive` → skip the prompt entirely (no
  install, no ack write).
- TTY detection via `term.IsTerminal(int(os.Stdin.Fd()))` from
  `golang.org/x/term` (transitive dep).
- Call site: start of `r1 chat` interactive mode, after
  conversation manager init, before the first prompt loop. Locate
  via grep on existing chat entry function in `cmd/r1/`.

VERIFY:
- Non-TTY stdin → helper returns immediately, no ack file
  written.
- `r1 chat --non-interactive` does not print the IDE-install
  prompt even with an unregistered IDE detected.

### T10 — Cross-platform tests

#### T10.1 — Build-tagged platform tests (Linux / macOS / Windows)

MUST:
- Each per-IDE test ships build-tagged companions:
  `<file>_{linux,darwin,windows}_test.go` with matching
  `//go:build` directives.
- Each platform test sets `HOME` (or Windows `USERPROFILE` +
  `APPDATA`) to `t.TempDir()` via `t.Setenv`, then asserts the
  resolved config path matches the per-platform spec from §Research.
- VS Code on Windows: assert both `APPDATA` and `USERPROFILE`
  honored (APPDATA precedence).

VERIFY:
- CI: `GOOS=linux go test ./internal/ideinstall/...`;
  `GOOS=darwin go build ./internal/ideinstall/...`;
  `GOOS=windows go build ./internal/ideinstall/...`. Per-OS test
  execution requires per-OS runners (documented in
  `docs/integrations/ide-bundles.md`).

#### T10.2 — Round-trip byte-identity test

MUST:
- `TestInstallUninstallRoundTrip` in `config_test.go`:
  1. Seed a config file (e.g. `{"mcpServers":{"github":{"command":"github-mcp"}}}`).
  2. Run `InstallCursor`, then `Uninstall("cursor")`.
  3. JSON-canonical compare (parse both sides, re-marshal with
     `json.Marshal`, compare bytes).
- Repeat for Windsurf and VS Code.

### T11 — Plugin signing + shared merge primitive

#### T11.1 — JetBrains signing setup doc

File: `docs/integrations/jetbrains-plugin-signing.md` (NEW).

MUST:
- Document key generation (`openssl genrsa 4096` + `openssl req
  -x509` with subj `/CN=RelayOne R1 Plugin/O=RelayOne/C=US`,
  3650-day validity).
- Document storage: operator password manager + CI secrets
  `R1_JETBRAINS_PRIVATE_KEY` / `R1_JETBRAINS_CERT_CHAIN`.
- Document the `gradle signPlugin` task in
  `ide/jetbrains/build.gradle.kts` reading those env vars,
  producing `r1-mcp-bridge-<ver>-signed.jar`.
- Out-of-scope: Marketplace publish flow. Plugin distributed via
  R1 release tarball.

VERIFY:
- Doc reviewed manually. No code test.

#### T11.2 — Shared `config.go` JSON merge primitive

File: `internal/ideinstall/config.go` (NEW, ~120 LOC).

MUST:
- `MergeOpts{Path, RootKey, Entry, EntryKey}` +
  `Result{Path, Action}` with Action in
  `added|updated|noop|restored|removed`.
- `func Merge(opts MergeOpts) (Result, error)`: read (empty if
  absent) → parse to `map[string]any` (create root key if absent)
  → `reflect.DeepEqual` on entry (`ResultNoop` if identical) →
  backup to `<path>.r1-backup` (skip if original absent, abort
  with clear error if backup write fails) → atomic write
  (`json.MarshalIndent(v, "", "  ")` → `<path>.tmp` → `os.Rename`).

VERIFY:
- `config_test.go`: empty file → adds root + entry; non-JSON →
  parse error wrapped with path; identical entry → ResultNoop, no
  backup; rename-failure (read-only target dir) → backup
  preserved.

### T12 — Docs

File: `docs/integrations/ide-bundles.md` (NEW).

#### T12.1 — Per-IDE quickstart + troubleshooting

MUST:
- Quickstart for each of the 4 IDEs: install command, expected
  confirmation output, where the IDE surfaces R1 (Cursor: MCP tab;
  Windsurf: Cascade panel; VS Code: Copilot Agent mode; JetBrains:
  AI Assistant settings).
- Troubleshooting:
  - "config file not found" → `r1 ide verify` to see detected
    paths.
  - "IDE not picking up R1" → restart the IDE.
  - "JetBrains plugin not loading" → check `Help → About →
    Installed Plugins`; check `idea.log` for
    `ClassNotFoundException` (JDK mismatch).
  - "r1 not found on PATH" (JetBrains plugin) → install R1,
    restart IDE.
- Uninstall instructions for each IDE.

#### T12.2 — Update `docs/HOW-IT-WORKS.md` + `docs/FEATURE-MAP.md`

MUST:
- New "IDE Integrations" section in `docs/HOW-IT-WORKS.md`
  pointing at `docs/integrations/ide-bundles.md`.
- Add to "Done" in `docs/FEATURE-MAP.md`:
  `MCP IDE bundles — r1 ide install {cursor|windsurf|vscode|jetbrains}.
  See specs/mcp-ide-bundles.md.`
- Doc commit is separate from code commit per `CLAUDE.md`.

VERIFY:
- Manual: reviewer copies the quickstart command into a fresh
  shell and the IDE picks up R1.

#### T12.3 — `r1 --help` and `r1 ide --help`

MUST:
- `r1 --help` summary lists `ide` as a top-level subcommand
  alongside the existing `mcp` group.
- `r1 ide --help` / `-h` prints usage covering:
  - `r1 ide install <cursor|windsurf|vscode|jetbrains>`
  - `r1 ide uninstall <cursor|windsurf|vscode|jetbrains>`
  - `r1 ide verify`
  - Flags: `--workspace <path>` (Cursor, VS Code),
    `--global` (default for Windsurf, JetBrains),
    `--ide <name>` and `--version <ver>` (JetBrains only).

VERIFY:
- Snapshot test in `cmd/r1/ide_install_cmd_test.go` asserts the
  help output matches the goldenfile at
  `cmd/r1/testdata/ide_help.txt`.

## Boundaries (DO NOT)

- **No config writes without a backup.** Every install path writes
  `<file>.r1-backup` before the merge.
- **No global install if `--workspace .` is passed** (and vice
  versa). Mutually-exclusive flags enforced at CLI layer (T1.2).
- **No R1 binary bundled inside the JetBrains plugin.** Plugin
  assumes `r1` on PATH. Bundling cross-platform binaries in a
  `.jar` is fragile (platform mismatch, signing, AV false-positives).
- **No IDEs not listed without operator approval.** Zed, Helix,
  Neovim, Emacs are deferred.
- **No auto-install on `r1 chat` startup without the prompt.** T9
  is opt-in.
- **No live-reload IPC into the IDE.** None of the 4 IDEs document
  a stable hot-reload IPC for MCP config; restart-IDE is the
  contract.

## Acceptance criteria (measurable)

- [ ] `r1 ide install cursor` writes `~/.cursor/mcp.json` (or
      `./.cursor/mcp.json` with `--workspace .`) with the R1
      stanza; Cursor's MCP UI lists `r1`; `tools/list` returns the
      38-tool catalog.
- [ ] `r1 ide install windsurf` writes
      `~/.codeium/windsurf/mcp_config.json`; Windsurf Cascade lists
      `r1` and round-trips one tool call.
- [ ] `r1 ide install vscode --workspace .` writes
      `./.vscode/mcp.json` with root key `servers` and the R1
      stanza including `"type": "stdio"`; VS Code Copilot Agent
      mode lists `r1`.
- [ ] `r1 ide install jetbrains` copies the bundled jar to the
      correct plugins dir; IntelliJ IDEA 2026.1+ loads the plugin
      after restart and the "R1" tool window appears.
- [ ] All 4 detectors return the correct config path on Linux,
      macOS, Windows (build-tagged tests).
- [ ] Install + uninstall round-trip leaves the original config
      JSON-canonical byte-identical (Cursor, Windsurf, VS Code).
- [ ] JetBrains plugin loads and invokes 1 R1 MCP tool end-to-end
      (manual gate per T6.2).
- [ ] `r1 ide verify` reports registered/unregistered for every
      IDE, exits 0, output matches the table in T8.2.
- [ ] First-run prompt never prompts twice; respects non-TTY
      stdin; writes ack file.
- [ ] `go build ./... && go test ./... && go vet ./...` green.

## Verification gate

Standard CI gate per `CLAUDE.md` (`go build ./cmd/r1 && go test
./... && go vet ./...`) plus the JetBrains plugin gate
(`cd ide/jetbrains && ./gradlew check buildPlugin signPlugin`).
The signed jar is a release artifact; CI publishes it alongside
the `r1` binary.

## Out of scope (deferred)

- Zed / Helix / Neovim / Emacs MCP integration.
- JetBrains Marketplace publish flow.
- Auto-update of installed config when `mcp serve` flags change.
- Multi-instance support for JetBrains (acceptable resource cost
  for now — each IDE spawns its own `r1 mcp serve` child).
- Live hot-reload of MCP config (no IDE documents a stable IPC).

## Open questions for review

1. **Source-of-truth for the bundled JetBrains jar.** This spec
   assumes both: ship at `share/r1/plugins/r1-mcp-bridge.jar` (fast
   path) AND download from GitHub release as fallback. Confirm.

2. **VS Code Insiders.** Uses `Code - Insiders` as config dir name.
   Add an `--insiders` flag, or defer? Defaulting to deferred.

3. **First-run prompt scope.** When `r1 chat` runs outside a git
   repo, should the prompt offer workspace-scope or global-scope?
   Recommend global-scope default; operator opts in to workspace
   via explicit `r1 ide install cursor --workspace .` later.

4. **JetBrains AI Assistant license.** Plugin assumes operator has
   AI Assistant enabled (the MCP client lives there). Should the
   plugin emit a notification on first run if AI Assistant is
   disabled? Recommend yes; one-shot notification.

## Sources

- [MCP Servers in Cursor — TrueFoundry, 2026](https://www.truefoundry.com/blog/mcp-servers-in-cursor-setup-configuration-and-security-guide)
- [Cascade MCP Integration — Windsurf docs](https://docs.windsurf.com/windsurf/cascade/mcp)
- [MCP configuration reference — VS Code](https://code.visualstudio.com/docs/copilot/reference/mcp-configuration)
- [Add and manage MCP servers in VS Code](https://code.visualstudio.com/docs/copilot/customization/mcp-servers)
- [MCP Server — IntelliJ IDEA Documentation](https://www.jetbrains.com/help/idea/mcp-server.html)
- [JetBrains mcp-server-plugin (GitHub)](https://github.com/JetBrains/mcp-server-plugin)
- [Model Context Protocol — JetBrains AI Assistant](https://www.jetbrains.com/help/ai-assistant/mcp.html)
