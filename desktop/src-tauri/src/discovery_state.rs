// SPDX-License-Identifier: MIT
//
// Tracked: connect_url() and backoff_policy() are accessor helpers for
// the discovered-daemon path; pending wire-up into a Tauri command
// surface alongside discovery.rs. Remove this attribute when those
// accessors are exposed via #[tauri::command].
#![allow(dead_code)] // tracked: pending wire-up (matches discovery.rs/transport.rs)
//
// DiscoveryState — Tauri-managed wrapper around the discovered
// DaemonHandle, tracking issue #145.
//
// Spec desktop-cortex-augmentation §5 calls for `tauri::Builder::setup`
// to call `discovery::discover_or_spawn(app)` at startup, attach the
// resulting handle as managed state, and surface a `app.daemon_status`
// IPC command so the WebView's connection banner can render the right
// state without sniffing internal fields.
//
// This module is the orchestration glue. It exists separately from
// discovery.rs so the discovery functions stay pure (testable without a
// Tauri runtime) and main.rs stays a thin wiring layer.
//
// Lifecycle:
//   - `DiscoveryState::new()` creates an empty state. Tauri owns it
//     via `.manage()` in main.rs.
//   - The setup() closure spawns an async task that calls
//     `discovery::discover_or_spawn(&app_handle)` and on success
//     publishes the handle into `DiscoveryState`. On failure we leave
//     the state empty and log the error; the per-session SubprocessManager
//     path continues to work as the fallback.
//   - The `app.daemon_status` IPC verb reads `DiscoveryState` and
//     returns a JSON-friendly snapshot.

use std::sync::Mutex;

use crate::discovery::{DaemonHandle, TransportMode};
use crate::transport::{build_connect_url, BackoffPolicy};
use serde::Serialize;

/// Tauri-managed state holding the latest discovery result.
///
/// The Mutex is contended only at startup (single write) and on each
/// `daemon_status` query (read). The handle field is a regular `Option`
/// because the lazy "discovery still running" case is also `None`; the
/// IPC verb returns a `pending` flag to disambiguate.
pub struct DiscoveryState {
    inner: Mutex<DiscoveryInner>,
}

struct DiscoveryInner {
    /// Set once the discover_or_spawn task completes successfully.
    handle: Option<DaemonHandle>,
    /// True from the moment setup() spawns the discovery task until
    /// it finishes (success or failure). Lets the UI distinguish
    /// "still probing" from "probed and failed".
    pending: bool,
    /// On failure we surface the error string to the UI without
    /// keeping the typed `DiscoveryError` (Send + 'static gymnastics).
    error: Option<String>,
}

impl DiscoveryState {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(DiscoveryInner {
                handle: None,
                pending: false,
                error: None,
            }),
        }
    }

    /// Marks the state as actively probing. Called from setup() before
    /// the async task launches.
    pub fn mark_pending(&self) {
        let mut g = self.inner.lock().expect("DiscoveryState poisoned");
        g.pending = true;
        g.error = None;
    }

    /// Publish a successful discovery result. The pending flag flips
    /// off so the UI banner switches from "Connecting..." to "Up".
    pub fn set_handle(&self, handle: DaemonHandle) {
        let mut g = self.inner.lock().expect("DiscoveryState poisoned");
        g.handle = Some(handle);
        g.pending = false;
        g.error = None;
    }

    /// Publish a failure. The handle stays empty.
    pub fn set_error(&self, err: String) {
        let mut g = self.inner.lock().expect("DiscoveryState poisoned");
        g.error = Some(err);
        g.pending = false;
    }

    /// Read a snapshot for the IPC verb. The DaemonHandle isn't
    /// `Clone`-able (it owns a CommandChild) so we project it into a
    /// JSON-friendly summary instead.
    pub fn snapshot(&self) -> DaemonStatusSnapshot {
        let g = self.inner.lock().expect("DiscoveryState poisoned");
        let mode = g.handle.as_ref().map(|h| match h.mode {
            TransportMode::External => "external",
            TransportMode::Sidecar => "sidecar",
        });
        DaemonStatusSnapshot {
            connected: g.handle.is_some(),
            pending: g.pending,
            url: g.handle.as_ref().map(|h| h.url.clone()),
            mode: mode.map(str::to_owned),
            error: g.error.clone(),
        }
    }

    /// Build the WS connect URL for the discovered daemon, threading
    /// the optional Last-Event-ID. Returns None when discovery hasn't
    /// completed (or failed) — caller must fall through to the per-
    /// session subprocess path. Uses the transport module's
    /// canonical URL builder so the encoding is identical to the one
    /// the runtime path emits.
    pub fn connect_url(&self, last_event_id: Option<&str>) -> Option<String> {
        let g = self.inner.lock().expect("DiscoveryState poisoned");
        g.handle
            .as_ref()
            .map(|h| build_connect_url(&h.url, &h.token, last_event_id))
    }

    /// Default reconnect backoff policy. Pulled from the transport
    /// module so the daemon-discovery path uses the same schedule the
    /// per-session forwarder uses (250 ms → 16 s cap).
    pub fn backoff_policy() -> BackoffPolicy {
        BackoffPolicy::r1_default()
    }
}

impl Default for DiscoveryState {
    fn default() -> Self {
        Self::new()
    }
}

/// JSON-friendly view of `DiscoveryState`. Returned by the
/// `app.daemon_status` IPC verb so the React banner can switch its
/// colour token (green=connected, blue=pending, red=error).
#[derive(Debug, Serialize)]
pub struct DaemonStatusSnapshot {
    /// True when a DaemonHandle is live (either external or sidecar).
    pub connected: bool,
    /// True when the discovery task is still running.
    pub pending: bool,
    /// The ws:// URL we discovered, if any.
    pub url: Option<String>,
    /// "external" | "sidecar" — null when not connected.
    pub mode: Option<String>,
    /// Error string from the most recent discovery attempt, if it
    /// failed. Cleared on the next successful attempt.
    pub error: Option<String>,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::discovery::{DaemonHandle, TransportMode};

    fn fake_handle(url: &str, token: &str, mode: TransportMode) -> DaemonHandle {
        DaemonHandle {
            url: url.to_string(),
            token: token.to_string(),
            mode,
            child: None,
        }
    }

    #[test]
    fn empty_state_is_disconnected_and_not_pending() {
        let s = DiscoveryState::new();
        let snap = s.snapshot();
        assert!(!snap.connected);
        assert!(!snap.pending);
        assert!(snap.url.is_none());
        assert!(snap.mode.is_none());
        assert!(snap.error.is_none());
    }

    #[test]
    fn mark_pending_sets_pending_flag_only() {
        let s = DiscoveryState::new();
        s.mark_pending();
        let snap = s.snapshot();
        assert!(!snap.connected);
        assert!(snap.pending);
        assert!(snap.error.is_none());
    }

    #[test]
    fn set_handle_publishes_url_mode_clears_pending() {
        let s = DiscoveryState::new();
        s.mark_pending();
        s.set_handle(fake_handle("ws://127.0.0.1:7777", "tok", TransportMode::External));
        let snap = s.snapshot();
        assert!(snap.connected);
        assert!(!snap.pending);
        assert_eq!(snap.url.as_deref(), Some("ws://127.0.0.1:7777"));
        assert_eq!(snap.mode.as_deref(), Some("external"));
    }

    #[test]
    fn set_handle_distinguishes_external_from_sidecar() {
        let s = DiscoveryState::new();
        s.set_handle(fake_handle("ws://127.0.0.1:1234", "t", TransportMode::Sidecar));
        assert_eq!(s.snapshot().mode.as_deref(), Some("sidecar"));
    }

    #[test]
    fn set_error_clears_pending_keeps_handle_unset() {
        let s = DiscoveryState::new();
        s.mark_pending();
        s.set_error("daemon refused connection".into());
        let snap = s.snapshot();
        assert!(!snap.connected);
        assert!(!snap.pending);
        assert_eq!(snap.error.as_deref(), Some("daemon refused connection"));
    }

    #[test]
    fn set_handle_after_error_clears_error() {
        let s = DiscoveryState::new();
        s.set_error("first attempt failed".into());
        s.set_handle(fake_handle("ws://127.0.0.1:7777", "tok", TransportMode::External));
        let snap = s.snapshot();
        assert!(snap.connected);
        assert!(snap.error.is_none());
    }

    #[test]
    fn connect_url_threads_last_event_id() {
        let s = DiscoveryState::new();
        s.set_handle(fake_handle("ws://127.0.0.1:9999", "tok-abc", TransportMode::External));
        let url = s.connect_url(Some("evt-42")).expect("connect_url available");
        assert!(url.contains("token=tok-abc"));
        assert!(url.contains("last_event_id=evt-42"));
    }

    #[test]
    fn connect_url_none_when_disconnected() {
        let s = DiscoveryState::new();
        assert!(s.connect_url(None).is_none());
    }

    #[test]
    fn backoff_policy_matches_transport_default() {
        let p = DiscoveryState::backoff_policy();
        assert_eq!(p, BackoffPolicy::r1_default());
    }
}
