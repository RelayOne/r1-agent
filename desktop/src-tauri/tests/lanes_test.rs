// SPDX-License-Identifier: MIT
//
// Integration tests for `lanes::*` — spec
// desktop-cortex-augmentation §11.2 + checklist item 33.
//
// Per the spec: "feed an mpsc 100 fake `LaneEvent`s and assert the
// test-side channel receiver got them in order." Pinned through the
// lib target so we don't need a Tauri runtime; the production
// `LaneSink` is a thin wrapper over `tauri::ipc::Channel`, but
// `LaneSubscription::ingest` is generic over `dyn LaneSink`, so an
// mpsc-backed test sink exercises the same code path.
//
// Plus: cross-lane interleaving (R7 ordering guarantee), overflow
// gap-marker emission, and per-subscription teardown via
// `LanesState::unregister`.

use std::sync::Arc;

use async_trait::async_trait;
use r1_desktop::lanes::{
    unsubscribe, LaneEvent, LaneSink, LaneSinkError, LaneSubscription, LaneUnsubscribeParams,
    LanesState,
};
use tokio::sync::mpsc;

// -------------------------------------------------------------------
// Test sink (mpsc-backed)
// -------------------------------------------------------------------

struct MpscSink {
    tx: mpsc::UnboundedSender<LaneEvent>,
}

#[async_trait]
impl LaneSink for MpscSink {
    async fn send(&self, ev: LaneEvent) -> Result<(), LaneSinkError> {
        self.tx.send(ev).map_err(|_| LaneSinkError::Closed)
    }
}

fn delta_event(session: &str, lane: &str, seq: u64) -> LaneEvent {
    LaneEvent::Delta {
        session_id: session.into(),
        lane_id: lane.into(),
        seq,
        payload: serde_json::json!({"kind":"token","text":"."}),
    }
}

fn iso_now() -> String {
    chrono::Utc::now().to_rfc3339()
}

// -------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------

/// Feed 100 deltas into a single lane, then a status event, and
/// assert the receiver got every delta in seq order followed by the
/// status flip — spec §11.2 mandate.
#[tokio::test]
async fn hundred_deltas_arrive_in_order_then_status() {
    let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
    let sink: Arc<dyn LaneSink> = Arc::new(MpscSink { tx });
    // Capacity must be ≥ 100 so no overflow drops; the spec test
    // explicitly says "100 events in order" — overflow is a separate
    // assertion below.
    let sub = LaneSubscription::new("S1".into(), sink, 256);

    for seq in 1u64..=100 {
        sub.ingest(delta_event("S1", "L1", seq))
            .await
            .expect("ingest");
    }
    // Drive a status flip — forwarder flushes pending deltas first
    // (R7 ordering guarantee), then sends the status event.
    sub.ingest(LaneEvent::Status {
        session_id: "S1".into(),
        lane_id: "L1".into(),
        from: "running".into(),
        to: "done".into(),
        at: iso_now(),
    })
    .await
    .expect("status");

    let mut received_seqs = Vec::with_capacity(100);
    for _ in 0..100 {
        let ev = rx.recv().await.expect("delta arrives");
        match ev {
            LaneEvent::Delta { seq, .. } => received_seqs.push(seq),
            other => panic!("unexpected pre-status event: {other:?}"),
        }
    }
    assert_eq!(received_seqs, (1u64..=100).collect::<Vec<_>>());

    let after = rx.recv().await.expect("status arrives");
    assert!(matches!(after, LaneEvent::Status { to, .. } if to == "done"));
}

/// Two interleaved lanes — each lane's deltas arrive in order,
/// neither lane's status is delivered before its trailing deltas
/// (R7).
#[tokio::test]
async fn cross_lane_interleaving_preserves_per_lane_order() {
    let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
    let sink: Arc<dyn LaneSink> = Arc::new(MpscSink { tx });
    let sub = LaneSubscription::new("S1".into(), sink, 256);

    // Interleave 5 events on each lane.
    for n in 1u64..=5 {
        sub.ingest(delta_event("S1", "L1", n)).await.expect("L1 d");
        sub.ingest(delta_event("S1", "L2", n + 100))
            .await
            .expect("L2 d");
    }
    // Flip L1 to done. Forwarder flushes L1 deltas first, then sends
    // the L1 status. L2 buffer is untouched.
    sub.ingest(LaneEvent::Status {
        session_id: "S1".into(),
        lane_id: "L1".into(),
        from: "running".into(),
        to: "done".into(),
        at: iso_now(),
    })
    .await
    .expect("L1 status");

    // Collect events: 5× L1 deltas, then L1 status. L2 is silent.
    let mut got: Vec<(String, Option<u64>, Option<String>)> = Vec::new();
    for _ in 0..6 {
        let ev = rx.recv().await.expect("ev arrives");
        match ev {
            LaneEvent::Delta { lane_id, seq, .. } => got.push((lane_id, Some(seq), None)),
            LaneEvent::Status { lane_id, to, .. } => got.push((lane_id, None, Some(to))),
            other => panic!("unexpected event: {other:?}"),
        }
    }
    let l1_seqs: Vec<u64> = got
        .iter()
        .filter(|(l, _, _)| l == "L1")
        .filter_map(|(_, s, _)| *s)
        .collect();
    assert_eq!(l1_seqs, vec![1, 2, 3, 4, 5]);
    // The 6th event must be L1 status (R7).
    assert!(matches!(&got[5], (l, _, Some(s)) if l == "L1" && s == "done"));
}

/// Overflow on a small ring emits a single DeltaGap marker. Subsequent
/// overflow within the same window does NOT re-emit (deduplicated by
/// `gap_pending`).
#[tokio::test]
async fn overflow_emits_one_gap_marker_per_window() {
    let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
    let sink: Arc<dyn LaneSink> = Arc::new(MpscSink { tx });
    // Capacity 3 so the 4th delta forces overflow.
    let sub = LaneSubscription::new("S1".into(), sink, 3);

    for seq in 1u64..=10 {
        sub.ingest(delta_event("S1", "L1", seq))
            .await
            .expect("ingest");
    }

    // Exactly one DeltaGap event (carries last_seen_seq = 4 — first
    // overflow happens when seq 4 arrives and pushes seq 1 out).
    let ev = rx.recv().await.expect("gap arrives");
    let last_seen = match ev {
        LaneEvent::DeltaGap { last_seen_seq, .. } => last_seen_seq,
        other => panic!("expected DeltaGap, got {other:?}"),
    };
    assert_eq!(last_seen, 4);
    // No further events because the rest are buffered until status flush.
    assert!(rx.try_recv().is_err());
}

/// `LanesState::register` then `unsubscribe(...)` round-trips
/// through the public surface; missing id maps to `not_found`.
#[tokio::test]
async fn lanes_state_unsubscribe_round_trip() {
    let st = LanesState::new();
    let (tx, _rx) = mpsc::unbounded_channel::<LaneEvent>();
    let sink: Arc<dyn LaneSink> = Arc::new(MpscSink { tx });
    let sub = LaneSubscription::new("S1".into(), sink, 8);

    st.register("sub-X".into(), sub).await;
    assert_eq!(st.count().await, 1);

    let res = unsubscribe(
        &st,
        LaneUnsubscribeParams {
            subscription_id: "sub-X".into(),
        },
    )
    .await;
    let ok = res.expect("unsubscribe ok");
    assert!(ok.ok);
    assert_eq!(st.count().await, 0);

    // Idempotent failure on missing id.
    let res2 = unsubscribe(
        &st,
        LaneUnsubscribeParams {
            subscription_id: "sub-X".into(),
        },
    )
    .await;
    let err = res2.expect_err("missing → not_found");
    assert_eq!(err.stoke_code, "not_found");
}

/// Non-droppable events (Spawned / Killed / DeltaGap synthesised
/// upstream) bypass the ring entirely. Verifies they reach the sink
/// even if the ring is at capacity for the same lane.
#[tokio::test]
async fn non_delta_events_bypass_the_ring() {
    let (tx, mut rx) = mpsc::unbounded_channel::<LaneEvent>();
    let sink: Arc<dyn LaneSink> = Arc::new(MpscSink { tx });
    let sub = LaneSubscription::new("S1".into(), sink, 1);

    // Saturate the ring with deltas first.
    for seq in 1u64..=5 {
        sub.ingest(delta_event("S1", "L1", seq))
            .await
            .expect("delta");
    }
    // Drain the gap marker the saturation emitted so we see only
    // the Spawned/Killed events below.
    let _ = rx.recv().await;

    sub.ingest(LaneEvent::Spawned {
        session_id: "S1".into(),
        lane_id: "L9".into(),
        title: "explore".into(),
        at: iso_now(),
    })
    .await
    .expect("spawn");
    sub.ingest(LaneEvent::Killed {
        session_id: "S1".into(),
        lane_id: "L9".into(),
        reason: "operator".into(),
        at: iso_now(),
    })
    .await
    .expect("kill");

    let s = rx.recv().await.expect("spawn out");
    assert!(matches!(s, LaneEvent::Spawned { .. }));
    let k = rx.recv().await.expect("kill out");
    assert!(matches!(k, LaneEvent::Killed { .. }));
}
