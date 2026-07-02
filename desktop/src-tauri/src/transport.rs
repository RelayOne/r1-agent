// SPDX-License-Identifier: MIT
//
// R1 Desktop transport policy primitives.
//
// The desktop currently ships with the per-session SubprocessManager
// (subprocess.rs) as its ONLY live transport. The shared daemon-WS
// run-loop this module once scaffolded is deferred until the
// multi-session daemon revision; per
// audit/complete-systems-2026-07-01.md A095 the mutually-dormant
// scaffolding (TransportHandle, run_with, ConnectOutcome,
// LifecycleEvent, LifecycleRx/FrameRx, InboundFrame, LastEventId,
// jitter) was deleted instead of accumulating dead_code markers.
// What remains is small and real:
//
//   * `BackoffPolicy` — deterministic reconnect schedule
//     (250 ms → 16 s cap) shared with discovery_state.rs.
//   * `build_connect_url` — canonical ws URL builder (token +
//     Last-Event-ID query fallback) used by
//     `DiscoveryState::connect_url`.
//   * `ReconnectStatus` — the typed frame the
//     `transport_reconnect_status` verb (ipc.rs) streams to the
//     title-bar pill; frames are derived from the live
//     DiscoveryState, never hardcoded.

use std::time::Duration;

use serde::{Deserialize, Serialize};

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
    #[allow(dead_code)] // deferred daemon-WS migration helper; unit-tested, no production caller yet (A095)
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
// Reconnect status (transport_reconnect_status verb payload)
// ---------------------------------------------------------------------------

/// Compact status frame the title-bar pill subscribes to via a
/// `tauri::ipc::Channel<ReconnectStatus>`. ipc.rs derives every frame
/// from the live `DiscoveryState` snapshot (audit A095): a connected
/// DaemonHandle maps to `Connected`, an in-flight discovery probe to
/// `Reconnecting`, and a failed probe to `Offline` with the real
/// error string. TS mirror: `ReconnectStatusFrame` in
/// desktop/src/panels/daemon-status.ts.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum ReconnectStatus {
    /// Daemon attached (external or sidecar).
    Connected,
    /// Probe/backoff window — `attempt` is the 0-based counter.
    Reconnecting { attempt: u32, next_in_ms: u64 },
    /// Discovery failed or never started. The pill renders red until
    /// the user triggers Settings → "Reconnect daemon" / the wizard.
    Offline { reason: String },
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

    #[test]
    fn reconnect_status_serialises_with_state_tag() {
        // Pins the wire shape the TS pill narrows on
        // (ReconnectStatusFrame in daemon-status.ts).
        let json = serde_json::to_string(&ReconnectStatus::Reconnecting {
            attempt: 3,
            next_in_ms: 2000,
        })
        .expect("ReconnectStatus serialises");
        assert!(json.contains(r#""state":"reconnecting""#), "got: {json}");
        assert!(json.contains(r#""attempt":3"#), "got: {json}");
        assert!(json.contains(r#""next_in_ms":2000"#), "got: {json}");

        let back: ReconnectStatus =
            serde_json::from_str(r#"{"state":"offline","reason":"probe refused"}"#)
                .expect("ReconnectStatus round-trips");
        assert_eq!(
            back,
            ReconnectStatus::Offline {
                reason: "probe refused".into()
            }
        );
    }
}
