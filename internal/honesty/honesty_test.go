package honesty

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

func TestRecordAndQuery(t *testing.T) {
	lg, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer lg.Close()
	d := Decision{
		Kind:      KindRefused,
		TaskID:    "task-9",
		Claim:     "ship as live-verified",
		Reason:    "no live probe evidence",
		Evidence:  []string{"curl missing"},
		CreatedAt: time.Now().UTC(),
	}
	if _, err := Record(lg, "worker", "mission-1", d); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := Query(lg, "task-9")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query len=%d want 1", len(got))
	}
	if got[0].Reason != d.Reason {
		t.Fatalf("reason=%q want %q", got[0].Reason, d.Reason)
	}
}
