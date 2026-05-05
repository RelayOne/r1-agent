// SPDX-License-Identifier: MIT
//
// R1 Desktop lane subscription forwarder.
//
// Implements spec desktop-cortex-augmentation §8: one
// `tauri::ipc::Channel<LaneEvent>` per session, multiplexing all of
// that session's lanes. The forwarder task `select!`s an internal
// mpsc fed by transport.rs (item 16) and calls `channel.send(event)`
// with backpressure handling per R3 / R7 in §12:
//
//   * Per-channel ring of 1024 events. Status / spawn / kill events
//     never drop. lane.delta drops on overflow with a single emitted
//     `lane.delta.gap` marker so the UI can re-fetch via
//     `session.lanes.list`.
//   * `lane.status_changed` flushes pending deltas for that lane
//     before being forwarded, so the UI never renders a "done" lane
//     that's still streaming (R7 mitigation).
//   * Channel `send` failure tears the subscription down -- the
//     WebView dropped its end (window closed, navigation, etc.).
//
// The Tauri commands `session_lanes_subscribe` and
// `session_lanes_unsubscribe` register/deregister forwarders against
// a `LanesState` held as `tauri::State<>` by the host.

use std::collections::{HashMap, VecDeque};
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;

use crate::errors::IpcError;

// ---------------------------------------------------------------------------
// LaneEvent — wire shape mirroring TS LaneEvent in
// packages/web-components/src/types/LaneEvent.ts
// ---------------------------------------------------------------------------

/// One frame sent to the WebView over a `tauri::ipc::Channel<LaneEvent>`.
///
/// `kind` is the discriminant — TS narrows on it via the type guards
/// (`isDelta`, `isStatus`, ...). The match between Rust and TS is
/// asserted by the e2e suite (item 36).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum LaneEvent {
    Delta {
        session_id: String,
        lane_id: String,
        seq: u64,
        payload: serde_json::Value,
    },
    Status {
        session_id: String,
        lane_id: String,
        from: String,
        to: String,
        at: String,
    },
    Spawned {
        session_id: String,
        lane_id: String,
        title: String,
        at: String,
    },
    Killed {
        session_id: String,
        lane_id: String,
        reason: String,
        at: String,
    },
    /// Sentinel emitted by the forwarder when overflow drops deltas
    /// (R3) or when reconnect replay missed events (R9).
    DeltaGap {
        session_id: String,
        lane_id: String,
        last_seen_seq: u64,
        at: String,
    },
}

impl LaneEvent {
    pub fn session_id(&self) -> &str {
        match self {
            LaneEvent::Delta { session_id, .. }
            | LaneEvent::Status { session_id, .. }
            | LaneEvent::Spawned { session_id, .. }
            | LaneEvent::Killed { session_id, .. }
            | LaneEvent::DeltaGap { session_id, .. } => session_id,
        }
    }

    pub fn lane_id(&self) -> &str {
        match self {
            LaneEvent::Delta { lane_id, .. }
            | LaneEvent::Status { lane_id, .. }
            | LaneEvent::Spawned { lane_id, .. }
            | LaneEvent::Killed { lane_id, .. }
            | LaneEvent::DeltaGap { lane_id, .. } => lane_id,
        }
    }

    pub fn is_droppable(&self) -> bool {
        matches!(self, LaneEvent::Delta { .. })
    }
}

// ---------------------------------------------------------------------------
// Per-session backpressure ring (R3)
// ---------------------------------------------------------------------------

/// Ring of pending events per lane that the forwarder coalesces
/// before sending. Status / spawn / kill never drop; deltas drop on
/// overflow and emit a single `DeltaGap` marker per overflow window.
#[derive(Debug)]
pub struct LaneBuffer {
    pub deltas: VecDeque<LaneEvent>,
    /// Last delta seq we accepted into the ring. Carried into the
    /// `DeltaGap` marker on overflow so the UI knows where the gap
    /// starts.
    pub last_seen_seq: u64,
    /// True after an overflow until the UI consumes the gap marker
    /// (i.e., we suppress further markers until the next overflow
    /// window).
    pub gap_pending: bool,
    pub capacity: usize,
}

impl LaneBuffer {
    pub fn new(capacity: usize) -> Self {
        Self {
            deltas: VecDeque::with_capacity(capacity),
            last_seen_seq: 0,
            gap_pending: false,
            capacity: capacity.max(1),
        }
    }

    /// Push a delta. Returns `Some(gap_marker)` if overflow caused a
    /// drop AND no gap marker is already pending; the caller forwards
    /// the marker as a synthesised LaneEvent::DeltaGap.
    pub fn push_delta(&mut self, ev: LaneEvent) -> Option<LaneEvent> {
        let LaneEvent::Delta {
            session_id,
            lane_id,
            seq,
            ..
        } = &ev
        else {
            return None;
        };
        let session_id = session_id.clone();
        let lane_id = lane_id.clone();
        let seq = *seq;

        let mut overflow = false;
        if self.deltas.len() >= self.capacity {
            self.deltas.pop_front();
            overflow = true;
        }
        self.deltas.push_back(ev);
        if seq > self.last_seen_seq {
            self.last_seen_seq = seq;
        }

        if overflow && !self.gap_pending {
            self.gap_pending = true;
            return Some(LaneEvent::DeltaGap {
                session_id,
                lane_id,
                last_seen_seq: self.last_seen_seq,
                at: now_iso(),
            });
        }
        None
    }

    /// Drain pending deltas in arrival order. Resets `gap_pending`
    /// because subsequent overflow re-arms a new marker.
    pub fn drain(&mut self) -> Vec<LaneEvent> {
        self.gap_pending = false;
        self.deltas.drain(..).collect()
    }
}

fn now_iso() -> String {
    chrono::Utc::now().to_rfc3339()
}

// ---------------------------------------------------------------------------
// Sender abstraction — generic so we can test without Tauri's runtime
// ---------------------------------------------------------------------------

/// Trait the forwarder uses to deliver LaneEvents to the consumer.
/// Production impl wraps `tauri::ipc::Channel<LaneEvent>`; tests use
/// an mpsc-backed mock so no Tauri runtime is required.
#[async_trait::async_trait]
pub trait LaneSink: Send + Sync {
    async fn send(&self, ev: LaneEvent) -> Result<(), LaneSinkError>;
}

#[derive(Debug, thiserror::Error)]
pub enum LaneSinkError {
    #[error("sink closed")]
    Closed,
    #[error("send failed: {0}")]
    Other(String),
}

// ---------------------------------------------------------------------------
// Subscription registry
// ---------------------------------------------------------------------------

/// Forwarder state for a single subscription. Owned by `LanesState`
/// and looked up by `subscription_id`. Holds the per-lane ring map
/// behind a Mutex so the transport-side feeder and the
/// flush-task can both touch it.
pub struct LaneSubscription {
    pub session_id: String,
    pub buffers: Arc<Mutex<HashMap<String, LaneBuffer>>>,
    pub sink: Arc<dyn LaneSink>,
    pub ring_capacity: usize,
}

impl LaneSubscription {
    pub fn new(session_id: String, sink: Arc<dyn LaneSink>, ring_capacity: usize) -> Self {
        Self {
            session_id,
            buffers: Arc::new(Mutex::new(HashMap::new())),
            sink,
            ring_capacity: ring_capacity.max(1),
        }
    }

    /// Ingest one event from transport.rs. Routes to the per-lane
    /// ring or forwards immediately, returns whether the sink is
    /// still alive so the caller can deregister on hard close.
    pub async fn ingest(&self, ev: LaneEvent) -> Result<(), LaneSinkError> {
        // Status events flush pending deltas for that lane FIRST so
        // the UI sees ordering: deltas then status (R7 mitigation).
        if matches!(&ev, LaneEvent::Status { .. }) {
            self.flush_lane(ev.lane_id()).await?;
            return self.sink.send(ev).await;
        }

        // Spawned / Killed / DeltaGap (synthetic) bypass the ring.
        if !ev.is_droppable() {
            return self.sink.send(ev).await;
        }

        // Delta path: ring, with gap marker on overflow.
        let lane_id = ev.lane_id().to_string();
        let gap_marker = {
            let mut bufs = self.buffers.lock().await;
            let buf = bufs
                .entry(lane_id.clone())
                .or_insert_with(|| LaneBuffer::new(self.ring_capacity));
            buf.push_delta(ev)
        };
        if let Some(marker) = gap_marker {
            self.sink.send(marker).await?;
        }
        Ok(())
    }

    async fn flush_lane(&self, lane_id: &str) -> Result<(), LaneSinkError> {
        let drained = {
            let mut bufs = self.buffers.lock().await;
            match bufs.get_mut(lane_id) {
                Some(b) => b.drain(),
                None => return Ok(()),
            }
        };
        for ev in drained {
            self.sink.send(ev).await?;
        }
        Ok(())
    }

    /// Force-flush every lane in the subscription. Used by a periodic
    /// 100 ms tick (RT-DESKTOP-TAURI §7) so steady-state delta flow
    /// reaches the UI without waiting for a status flip.
    pub async fn flush_all(&self) -> Result<(), LaneSinkError> {
        let lane_ids: Vec<String> = {
            let bufs = self.buffers.lock().await;
            bufs.keys().cloned().collect()
        };
        for lane_id in lane_ids {
            self.flush_lane(&lane_id).await?;
        }
        Ok(())
    }
}

/// Top-level state held in `tauri::State<>`. Maps `subscription_id` to
/// a live forwarder. Supports register / drop atomically.
#[derive(Default)]
pub struct LanesState {
    inner: Mutex<HashMap<String, Arc<LaneSubscription>>>,
}

impl LanesState {
    pub fn new() -> Self {
        Self::default()
    }

    pub async fn register(&self, sub_id: String, sub: LaneSubscription) {
        let mut g = self.inner.lock().await;
        g.insert(sub_id, Arc::new(sub));
    }

    pub async fn unregister(&self, sub_id: &str) -> bool {
        let mut g = self.inner.lock().await;
        g.remove(sub_id).is_some()
    }

    pub async fn get(&self, sub_id: &str) -> Option<Arc<LaneSubscription>> {
        let g = self.inner.lock().await;
        g.get(sub_id).cloned()
    }

    pub async fn count(&self) -> usize {
        let g = self.inner.lock().await;
        g.len()
    }
}

// ---------------------------------------------------------------------------
// IPC verbs (spec §6.1)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LaneSubscribeResult {
    pub subscription_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LaneUnsubscribeParams {
    pub subscription_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LaneUnsubscribeResult {
    pub ok: bool,
}

/// Tauri-host-side fast path that maps a missing subscription to the
/// IPC `not_found` taxonomy code so callers can pattern-match without
/// a wire round-trip to the daemon.
pub async fn unsubscribe(
    state: &LanesState,
    params: LaneUnsubscribeParams,
) -> Result<LaneUnsubscribeResult, IpcError> {
    if state.unregister(&params.subscription_id).await {
        Ok(LaneUnsubscribeResult { ok: true })
    } else {
        Err(IpcError::not_found(format!(
            "subscription {}",
            params.subscription_id
        )))
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::sync::mpsc;

    /// mpsc-backed sink used by tests. The receive half is held by
    /// the test so it can assert on emitted events in arrival order.
    struct MpscSink {
        tx: mpsc::UnboundedSender<LaneEvent>,
    }

    #[async_trait::async_trait]
    impl LaneSink for MpscSink {
        async fn send(&self, ev: LaneEvent) -> Result<(), LaneSinkError> {
            self.tx.send(ev).map_err(|_| LaneSinkError::Closed)
        }
    }

    fn delta(session: &str, lane: &str, seq: u64) -> LaneEvent {
        LaneEvent::Delta {
            session_id: session.into(),
            lane_id: lane.into(),
            seq,
            payload: serde_json::json!({"kind":"token","text":"."}),
        }
    }

    #[test]
    fn lane_buffer_pushes_under_capacity() {
        let mut buf = LaneBuffer::new(4);
        assert!(buf.push_delta(delta("S1", "L1", 1)).is_none());
        assert!(buf.push_delta(delta("S1", "L1", 2)).is_none());
        assert_eq!(buf.deltas.len(), 2);
        assert_eq!(buf.last_seen_seq, 2);
        assert!(!buf.gap_pending);
    }

    #[test]
    fn lane_buffer_emits_gap_on_overflow_once() {
        let mut buf = LaneBuffer::new(2);
        assert!(buf.push_delta(delta("S1", "L1", 1)).is_none());
        assert!(buf.push_delta(delta("S1", "L1", 2)).is_none());
        // Third push overflows; gap marker emitted.
        let marker = buf
            .push_delta(delta("S1", "L1", 3))
            .expect("overflow emits marker");
        assert!(matches!(marker, LaneEvent::DeltaGap { last_seen_seq: 3, .. }));
        // Second overflow within the same window does NOT re-emit.
        assert!(buf.push_delta(delta("S1", "L1", 4)).is_none());
        assert!(buf.gap_pending);
    }

    #[test]
    fn lane_buffer_drain_resets_gap_flag() {
        let mut buf = LaneBuffer::new(1);
        buf.push_delta(delta("S1", "L1", 1));
        // overflow path arms the flag
        let _ = buf.push_delta(delta("S1", "L1", 2));
        assert!(buf.gap_pending);
        let drained = buf.drain();
        assert_eq!(drained.len(), 1);
        assert!(!buf.gap_pending);
    }

    #[tokio::test]
    async fn subscription_routes_status_after_flushing_deltas() {
        let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 16);

        sub.ingest(delta("S1", "L1", 1)).await.expect("ingest");
        sub.ingest(delta("S1", "L1", 2)).await.expect("ingest");
        sub.ingest(LaneEvent::Status {
            session_id: "S1".into(),
            lane_id: "L1".into(),
            from: "running".into(),
            to: "done".into(),
            at: now_iso(),
        })
        .await
        .expect("ingest status");

        // Order: Delta(seq=1), Delta(seq=2), Status. R7 mitigation.
        let e1 = rx.recv().await.expect("first event");
        assert!(matches!(e1, LaneEvent::Delta { seq: 1, .. }));
        let e2 = rx.recv().await.expect("second event");
        assert!(matches!(e2, LaneEvent::Delta { seq: 2, .. }));
        let e3 = rx.recv().await.expect("third event");
        assert!(matches!(e3, LaneEvent::Status { .. }));
    }

    #[tokio::test]
    async fn subscription_emits_gap_marker_on_overflow() {
        let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 2);

        sub.ingest(delta("S1", "L1", 1)).await.expect("ingest 1");
        sub.ingest(delta("S1", "L1", 2)).await.expect("ingest 2");
        // Overflow now -- third delta pushes seq=1 out and emits gap marker.
        sub.ingest(delta("S1", "L1", 3)).await.expect("ingest 3");

        let first = rx.recv().await.expect("gap arrives");
        assert!(matches!(first, LaneEvent::DeltaGap { last_seen_seq: 3, .. }));
    }

    #[tokio::test]
    async fn subscription_forwards_spawn_and_kill_immediately() {
        let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 16);

        sub.ingest(LaneEvent::Spawned {
            session_id: "S1".into(),
            lane_id: "L9".into(),
            title: "explore".into(),
            at: now_iso(),
        })
        .await
        .expect("spawn");
        sub.ingest(LaneEvent::Killed {
            session_id: "S1".into(),
            lane_id: "L9".into(),
            reason: "operator".into(),
            at: now_iso(),
        })
        .await
        .expect("kill");

        let s = rx.recv().await.expect("spawn out");
        assert!(matches!(s, LaneEvent::Spawned { .. }));
        let k = rx.recv().await.expect("kill out");
        assert!(matches!(k, LaneEvent::Killed { .. }));
    }

    #[tokio::test]
    async fn subscription_flush_all_drains_every_lane() {
        let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 16);

        sub.ingest(delta("S1", "L1", 1)).await.expect("L1");
        sub.ingest(delta("S1", "L2", 1)).await.expect("L2");
        sub.flush_all().await.expect("flush_all");

        // Two events drained -- order across lanes is unspecified but
        // both must arrive.
        let mut got_l1 = false;
        let mut got_l2 = false;
        for _ in 0..2 {
            let ev = rx.recv().await.expect("flushed event");
            match ev.lane_id() {
                "L1" => got_l1 = true,
                "L2" => got_l2 = true,
                other => panic!("unexpected lane_id: {other}"),
            }
        }
        assert!(got_l1 && got_l2);
    }

    #[tokio::test]
    async fn subscription_send_failure_propagates() {
        // Drop the receiver immediately so every send sees Closed.
        let (tx, rx) = mpsc::unbounded_channel::<LaneEvent>();
        drop(rx);
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 16);

        let err = sub
            .ingest(LaneEvent::Spawned {
                session_id: "S1".into(),
                lane_id: "L1".into(),
                title: "x".into(),
                at: now_iso(),
            })
            .await
            .expect_err("closed sink errors");
        assert!(matches!(err, LaneSinkError::Closed));
    }

    #[tokio::test]
    async fn lanes_state_register_unregister_round_trip() {
        let st = LanesState::new();
        let (tx, _rx) = mpsc::unbounded_channel::<LaneEvent>();
        let sink = Arc::new(MpscSink { tx });
        let sub = LaneSubscription::new("S1".into(), sink, 16);
        st.register("sub-1".into(), sub).await;
        assert_eq!(st.count().await, 1);
        assert!(st.get("sub-1").await.is_some());
        assert!(st.unregister("sub-1").await);
        assert_eq!(st.count().await, 0);
        // Idempotent: second unregister returns false.
        assert!(!st.unregister("sub-1").await);
    }

    #[tokio::test]
    async fn unsubscribe_returns_not_found_for_missing_id() {
        let st = LanesState::new();
        let res = unsubscribe(
            &st,
            LaneUnsubscribeParams {
                subscription_id: "nope".into(),
            },
        )
        .await;
        let err = res.expect_err("missing id is not_found");
        assert_eq!(err.stoke_code, "not_found");
    }

    #[test]
    fn lane_event_serialises_with_kind_tag() {
        let ev = LaneEvent::Spawned {
            session_id: "S".into(),
            lane_id: "L".into(),
            title: "t".into(),
            at: "2026-05-04T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&ev).expect("LaneEvent serialises");
        assert!(json.contains(r#""kind":"spawned""#));
    }

    #[test]
    fn lane_event_helpers_extract_ids() {
        let ev = delta("S2", "L7", 100);
        assert_eq!(ev.session_id(), "S2");
        assert_eq!(ev.lane_id(), "L7");
        assert!(ev.is_droppable());
        let ev2 = LaneEvent::Killed {
            session_id: "S2".into(),
            lane_id: "L7".into(),
            reason: "x".into(),
            at: now_iso(),
        };
        assert!(!ev2.is_droppable());
    }
}
