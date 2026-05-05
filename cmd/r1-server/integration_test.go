// Package main — integration_test.go
//
// Spec 3 §10 T11 + T12. Two render-level integration tests that
// exercise the new partials end-to-end against a hand-built
// template context. Full HTTP-level coverage (fixture session DB +
// SSE replay) is left for Spec 5.
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// renderPartial renders a single named partial against the parse
// tree from parseV2Templates. Helps the tests pin down a single
// block's output without running the whole page shell.
func renderPartial(t *testing.T, name string, ctx interface{}) string {
	t.Helper()
	tmpl, err := parseV2Templates()
	if err != nil {
		t.Fatalf("parseV2Templates: %v", err)
	}
	if tmpl == nil {
		t.Fatal("parseV2Templates returned nil template")
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, ctx); err != nil {
		t.Fatalf("execute %q: %v", name, err)
	}
	return buf.String()
}

func TestEventRendering_RedactedNode_RendersLockInWaterfallRow(t *testing.T) {
	ctx := struct {
		NodeID              string
		NodeType            string
		CreatedAtUnix       int64
		Depth               int
		Chevron             string
		TypeIcon            string
		Title               string
		DurationStr         string
		CostStr             string
		IsRedacted          bool
		IsRedactionAnomaly  bool
	}{
		NodeID:        "n-redacted-3",
		NodeType:      "agent_io",
		CreatedAtUnix: 1715000000,
		Depth:         1,
		Chevron:       "▶",
		TypeIcon:      "📦",
		Title:         "operator response",
		DurationStr:   "120ms",
		CostStr:       "$0.001",
		IsRedacted:    true,
	}
	out := renderPartial(t, "waterfall-row", ctx)
	mustContain(t, out, []string{
		`data-node-id="n-redacted-3"`,
		`row--redacted`,
		`data-state="redacted"`,
		`<svg class="icon-lock"`,
		`data-redacted="true"`,
		`[content redacted]`,
	})
	mustNotContain(t, out, []string{
		`📦`, // TypeIcon should NOT show through when redacted
	})
}

func TestEventRendering_RedactedNode_AnomalyOverlayWhenNoEvents(t *testing.T) {
	ctx := struct {
		NodeID              string
		NodeType            string
		CreatedAtUnix       int64
		Depth               int
		Chevron             string
		TypeIcon            string
		Title               string
		DurationStr         string
		CostStr             string
		IsRedacted          bool
		IsRedactionAnomaly  bool
	}{
		NodeID:             "n-redacted-no-log",
		NodeType:           "agent_io",
		CreatedAtUnix:      1715000000,
		IsRedacted:         true,
		IsRedactionAnomaly: true,
	}
	out := renderPartial(t, "waterfall-row", ctx)
	mustContain(t, out, []string{
		`row--redaction-anomaly`,
		`⚠`,
		`aria-label="redacted, no event log"`,
	})
}

func TestEventRendering_SkillLoadedDetail_AllFieldsRendered(t *testing.T) {
	loaded := &nodes.SkillLoaded{
		SkillRef:              "alpha",
		LoadingStanceID:       "stance-cto-1",
		LoadingStanceRole:     "cto",
		ConcernFieldTemplate:  "cto_planning",
		MatchingApplicability: "build",
		TaskDAGScope:          "task-007",
		LoopRef:               "loop-42",
		CreatedAt:             time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC),
		Version:               1,
	}
	out := renderPartial(t, "skill-loaded-detail", loaded)
	mustContain(t, out, []string{
		`alpha`,
		`stance-cto-1`,
		`cto`,
		`cto_planning`,
		`build`,
		`task-007`,
		`loop-42`,
		`2026-05-05 10:30:00 UTC`,
	})
}

func TestEventRendering_SkillUnloadedDetail_FieldsAndReason(t *testing.T) {
	unloaded := &nodes.SkillUnloaded{
		SkillRef:          "alpha",
		LoadRef:           "load-1",
		StanceID:          "stance-cto-1",
		StanceRole:        "cto",
		Reason:            "scope_exit",
		BudgetTokensFreed: 800,
		CreatedAt:         time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC),
		Version:           1,
	}
	out := renderPartial(t, "skill-unloaded-detail", unloaded)
	mustContain(t, out, []string{
		`alpha`,
		`load-1`,
		`stance-cto-1`,
		`scope_exit`,
		`800`,
		`2026-05-05 11:00:00 UTC`,
		`data-state="warning"`,
	})
}

func TestEventRendering_RedactionEvents_ListAndPlural(t *testing.T) {
	events := []RedactionEvent{
		{
			NodeID:      "n-1",
			RedactedAt:  time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
			HumanReason: "redacted by retention policy",
			Signer:      "policy-engine",
		},
		{
			NodeID:      "n-1",
			RedactedAt:  time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
			HumanReason: "redacted under GDPR right-to-erasure",
			Signer:      "operator-alice",
		},
	}
	out := renderPartial(t, "redaction-events", events)
	mustContain(t, out, []string{
		`2 redaction events for this node`,
		`redacted by retention policy`,
		`redacted under GDPR right-to-erasure`,
		`policy-engine`,
		`operator-alice`,
		`2026-05-04 10:00:00 UTC`,
		`2026-05-05 12:00:00 UTC`,
	})
	if strings.Contains(out, "redacted: ") {
		// "redacted: " is the bare-code fallback; should never appear for known reasons
		t.Errorf("redaction-events leaked the bare-code fallback prefix")
	}
}

// helpers

func mustContain(t *testing.T, body string, fragments []string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(body, f) {
			t.Errorf("rendered body missing fragment: %q\n--- body ---\n%s", f, body)
		}
	}
}

func mustNotContain(t *testing.T, body string, fragments []string) {
	t.Helper()
	for _, f := range fragments {
		if strings.Contains(body, f) {
			t.Errorf("rendered body unexpectedly contains: %q", f)
		}
	}
}
