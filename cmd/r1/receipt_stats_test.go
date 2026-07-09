package main

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/receipts"
	"github.com/RelayOne/r1/internal/taskstats"
)

func TestBuildWorkerStats_GroupsAndJoins(t *testing.T) {
	stats := []taskstats.Record{
		{SessionID: "S1", TaskID: "T1", Turns: 5, CostUSD: 0.10, Success: true, ProjectSlug: "proj"},
		{SessionID: "S1", TaskID: "T2", Turns: 3, CostUSD: 0.05, Success: false, ProjectSlug: "proj"},
		{SessionID: "S2", TaskID: "T3", Turns: 8, CostUSD: 0.20, Success: true, ProjectSlug: "proj"},
		// Different project — excluded when project filter is set.
		{SessionID: "S3", TaskID: "T4", Turns: 99, CostUSD: 9.99, Success: true, ProjectSlug: "other"},
	}
	recs := []receipts.Receipt{
		{TaskID: "T1"}, {TaskID: "T1"}, // 2 receipts for T1
		{TaskID: "T3"}, // 1 receipt for T3
	}

	rows := buildWorkerStats(stats, recs, "proj")

	if len(rows) != 2 {
		t.Fatalf("expected 2 workers (proj only), got %d: %+v", len(rows), rows)
	}
	// Sorted by worker id: S1 then S2.
	s1 := rows[0]
	if s1.Worker != "S1" || s1.Tasks != 2 || s1.Turns != 8 {
		t.Fatalf("S1 aggregation wrong: %+v", s1)
	}
	if s1.Passed != 1 || s1.Failed != 1 {
		t.Fatalf("S1 pass/fail wrong: %+v", s1)
	}
	if s1.Receipts != 2 { // T1 had 2 receipts, T2 had 0
		t.Fatalf("S1 receipts join wrong: %+v", s1)
	}
	if s1.CostUSD < 0.149 || s1.CostUSD > 0.151 {
		t.Fatalf("S1 cost sum wrong: %+v", s1)
	}
	s2 := rows[1]
	if s2.Worker != "S2" || s2.Turns != 8 || s2.Receipts != 1 || s2.Passed != 1 {
		t.Fatalf("S2 aggregation wrong: %+v", s2)
	}

	total := totalRow(rows)
	if total.Tasks != 3 || total.Turns != 16 || total.Receipts != 3 || total.Passed != 2 || total.Failed != 1 {
		t.Fatalf("total row wrong: %+v", total)
	}
}

func TestBuildWorkerStats_NoProjectFilterIncludesAll(t *testing.T) {
	stats := []taskstats.Record{
		{SessionID: "S1", TaskID: "T1", Turns: 1, Success: true, ProjectSlug: "a"},
		{SessionID: "S2", TaskID: "T2", Turns: 2, Success: true, ProjectSlug: "b"},
	}
	rows := buildWorkerStats(stats, nil, "")
	if len(rows) != 2 {
		t.Fatalf("no filter must include all projects, got %d", len(rows))
	}
}

func TestBuildWorkerStats_UnknownSession(t *testing.T) {
	stats := []taskstats.Record{{TaskID: "T1", Turns: 1, Success: false, Timestamp: time.Now()}}
	rows := buildWorkerStats(stats, nil, "")
	if len(rows) != 1 || rows[0].Worker != "(unknown)" || rows[0].Failed != 1 {
		t.Fatalf("unknown-session bucket wrong: %+v", rows)
	}
}
