// SPDX-License-Identifier: MIT
//
// R1 Desktop WS transport — wraps tauri-plugin-websocket.
//
// NOTE: the run-loop wiring to a real socket is still pending the
// multi-session daemon revision (current shipping design uses a
// per-session subprocess). The policy layer (BackoffPolicy,
// LastEventId, lifecycle event taxonomy, reconnect_status Channel
// driver) is wired through the `transport_reconnect_status` Tauri
// verb so the title-bar pill renders the correct state once the
// run-loop fires events into it.
//
// Implements spec desktop-cortex-augmentation §6 + the reconnect /
// Last-Event-ID handshake contract from docs/decisions/index.md D-S6
// (RT-R1D-DAEMON). Responsibilities:
//
//   * Connect to a `DaemonHandle` (url + token from discovery.rs)
//   * Auto-reconnect with exponential backoff: 250 ms, 500 ms, 1 s,
//     2 s, 4 s, 8 s, 16 s cap, with ±20% jitter to break thundering
//     herd between multiple desktop instances.
//   * Track `last_event_id` from the server's events; on reconnect
//     send it as the `Last-Event-ID` query string so the daemon can
//     replay the gap.
//   * Emit a typed lifecycle stream (`Connecting`, `Up`, `Down`,
//     `Reconnecting`, `Replaying`) so the DaemonStatus banner can
//     render the right colour without sniffing internal state.
//
// This module is the upper half of the lane subscription pipeline;
// the lower half (per-session forwarder + Channel<LaneEvent>) lives
// in lanes.rs (item 17). Frames received here are parsed once and
// fanned out to whichever subscriber is responsible.
//
// The actual WebSocket I/O is delegated to `tokio_tungstenite` via
// `tauri-plugin-websocket`'s embedded client; we own the policy layer
// (backoff, replay handshake, lifecycle events) and stay agnostic of
// the underlying socket.

use std::sync::atomic::{AtomicBool, Ordering as AtomicOrdering};
use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio::sync::{mpsc, Mutex};

// ---------------------------------------------------------------------------
// Backoff schedule (spec §16: 250 ms → 16 s cap)
// ---------------------------------------------------------------------------

/// Bounds and progression for the reconnect backoff. The schedule is
/// a doubling sequence capped at `max`. Jitter is applied per-attempt
/// at the call site (we keep the schedule itself deterministic so
/// tests can assert exact step values).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BackoffPolicy {
    pub initial: Duration,
    pub max: Duration,
    pub factor: u32,
}

impl BackoffPolicy {
    pub const fn r1_default() -> Self {
        Self {
            initial: Duration::from_millis(250),
            max: Duration::from_secs(16),
            factor: 2,
        }
    }

    /// Compute the delay for the n-th reconnect attempt (n == 0 is
    /// the first retry after the initial connect failure). Saturates
    /// at `self.max`.
    pub fn delay_for_attempt(&self, n: u32) -> Duration {
        // Use saturating arithmetic — pow(32) overflows fast otherwise.
        let factor = self.factor.max(1);
        let mut d = self.initial;
        for _ in 0..n {
            let doubled = d.saturating_mul(factor);
            if doubled >= self.max {
                return self.max;
            }
            d = doubled;
        }
        d
    }
}

// ---------------------------------------------------------------------------
// Lifecycle events (spec §5 banner state)
// ---------------------------------------------------------------------------

/// What the transport is currently doing. The DaemonStatus banner
/// maps these one-to-one onto its colour palette (green / blue /
/// yellow / red).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum LifecycleEvent {
    /// First connect attempt before any frame received.
    Connecting { url: String },
    /// Handshake complete. `replayed_from` carries the last_event_id
    /// the server replayed from on this reconnect (None on first connect).
    Up {
        url: String,
        replayed_from: Option<String>,
    },
    /// Socket closed. `will_retry` is false only when the caller
    /// explicitly requested shutdown.
    Down {
        url: String,
        reason: String,
        will_retry: bool,
    },
    /// Between attempts. `attempt` is 0-based, `next_in_ms` is the
    /// jittered delay until the next try.
    Reconnecting {
        url: String,
        attempt: u32,
        next_in_ms: u64,
    },
}

// ---------------------------------------------------------------------------
// Wire frames
// ---------------------------------------------------------------------------

/// One inbound message off the WS. Either a JSON-RPC response (id
/// keyed) or a server-pushed event (event keyed). `last_event_id`
/// is captured from a sidecar header in the daemon's event envelope
/// when present so the transport can replay on reconnect.
#[derive(Debug, Clone, Deserialize)]
#[allow(dead_code)] // audit/scan-rust-stubs.md #10: fields read by future frame router
pub struct InboundFrame {
    #[serde(default)]
    pub id: Option<String>,
    #[serde(default)]
    pub event: Option<String>,
    #[serde(default)]
    pub last_event_id: Option<String>,
    #[serde(flatten)]
    pub rest: serde_json::Map<String, serde_json::Value>,
}

// ---------------------------------------------------------------------------
// Last-Event-ID tracker
// ---------------------------------------------------------------------------

/// Keeps the most recent server-supplied event id so reconnects can
/// hand it back to the daemon. `try_update` is monotonic — out-of-
/// order frames don't rewind the cursor.
#[derive(Debug, Default)]
pub struct LastEventId {
    inner: Mutex<Option<String>>,
}

impl LastEventId {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(None),
        }
    }

    pub async fn get(&self) -> Option<String> {
        self.inner.lock().await.clone()
    }

    /// Update only if the new id is strictly greater (by string
    /// comparison — daemon ids are zero-padded ULIDs per
    /// specs/r1-server.md so lexical = chronological).
    pub async fn try_update(&self, candidate: &str) -> bool {
        let mut guard = self.inner.lock().await;
        match guard.as_deref() {
            Some(current) if current >= candidate => false,
            _ => {
                *guard = Some(candidate.to_string());
                true
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Connect URL builder
// ---------------------------------------------------------------------------

/// Build the connect URL the daemon expects: base ws URL with token
/// in the `Authorization` query and `Last-Event-ID` query if present.
/// Header-based handshake is preferred but `tauri-plugin-websocket`
/// 2.x doesn't expose custom-header API yet (upstream tracking issue
/// pending), so the daemon also accepts the query string fallback.
pub fn build_connect_url(base_url: &str, token: &str, last_event_id: Option<&str>) -> String {
    let mut out = String::with_capacity(base_url.len() + 64);
    out.push_str(base_url);
    out.push(if base_url.contains('?') { '&' } else { '?' });
    out.push_str("token=");
    out.push_str(&urlencode(token));
    if let Some(last) = last_event_id {
        out.push_str("&last_event_id=");
        out.push_str(&urlencode(last));
    }
    out
}

/// Minimal RFC-3986 percent-encoder for query-string values. We don't
/// need the full RFC since tokens are base32/base64 and event ids are
/// ULIDs -- both fit `unreserved` already in practice -- but encode
/// defensively in case the daemon ever issues tokens with `+` or `=`.
fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for &b in s.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(b as char);
            }
            _ => {
                out.push('%');
                out.push_str(&format!("{b:02X}"));
            }
        }
    }
    out
}

// ---------------------------------------------------------------------------
// Reconnect status (audit/scan-rust-stubs.md item #5)
// ---------------------------------------------------------------------------

/// Compact status surface the title-bar pill subscribes to via a
/// `tauri::ipc::Channel<ReconnectStatus>`. The lifecycle event stream
/// carries the full taxonomy; this is the projection-friendly view
/// the WebView pill renders directly.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum ReconnectStatus {
    /// Live socket; lane events flowing.
    Connected,
    /// Backoff window — `attempt` is the 0-based reconnect counter.
    Reconnecting { attempt: u32, next_in_ms: u64 },
    /// Caller-requested shutdown (`shutdown()` flipped the flag) or
    /// retry budget exhausted. The pill renders red until the user
    /// triggers Settings → "Reconnect daemon".
    Offline { reason: String },
}

impl ReconnectStatus {
    #[allow(dead_code)] // audit/scan-rust-stubs.md #10: invoked by run_with
    pub fn from_lifecycle(ev: &LifecycleEvent) -> Self {
        match ev {
            LifecycleEvent::Connecting { .. } => ReconnectStatus::Reconnecting {
                attempt: 0,
                next_in_ms: 0,
            },
            LifecycleEvent::Up { .. } => ReconnectStatus::Connected,
            LifecycleEvent::Reconnecting {
                attempt,
                next_in_ms,
                ..
            } => ReconnectStatus::Reconnecting {
                attempt: *attempt,
                next_in_ms: *next_in_ms,
            },
            LifecycleEvent::Down {
                reason, will_retry, ..
            } => {
                if *will_retry {
                    ReconnectStatus::Reconnecting {
                        attempt: 0,
                        next_in_ms: 0,
                    }
                } else {
                    ReconnectStatus::Offline {
                        reason: reason.clone(),
                    }
                }
            }
        }
    }
}

// ---------------------------------------------------------------------------
// TransportHandle — caller-facing state
// ---------------------------------------------------------------------------

/// Lifecycle channel rx the consumer drives the banner from.
#[allow(dead_code)] // audit/scan-rust-stubs.md #10: returned by run_with(), pending wire-up
pub type LifecycleRx = mpsc::Receiver<LifecycleEvent>;

/// Inbound frame channel rx the lanes/IPC layer drives from.
#[allow(dead_code)] // audit/scan-rust-stubs.md #10: returned by run_with(), pending wire-up
pub type FrameRx = mpsc::Receiver<InboundFrame>;

/// One connect attempt's outcome. The closure passed into
/// `TransportHandle::run_with` returns this so the run-loop knows
/// whether to retry, replay-from a Last-Event-ID, or surface a hard
/// fault to the caller.
#[derive(Debug)]
#[allow(dead_code)] // audit/scan-rust-stubs.md #10: consumed by run_with
pub enum ConnectOutcome {
    /// Connected, served traffic, then the socket closed cleanly with
    /// `last_event_id` as the most recent observed cursor (None if no
    /// events arrived).
    Closed { last_event_id: Option<String> },
    /// Connect attempt failed before the handshake completed.
    Failed { reason: String },
}

/// Owns the run-loop's stop flag plus the cursor and policy. Cloneable
/// so the lanes layer can share the cursor without holding the
/// run-loop's join handle.
#[derive(Clone)]
#[allow(dead_code)] // audit/scan-rust-stubs.md #10: constructed from future setup() hook
pub struct TransportHandle {
    pub last_event_id: Arc<LastEventId>,
    pub policy: BackoffPolicy,
    stop: Arc<AtomicBool>,
}

impl TransportHandle {
    #[allow(dead_code)] // audit/scan-rust-stubs.md #10: constructed from future setup() hook
    pub fn new(policy: BackoffPolicy) -> Self {
        Self {
            last_event_id: Arc::new(LastEventId::new()),
            policy,
            stop: Arc::new(AtomicBool::new(false)),
        }
    }

    /// Signal the run-loop to stop after the current attempt.
    /// `lifecycle_rx` will see a final `Down { will_retry: false }`.
    #[allow(dead_code)] // audit/scan-rust-stubs.md #10: invoked from app shutdown handler
    pub fn shutdown(&self) {
        self.stop.store(true, AtomicOrdering::SeqCst);
    }

    #[allow(dead_code)] // audit/scan-rust-stubs.md #10: read by run_with's outer guard
    pub fn is_shutdown(&self) -> bool {
        self.stop.load(AtomicOrdering::SeqCst)
    }

    /// Drive the reconnect run-loop. `connect` is called once per
    /// attempt; on each `ConnectOutcome::Closed` we update the
    /// `last_event_id` cursor and back off before reconnecting.
    /// Lifecycle events are emitted to `lifecycle_tx` for the banner;
    /// projection-friendly status frames go to `status_tx` (typically
    /// a `tauri::ipc::Channel<ReconnectStatus>` adapter held by the
    /// title-bar pill).
    ///
    /// Wired per audit/scan-rust-stubs.md item #5: previously the
    /// run-loop did not exist, so on daemon disconnect there was no
    /// backoff and no Last-Event-ID handshake. The closure-based
    /// connect surface keeps `tauri-plugin-websocket` out of this
    /// crate's mandatory dependency chain — the bin target can plug
    /// any WS implementation in.
    #[allow(dead_code)] // audit/scan-rust-stubs.md #10: driven by future setup() hook
    pub async fn run_with<F, Fut>(
        &self,
        mut connect: F,
        lifecycle_tx: mpsc::Sender<LifecycleEvent>,
        status_tx: mpsc::Sender<ReconnectStatus>,
        url: String,
    ) where
        F: FnMut(String, Option<String>) -> Fut + Send,
        Fut: std::future::Future<Output = ConnectOutcome> + Send,
    {
        let mut attempt: u32 = 0;
        loop {
            if self.is_shutdown() {
                let down = LifecycleEvent::Down {
                    url: url.clone(),
                    reason: "shutdown requested".into(),
                    will_retry: false,
                };
                let _ = status_tx
                    .send(ReconnectStatus::from_lifecycle(&down))
                    .await;
                let _ = lifecycle_tx.send(down).await;
                return;
            }

            // Notify "connecting" before the handshake.
            let connecting = LifecycleEvent::Connecting { url: url.clone() };
            let _ = status_tx
                .send(ReconnectStatus::from_lifecycle(&connecting))
                .await;
            let _ = lifecycle_tx.send(connecting).await;

            let last = self.last_event_id.get().await;
            match connect(url.clone(), last.clone()).await {
                ConnectOutcome::Closed { last_event_id } => {
                    if let Some(id) = last_event_id.as_deref() {
                        self.last_event_id.try_update(id).await;
                    }
                    let up = LifecycleEvent::Up {
                        url: url.clone(),
                        replayed_from: last.clone(),
                    };
                    let _ = status_tx.send(ReconnectStatus::from_lifecycle(&up)).await;
                    let _ = lifecycle_tx.send(up).await;

                    // Connection ended; emit Down then back off.
                    attempt = 0;
                    let down = LifecycleEvent::Down {
                        url: url.clone(),
                        reason: "socket closed".into(),
                        will_retry: !self.is_shutdown(),
                    };
                    let _ = status_tx
                        .send(ReconnectStatus::from_lifecycle(&down))
                        .await;
                    let _ = lifecycle_tx.send(down).await;
                }
                ConnectOutcome::Failed { reason } => {
                    let down = LifecycleEvent::Down {
                        url: url.clone(),
                        reason: reason.clone(),
                        will_retry: !self.is_shutdown(),
                    };
                    let _ = status_tx
                        .send(ReconnectStatus::from_lifecycle(&down))
                        .await;
                    let _ = lifecycle_tx.send(down).await;
                }
            }

            if self.is_shutdown() {
                return;
            }

            // Compute jittered backoff for this attempt.
            let base = self.policy.delay_for_attempt(attempt);
            // Use a deterministic seed derived from the system clock
            // so the schedule has spread without depending on rand.
            let seed = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos() as u64)
                .unwrap_or(attempt as u64);
            let delay = jitter(base, seed);

            let reconnecting = LifecycleEvent::Reconnecting {
                url: url.clone(),
                attempt,
                next_in_ms: delay.as_millis() as u64,
            };
            let _ = status_tx
                .send(ReconnectStatus::from_lifecycle(&reconnecting))
                .await;
            let _ = lifecycle_tx.send(reconnecting).await;

            tokio::time::sleep(delay).await;
            attempt = attempt.saturating_add(1);
        }
    }
}

impl Default for TransportHandle {
    fn default() -> Self {
        Self::new(BackoffPolicy::r1_default())
    }
}

/// Apply ±20 % jitter to a base delay. Spec §16 mandates jitter to
/// break thundering herd; ±20 % is a standard choice. Pure function
/// so the same `(delay, seed)` is reproducible in tests.
pub fn jitter(base: Duration, seed: u64) -> Duration {
    if base.is_zero() {
        return base;
    }
    // Map seed to [-0.20, +0.20] using a stable LCG so tests don't
    // depend on `rand`'s thread-RNG being deterministic.
    let s = seed.wrapping_mul(2862933555777941757).wrapping_add(3037000493);
    let frac = (s as f64) / (u64::MAX as f64); // [0, 1)
    let signed = (frac * 0.40) - 0.20; // [-0.20, +0.20)
    let nanos = base.as_nanos() as f64;
    let jittered = (nanos * (1.0 + signed)).max(0.0);
    Duration::from_nanos(jittered as u64)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_progression_caps_at_max() {
        let p = BackoffPolicy::r1_default();
        assert_eq!(p.delay_for_attempt(0), Duration::from_millis(250));
        assert_eq!(p.delay_for_attempt(1), Duration::from_millis(500));
        assert_eq!(p.delay_for_attempt(2), Duration::from_secs(1));
        assert_eq!(p.delay_for_attempt(3), Duration::from_secs(2));
        assert_eq!(p.delay_for_attempt(4), Duration::from_secs(4));
        assert_eq!(p.delay_for_attempt(5), Duration::from_secs(8));
        assert_eq!(p.delay_for_attempt(6), Duration::from_secs(16));
        // Anything beyond the 16-s cap stays clamped.
        assert_eq!(p.delay_for_attempt(20), Duration::from_secs(16));
        assert_eq!(p.delay_for_attempt(u32::MAX), Duration::from_secs(16));
    }

    #[test]
    fn backoff_handles_factor_one() {
        let p = BackoffPolicy {
            initial: Duration::from_millis(100),
            max: Duration::from_millis(500),
            factor: 1,
        };
        // factor=1 keeps the delay constant rather than growing.
        assert_eq!(p.delay_for_attempt(3), Duration::from_millis(100));
    }

    #[test]
    fn jitter_stays_within_20_percent_band() {
        let base = Duration::from_millis(1000);
        for seed in 0..100u64 {
            let j = jitter(base, seed);
            let lo = (1000.0 * 0.80) as u128;
            let hi = (1000.0 * 1.20) as u128;
            let ms = j.as_millis();
            assert!(
                ms >= lo && ms <= hi,
                "seed {seed}: expected {ms} in [{lo},{hi}]"
            );
        }
    }

    #[test]
    fn jitter_zero_in_zero_out() {
        assert_eq!(jitter(Duration::ZERO, 42), Duration::ZERO);
    }

    #[test]
    fn build_connect_url_appends_token() {
        let u = build_connect_url("ws://127.0.0.1:9", "tok", None);
        assert_eq!(u, "ws://127.0.0.1:9?token=tok");
    }

    #[test]
    fn build_connect_url_appends_last_event_id() {
        let u = build_connect_url("ws://127.0.0.1:9", "tok", Some("01HXY"));
        assert_eq!(u, "ws://127.0.0.1:9?token=tok&last_event_id=01HXY");
    }

    #[test]
    fn build_connect_url_preserves_existing_query() {
        let u = build_connect_url("ws://127.0.0.1:9?session=S01", "tok", None);
        assert_eq!(u, "ws://127.0.0.1:9?session=S01&token=tok");
    }

    #[test]
    fn build_connect_url_percent_encodes_special_chars() {
        let u = build_connect_url("ws://127.0.0.1:9", "a/b+c=d", None);
        // /, +, = all become %XX.
        assert!(u.contains("token=a%2Fb%2Bc%3Dd"), "got: {u}");
    }

    #[tokio::test]
    async fn last_event_id_is_monotonic() {
        let cur = LastEventId::new();
        assert_eq!(cur.get().await, None);
        assert!(cur.try_update("01HXY").await);
        assert_eq!(cur.get().await, Some("01HXY".into()));
        // Strictly-greater wins.
        assert!(cur.try_update("01HXZ").await);
        assert_eq!(cur.get().await, Some("01HXZ".into()));
        // Equal does NOT update.
        assert!(!cur.try_update("01HXZ").await);
        // Earlier id is rejected.
        assert!(!cur.try_update("01HXY").await);
        assert_eq!(cur.get().await, Some("01HXZ".into()));
    }

    #[test]
    fn transport_handle_shutdown_flag_flips() {
        let h = TransportHandle::default();
        assert!(!h.is_shutdown());
        h.shutdown();
        assert!(h.is_shutdown());
    }

    #[test]
    fn lifecycle_event_serialises_with_kind_tag() {
        let ev = LifecycleEvent::Up {
            url: "ws://127.0.0.1:9".into(),
            replayed_from: Some("01HXY".into()),
        };
        let json = serde_json::to_string(&ev).expect("LifecycleEvent serialises");
        assert!(json.contains(r#""kind":"up""#));
        assert!(json.contains(r#""replayed_from":"01HXY""#));
    }

    #[test]
    fn lifecycle_event_round_trips() {
        let ev = LifecycleEvent::Reconnecting {
            url: "ws://127.0.0.1:9".into(),
            attempt: 3,
            next_in_ms: 4000,
        };
        let json = serde_json::to_string(&ev).expect("LifecycleEvent serialises");
        let back: LifecycleEvent =
            serde_json::from_str(&json).expect("LifecycleEvent round-trips");
        assert_eq!(back, ev);
    }

    #[test]
    fn inbound_frame_captures_last_event_id() {
        let raw = r#"{"event":"lane.delta","last_event_id":"01HXY","payload":{}}"#;
        let f: InboundFrame =
            serde_json::from_str(raw).expect("InboundFrame parses");
        assert_eq!(f.event.as_deref(), Some("lane.delta"));
        assert_eq!(f.last_event_id.as_deref(), Some("01HXY"));
        assert!(f.rest.contains_key("payload"));
    }
}
