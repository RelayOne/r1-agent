// SPDX-License-Identifier: MIT
//
// R1 Desktop lane pop-out — `app.popout_lane` command.
//
// Implements spec desktop-cortex-augmentation §6.1 + checklist
// item 23. Each invocation builds a `WebviewWindow` with:
//
//   label  : "lane:<session_id>:<lane_id>"   (Tauri-stable;
//                                              uniquely identifies
//                                              the pop-out across
//                                              focus / hide events)
//   url    : "index.html?popout=lane&session=<>&lane=<>"
//   size   : 480 × 640 (per spec §6.1)
//
// On window close the registry entry is dropped so a subsequent
// `popout_lane` for the same lane re-opens a fresh window rather
// than refusing.
//
// `PopoutRegistry` is held as `tauri::State<>`. It serves three
// purposes:
//   1. Idempotency: re-popping an already-open lane focuses the
//      existing window instead of stacking two copies.
//   2. Lifecycle bookkeeping: the menu's "Lane Pop-Outs" submenu
//      (item 25) iterates the registry to list open windows.
//   3. Cleanup: app-exit callers iterate and close every entry.

use std::collections::HashMap;
use std::sync::Arc;

use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};
use tokio::sync::Mutex;

use crate::ipc::{IpcError, IpcResult};

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

/// One entry per open pop-out window. The label IS the unique id.
#[derive(Debug, Clone)]
pub struct PopoutEntry {
    pub label: String,
    pub session_id: String,
    pub lane_id: String,
}

#[derive(Default)]
pub struct PopoutRegistry {
    inner: Arc<Mutex<HashMap<String, PopoutEntry>>>,
}

impl PopoutRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    pub async fn insert(&self, entry: PopoutEntry) {
        let mut g = self.inner.lock().await;
        g.insert(entry.label.clone(), entry);
    }

    pub async fn remove(&self, label: &str) -> bool {
        let mut g = self.inner.lock().await;
        g.remove(label).is_some()
    }

    pub async fn contains(&self, label: &str) -> bool {
        let g = self.inner.lock().await;
        g.contains_key(label)
    }

    pub async fn list(&self) -> Vec<PopoutEntry> {
        let g = self.inner.lock().await;
        g.values().cloned().collect()
    }

    pub async fn count(&self) -> usize {
        let g = self.inner.lock().await;
        g.len()
    }
}

// ---------------------------------------------------------------------------
// Label + URL builders (pure — testable without a Tauri runtime)
// ---------------------------------------------------------------------------

/// Build the stable per-lane window label. Format follows the spec:
/// `lane:<session>:<lane>`. Caller-supplied ids are validated by the
/// host before reaching here, so we don't escape; deliberately
/// leaving the colon delimiter visible so logs are greppable.
pub fn lane_window_label(session_id: &str, lane_id: &str) -> String {
    format!("lane:{session_id}:{lane_id}")
}

/// Build the relative URL the WebView mounts inside the pop-out.
/// `popout=lane` triggers the popout.tsx entry (item 24); the session
/// + lane query params are read by `<PoppedLaneApp>` to subscribe.
pub fn lane_window_url(session_id: &str, lane_id: &str) -> String {
    format!(
        "index.html?popout=lane&session={}&lane={}",
        urlencode(session_id),
        urlencode(lane_id)
    )
}

fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for &b in s.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(b as char);
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

// ---------------------------------------------------------------------------
// Window builder
// ---------------------------------------------------------------------------

/// Open (or focus) a pop-out window for one lane. Returns the window
/// label for the IPC verb to echo back to the WebView.
pub async fn open_or_focus_lane_popout(
    app: &AppHandle,
    registry: &PopoutRegistry,
    session_id: &str,
    lane_id: &str,
) -> IpcResult<String> {
    let label = lane_window_label(session_id, lane_id);

    // Idempotency: focus existing window if already open.
    if registry.contains(&label).await {
        if let Some(win) = app.get_webview_window(&label) {
            let _ = win.set_focus();
            return Ok(label);
        }
        // Stale entry — registry drifted from real Tauri state. Drop
        // it and fall through to the open path.
        registry.remove(&label).await;
    }

    let url = lane_window_url(session_id, lane_id);
    let webview_url = WebviewUrl::App(url.into());

    let title = format!("Lane {lane_id}");
    let window = WebviewWindowBuilder::new(app, &label, webview_url)
        .title(title)
        .inner_size(480.0, 640.0)
        .min_inner_size(360.0, 480.0)
        .resizable(true)
        .build()
        .map_err(|e| IpcError::internal(format!("popout build: {e}")))?;

    // Wire the close handler so registry stays in sync.
    let registry_inner = registry.inner.clone();
    let label_for_close = label.clone();
    window.on_window_event(move |event| {
        if let tauri::WindowEvent::Destroyed = event {
            let inner = registry_inner.clone();
            let label = label_for_close.clone();
            tokio::spawn(async move {
                let mut g = inner.lock().await;
                g.remove(&label);
            });
        }
    });

    registry
        .insert(PopoutEntry {
            label: label.clone(),
            session_id: session_id.to_string(),
            lane_id: lane_id.to_string(),
        })
        .await;

    Ok(label)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lane_window_label_matches_spec() {
        assert_eq!(
            lane_window_label("S01", "L02"),
            "lane:S01:L02".to_string()
        );
    }

    #[test]
    fn lane_window_url_carries_query() {
        let u = lane_window_url("S01", "L02");
        assert!(u.starts_with("index.html?popout=lane"));
        assert!(u.contains("session=S01"));
        assert!(u.contains("lane=L02"));
    }

    #[test]
    fn lane_window_url_percent_encodes_special_chars() {
        let u = lane_window_url("a/b", "c d");
        // / is reserved; space encoded as %20.
        assert!(u.contains("session=a%2Fb"), "got: {u}");
        assert!(u.contains("lane=c%20d"), "got: {u}");
    }

    #[tokio::test]
    async fn registry_insert_and_remove_round_trip() {
        let r = PopoutRegistry::new();
        let e = PopoutEntry {
            label: "lane:S:L".into(),
            session_id: "S".into(),
            lane_id: "L".into(),
        };
        r.insert(e).await;
        assert_eq!(r.count().await, 1);
        assert!(r.contains("lane:S:L").await);
        let listed = r.list().await;
        assert_eq!(listed.len(), 1);
        assert_eq!(listed[0].lane_id, "L");
        assert!(r.remove("lane:S:L").await);
        assert_eq!(r.count().await, 0);
        assert!(!r.remove("lane:S:L").await);
    }

    #[tokio::test]
    async fn registry_overwrites_on_duplicate_label() {
        let r = PopoutRegistry::new();
        let a = PopoutEntry {
            label: "lane:S:L".into(),
            session_id: "S".into(),
            lane_id: "L".into(),
        };
        r.insert(a).await;
        // Same label, different session_id (e.g. session id reused
        // after a renumber) — last-write wins.
        let b = PopoutEntry {
            label: "lane:S:L".into(),
            session_id: "Sx".into(),
            lane_id: "L".into(),
        };
        r.insert(b).await;
        assert_eq!(r.count().await, 1);
        let listed = r.list().await;
        assert_eq!(listed[0].session_id, "Sx");
    }
}
