package engine

import (
	"context"
	"testing"
	"time"
)

// TestDetachCtxSurvivesCancellation pins FIX 7: the final shadow-checkpoint
// flush must run even when the run ctx is already cancelled/timed out. The
// detached ctx used for that flush preserves values but is NOT cancelled by
// the parent, so shadow.flush's git can still complete and the last-turn
// checkpoint isn't lost on the cancel/timeout path.
func TestDetachCtxSurvivesCancellation(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "v")
	ctx, cancel := context.WithCancel(parent)
	cancel() // simulate the cancel/timeout return path

	if ctx.Err() == nil {
		t.Fatal("precondition: parent ctx should be cancelled")
	}

	d := detachCtx(ctx)
	if d.Err() != nil {
		t.Errorf("detached ctx must NOT be cancelled by the parent; Err()=%v", d.Err())
	}
	if got, _ := d.Value(key{}).(string); got != "v" {
		t.Errorf("detached ctx must preserve values; got %q want %q", got, "v")
	}

	// A deadline layered on top still applies (teardown stays bounded).
	dctx, dcancel := context.WithTimeout(detachCtx(ctx), 30*time.Second)
	defer dcancel()
	if dctx.Err() != nil {
		t.Errorf("bounded detached ctx should be live, got %v", dctx.Err())
	}
	if _, ok := dctx.Deadline(); !ok {
		t.Error("bounded detached ctx should carry a deadline")
	}
}
