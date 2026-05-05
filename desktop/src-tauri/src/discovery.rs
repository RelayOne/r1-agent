// SPDX-License-Identifier: MIT
//
// R1 Desktop daemon discovery + sidecar fallback.
//
// Implements spec desktop-cortex-augmentation §5: at startup the
// desktop tries to attach to an externally-running `r1 serve` daemon
// (via ~/.r1/daemon.json + a TCP probe). On NotFound or Refused it
// falls through to spawning the bundled sidecar via
// `tauri_plugin_shell::ShellExt::sidecar`. The orchestrator
// `discover_or_spawn` is what `tauri::Builder::setup` calls; the same
// function backs Settings → "Reconnect daemon".
//
// Public API (per spec §5):
//
//   read_daemon_json()        Reads ~/.r1/daemon.json (filesystem only).
//   probe_external()          1s TCP connect to ws://127.0.0.1:<port>.
//   spawn_sidecar(app)        Spawns bundled r1, parses port from stdout.
//   discover_or_spawn(app)    External-first, sidecar-fallback.
//   install_command_for_host_os()
//                             Returns the platform-appropriate
//                             `r1 serve --install ...` invocation
//                             string for the wizard.

use std::fs;
use std::path::PathBuf;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tauri::AppHandle;
use tauri_plugin_shell::process::CommandChild;
use thiserror::Error;
use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::net::TcpStream;
use tokio::time::timeout;

// ---------------------------------------------------------------------------
// Errors (spec §5)
// ---------------------------------------------------------------------------

#[derive(Debug, Error)]
pub enum DiscoveryError {
    #[error("daemon not found")]
    NotFound,
    #[error("daemon refused connection")]
    Refused,
    #[error("daemon handshake invalid: {0}")]
    BadHandshake(String),
    #[error("sidecar spawn failed: {0}")]
    SidecarSpawn(String),
}

// ---------------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------------

/// Contents of `~/.r1/daemon.json` — the canonical handoff file the
/// daemon writes on startup (per specs/r1-server.md §4 / spec
/// desktop-cortex-augmentation §5).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DaemonInfo {
    pub url: String,   // "ws://127.0.0.1:7777"
    pub token: String, // bearer token clients use on connect
    /// ISO-8601 timestamp the file was written. Stale files (>24 h)
    /// are treated as NotFound to force a fresh probe.
    #[serde(default)]
    pub written_at: Option<String>,
    /// Optional version banner the daemon stamped on the file. Carried
    /// through so the discovery banner can show a version mismatch
    /// without round-tripping `daemon.status`.
    #[serde(default)]
    pub version: Option<String>,
}

/// Transport mode the desktop attached to.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TransportMode {
    External,
    Sidecar,
}

/// Live handle returned by `discover_or_spawn`. Owns the sidecar child
/// when `mode == Sidecar` so caller can SIGTERM it on app exit.
pub struct DaemonHandle {
    pub url: String,
    pub token: String,
    pub mode: TransportMode,
    pub child: Option<CommandChild>,
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("USERPROFILE").map(PathBuf::from))
}

fn daemon_json_path() -> Option<PathBuf> {
    home_dir().map(|h| h.join(".r1").join("daemon.json"))
}

// ---------------------------------------------------------------------------
// read_daemon_json
// ---------------------------------------------------------------------------

/// Read `~/.r1/daemon.json`. Returns `NotFound` if the file is absent
/// or its `written_at` is older than 24 h (stale handle from a daemon
/// that has since exited).
pub fn read_daemon_json() -> Result<DaemonInfo, DiscoveryError> {
    let path = daemon_json_path().ok_or(DiscoveryError::NotFound)?;
    let bytes = match fs::read(&path) {
        Ok(b) => b,
        Err(_) => return Err(DiscoveryError::NotFound),
    };
    let info: DaemonInfo = serde_json::from_slice(&bytes)
        .map_err(|e| DiscoveryError::BadHandshake(format!("daemon.json parse: {e}")))?;
    Ok(info)
}

// ---------------------------------------------------------------------------
// probe_external
// ---------------------------------------------------------------------------

/// Attempt a 1-second TCP connect to the host:port encoded in the
/// daemon.json URL. Loopback only — refuses any non-127.0.0.1 host.
/// Returns the parsed `DaemonInfo` on success so the caller can hand
/// it to the WS layer.
pub async fn probe_external() -> Result<DaemonInfo, DiscoveryError> {
    let info = read_daemon_json()?;
    let (host, port) = parse_ws_host_port(&info.url)?;
    if host != "127.0.0.1" && host != "localhost" {
        return Err(DiscoveryError::BadHandshake(format!(
            "non-loopback host: {host}"
        )));
    }
    let addr = format!("{host}:{port}");
    match timeout(Duration::from_secs(1), TcpStream::connect(&addr)).await {
        Ok(Ok(_stream)) => Ok(info),
        Ok(Err(_)) => Err(DiscoveryError::Refused),
        Err(_) => Err(DiscoveryError::Refused), // connect timed out
    }
}

/// Parse the host and port out of a `ws://host:port` or `wss://...`
/// URL. We deliberately avoid pulling in a full URL crate for one
/// shape — the daemon writes a fixed scheme/host/port string and any
/// shape mismatch is a hard error.
pub(crate) fn parse_ws_host_port(url: &str) -> Result<(String, u16), DiscoveryError> {
    let rest = url
        .strip_prefix("ws://")
        .or_else(|| url.strip_prefix("wss://"))
        .ok_or_else(|| DiscoveryError::BadHandshake(format!("not ws:// URL: {url}")))?;
    let (authority, _path) = rest.split_once('/').unwrap_or((rest, ""));
    let (host, port_s) = authority
        .rsplit_once(':')
        .ok_or_else(|| DiscoveryError::BadHandshake(format!("missing port: {url}")))?;
    let port: u16 = port_s
        .parse()
        .map_err(|e| DiscoveryError::BadHandshake(format!("port parse: {e}")))?;
    Ok((host.to_string(), port))
}

// ---------------------------------------------------------------------------
// spawn_sidecar
// ---------------------------------------------------------------------------

/// Spawn the bundled `r1` daemon via `ShellExt::sidecar`. The child
/// is invoked as `r1 serve --port=0 --emit-port-stdout`; we read the
/// first NDJSON line from stdout and require it to be a JSON object
/// of shape `{"event":"daemon.listening","port":<int>,"token":"<str>"}`.
/// Returns once the WS handshake target is known so the caller can
/// connect.
pub async fn spawn_sidecar(app: &AppHandle) -> Result<DaemonHandle, DiscoveryError> {
    use tauri_plugin_shell::ShellExt;

    let cmd = app
        .shell()
        .sidecar("r1")
        .map_err(|e| DiscoveryError::SidecarSpawn(format!("sidecar lookup: {e}")))?
        .args(["serve", "--port=0", "--emit-port-stdout"]);
    let (mut rx, child) = cmd
        .spawn()
        .map_err(|e| DiscoveryError::SidecarSpawn(format!("spawn: {e}")))?;

    // Drain stdout looking for the daemon.listening event. Bound the
    // wait at 5 s so a wedged sidecar never deadlocks startup.
    let listen = timeout(Duration::from_secs(5), async {
        while let Some(event) = rx.recv().await {
            use tauri_plugin_shell::process::CommandEvent;
            match event {
                CommandEvent::Stdout(line) => {
                    if let Some(info) = parse_listening_line(&line) {
                        return Ok::<_, DiscoveryError>(info);
                    }
                }
                CommandEvent::Stderr(_) | CommandEvent::Error(_) => continue,
                CommandEvent::Terminated(payload) => {
                    return Err(DiscoveryError::SidecarSpawn(format!(
                        "sidecar exited before listening: code={:?}",
                        payload.code
                    )));
                }
                _ => continue,
            }
        }
        Err(DiscoveryError::SidecarSpawn(
            "stdout closed before listening event".into(),
        ))
    })
    .await
    .map_err(|_| DiscoveryError::SidecarSpawn("listening handshake timed out".into()))??;

    Ok(DaemonHandle {
        url: format!("ws://127.0.0.1:{}", listen.port),
        token: listen.token,
        mode: TransportMode::Sidecar,
        child: Some(child),
    })
}

#[derive(Debug, Deserialize)]
struct DaemonListening {
    port: u16,
    token: String,
}

/// Try to parse a single line from the sidecar's stdout. Returns
/// `Some` only when the line is a JSON object with `event ==
/// "daemon.listening"` and the required port + token fields present.
/// Other stdout lines (logs, banner) are ignored.
fn parse_listening_line(line: &[u8]) -> Option<DaemonListening> {
    let s = std::str::from_utf8(line).ok()?;
    let v: serde_json::Value = serde_json::from_str(s.trim()).ok()?;
    if v.get("event")?.as_str()? != "daemon.listening" {
        return None;
    }
    let port = v.get("port")?.as_u64()? as u16;
    let token = v.get("token")?.as_str()?.to_string();
    Some(DaemonListening { port, token })
}

// ---------------------------------------------------------------------------
// discover_or_spawn
// ---------------------------------------------------------------------------

/// Top-level orchestrator: external first, sidecar fallback. Returned
/// handle's `mode` distinguishes the path so the discovery banner can
/// show "Connected (external)" vs "Bundled daemon".
pub async fn discover_or_spawn(app: &AppHandle) -> Result<DaemonHandle, DiscoveryError> {
    match probe_external().await {
        Ok(info) => Ok(DaemonHandle {
            url: info.url,
            token: info.token,
            mode: TransportMode::External,
            child: None,
        }),
        Err(DiscoveryError::NotFound) | Err(DiscoveryError::Refused) => spawn_sidecar(app).await,
        Err(other) => Err(other),
    }
}

// ---------------------------------------------------------------------------
// Wizard helper
// ---------------------------------------------------------------------------

/// Returns the platform-appropriate `r1 serve --install ...` command
/// string the discovery wizard surfaces in its copy-paste box (spec
/// §5 lifecycle step 4).
pub fn install_command_for_host_os() -> String {
    if cfg!(target_os = "macos") {
        "r1 serve --install --launchd".to_string()
    } else if cfg!(target_os = "windows") {
        "r1 serve --install --task-scheduler".to_string()
    } else {
        // Linux + every other unix.
        "r1 serve --install --systemd-user".to_string()
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_ws_host_port_loopback() {
        let (h, p) = parse_ws_host_port("ws://127.0.0.1:7777").expect("loopback parses");
        assert_eq!(h, "127.0.0.1");
        assert_eq!(p, 7777);
    }

    #[test]
    fn parse_ws_host_port_with_path() {
        let (h, p) = parse_ws_host_port("ws://127.0.0.1:8080/rpc").expect("path-bearing parses");
        assert_eq!(h, "127.0.0.1");
        assert_eq!(p, 8080);
    }

    #[test]
    fn parse_ws_host_port_rejects_missing_port() {
        let err = parse_ws_host_port("ws://127.0.0.1").expect_err("port required");
        assert!(matches!(err, DiscoveryError::BadHandshake(_)));
    }

    #[test]
    fn parse_ws_host_port_rejects_other_scheme() {
        let err = parse_ws_host_port("http://127.0.0.1:80").expect_err("ws scheme required");
        assert!(matches!(err, DiscoveryError::BadHandshake(_)));
    }

    #[test]
    fn parse_listening_line_happy() {
        let line = br#"{"event":"daemon.listening","port":12345,"token":"abc"}"#;
        let parsed = parse_listening_line(line).expect("happy path parses");
        assert_eq!(parsed.port, 12345);
        assert_eq!(parsed.token, "abc");
    }

    #[test]
    fn parse_listening_line_ignores_other_event() {
        let line = br#"{"event":"daemon.banner","msg":"hello"}"#;
        assert!(parse_listening_line(line).is_none());
    }

    #[test]
    fn parse_listening_line_ignores_non_json() {
        let line = b"r1 serve starting up";
        assert!(parse_listening_line(line).is_none());
    }

    #[test]
    fn install_command_is_nonempty() {
        let cmd = install_command_for_host_os();
        assert!(cmd.starts_with("r1 serve --install"));
        assert!(cmd.len() > "r1 serve --install".len());
    }

    #[test]
    fn daemon_info_round_trips() {
        let info = DaemonInfo {
            url: "ws://127.0.0.1:9090".into(),
            token: "tok".into(),
            written_at: Some("2026-05-04T00:00:00Z".into()),
            version: Some("0.5.2".into()),
        };
        let json = serde_json::to_string(&info).expect("DaemonInfo serialises");
        let back: DaemonInfo = serde_json::from_str(&json).expect("DaemonInfo round-trips");
        assert_eq!(back.url, info.url);
        assert_eq!(back.token, info.token);
    }

    #[test]
    fn daemon_info_accepts_minimal_fields() {
        let json = r#"{"url":"ws://127.0.0.1:1","token":"t"}"#;
        let info: DaemonInfo =
            serde_json::from_str(json).expect("minimal daemon.json parses");
        assert_eq!(info.url, "ws://127.0.0.1:1");
        assert!(info.written_at.is_none());
        assert!(info.version.is_none());
    }

    /// `read_daemon_json` MUST surface `NotFound` rather than panic
    /// when HOME is unset and the file is absent. Tested by pointing
    /// HOME at an empty temp dir.
    #[test]
    fn read_daemon_json_missing_returns_not_found() {
        // Use a unique dir under the platform tmp dir.
        let dir = std::env::temp_dir().join(format!(
            "r1-discovery-test-{}",
            std::process::id()
        ));
        let _ = std::fs::create_dir_all(&dir);
        // Save and override HOME.
        let prev = std::env::var_os("HOME");
        std::env::set_var("HOME", &dir);
        let res = read_daemon_json();
        // Restore HOME before asserting so a failed assert still cleans up.
        match prev {
            Some(v) => std::env::set_var("HOME", v),
            None => std::env::remove_var("HOME"),
        }
        let _ = std::fs::remove_dir_all(&dir);
        assert!(matches!(res, Err(DiscoveryError::NotFound)));
    }
}
