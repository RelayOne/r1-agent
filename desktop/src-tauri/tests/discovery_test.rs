// SPDX-License-Identifier: MIT
//
// Integration tests for `discovery::*` — spec
// desktop-cortex-augmentation §11.2 + checklist item 32.
//
// Pinned via the lib target (src/lib.rs). The Tauri-runtime-bound
// surfaces (spawn_sidecar / discover_or_spawn) are tested at
// the integration boundary in a way that doesn't require a live
// AppHandle: probe_external uses real TCP loopback, and the
// listening-line parser is exercised through the public
// read_daemon_json contract.
//
// `cargo test --test discovery_test` runs these; the wider
// `cargo test` invocation rolls them up alongside the in-module
// unit tests.

use std::fs;
use std::io::Write;
use std::net::SocketAddr;
use std::time::Duration;

use r1_desktop::discovery::{
    install_command_for_host_os, probe_external, read_daemon_json, DaemonInfo,
    DiscoveryError,
};
use tokio::net::TcpListener;
use tokio::time::sleep;

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

/// Bind a TCP listener on 127.0.0.1:0 and return the OS-chosen port.
async fn bind_loopback() -> (TcpListener, u16) {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("can bind loopback");
    let port = listener
        .local_addr()
        .expect("listener has local addr")
        .port();
    (listener, port)
}

/// Write a temporary daemon.json under `home_dir`/.r1/ and return the
/// directory so the caller can drop it after.
fn write_daemon_json(home_dir: &std::path::Path, info: &DaemonInfo) {
    let r1_dir = home_dir.join(".r1");
    fs::create_dir_all(&r1_dir).expect("mkdir .r1");
    let path = r1_dir.join("daemon.json");
    let mut f = fs::File::create(&path).expect("create daemon.json");
    let bytes = serde_json::to_vec(info).expect("serialise DaemonInfo");
    f.write_all(&bytes).expect("write daemon.json");
}

fn unique_tmp_dir(label: &str) -> std::path::PathBuf {
    let stamp = format!(
        "{}-{}-{}",
        label,
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    );
    let dir = std::env::temp_dir().join(stamp);
    let _ = fs::create_dir_all(&dir);
    dir
}

// -------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------

/// `read_daemon_json` returns `NotFound` when there is no file.
#[tokio::test]
async fn read_daemon_json_missing_file() {
    let dir = unique_tmp_dir("read-missing");
    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = read_daemon_json();
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);
    assert!(matches!(res, Err(DiscoveryError::NotFound)));
}

/// `read_daemon_json` returns `BadHandshake` on malformed JSON.
#[tokio::test]
async fn read_daemon_json_malformed_returns_bad_handshake() {
    let dir = unique_tmp_dir("read-malformed");
    let r1 = dir.join(".r1");
    fs::create_dir_all(&r1).expect("mkdir .r1");
    fs::write(r1.join("daemon.json"), b"not-json").expect("write garbage");
    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = read_daemon_json();
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);
    assert!(matches!(res, Err(DiscoveryError::BadHandshake(_))));
}

/// `read_daemon_json` parses a well-formed file.
#[tokio::test]
async fn read_daemon_json_happy_path() {
    let dir = unique_tmp_dir("read-happy");
    let info = DaemonInfo {
        url: "ws://127.0.0.1:7777".into(),
        token: "tok".into(),
        written_at: None,
        version: Some("0.5.2".into()),
    };
    write_daemon_json(&dir, &info);
    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = read_daemon_json();
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);
    let parsed = res.expect("happy daemon.json parses");
    assert_eq!(parsed.url, "ws://127.0.0.1:7777");
    assert_eq!(parsed.token, "tok");
    assert_eq!(parsed.version.as_deref(), Some("0.5.2"));
}

/// `probe_external` connects when daemon.json points at a live
/// loopback listener.
#[tokio::test]
async fn probe_external_succeeds_with_live_listener() {
    let (listener, port) = bind_loopback().await;
    // Drive the listener to keep the OS port valid; we just need
    // accept() to be runnable for the connect to succeed.
    tokio::spawn(async move {
        let _ = listener.accept().await;
    });

    let dir = unique_tmp_dir("probe-live");
    let info = DaemonInfo {
        url: format!("ws://127.0.0.1:{port}"),
        token: "live".into(),
        written_at: None,
        version: None,
    };
    write_daemon_json(&dir, &info);

    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = probe_external().await;
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);

    let info = res.expect("connect succeeds");
    assert_eq!(info.token, "live");
}

/// `probe_external` returns `Refused` when the port is closed.
#[tokio::test]
async fn probe_external_refused_when_port_closed() {
    // Bind, capture the port, then drop the listener so the port
    // is now refused. The kernel keeps the address briefly in
    // TIME_WAIT but on Linux the connect call still gets ECONNREFUSED.
    let (listener, port) = bind_loopback().await;
    let local: SocketAddr = listener
        .local_addr()
        .expect("local addr known");
    drop(listener);
    // Tiny pause so the OS reflects the closed state (especially on
    // macOS where ECONNREFUSED can race the close path).
    sleep(Duration::from_millis(50)).await;

    let dir = unique_tmp_dir("probe-refused");
    let info = DaemonInfo {
        url: format!("ws://127.0.0.1:{}", local.port()),
        token: "x".into(),
        written_at: None,
        version: None,
    };
    write_daemon_json(&dir, &info);

    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = probe_external().await;
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);
    let _ = port; // referenced for read; the assertion is on res.
    assert!(matches!(res, Err(DiscoveryError::Refused)));
}

/// `probe_external` rejects a non-loopback host before attempting
/// the connect (defence-in-depth: even a leaked daemon.json should
/// never let the desktop talk to a remote daemon).
#[tokio::test]
async fn probe_external_rejects_non_loopback_host() {
    let dir = unique_tmp_dir("probe-non-loopback");
    let info = DaemonInfo {
        url: "ws://192.0.2.1:9999".into(), // TEST-NET-1, never routes
        token: "x".into(),
        written_at: None,
        version: None,
    };
    write_daemon_json(&dir, &info);

    let prev = std::env::var_os("HOME");
    std::env::set_var("HOME", &dir);
    let res = probe_external().await;
    match prev {
        Some(v) => std::env::set_var("HOME", v),
        None => std::env::remove_var("HOME"),
    }
    let _ = fs::remove_dir_all(&dir);
    assert!(matches!(res, Err(DiscoveryError::BadHandshake(_))));
}

/// install_command_for_host_os returns a non-empty platform-aware
/// string; the actual content is platform-conditional and not
/// re-asserted here (covered by the in-module unit test) — this
/// integration test pins the public surface.
#[tokio::test]
async fn install_command_is_exposed_through_lib() {
    let cmd = install_command_for_host_os();
    assert!(cmd.starts_with("r1 serve"));
    assert!(cmd.contains("--install"));
}
