package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMemories_V2Off_404 — the default state. The flag is unset, the
// /memories route must 404 indistinguishably from "no route" per the
// share.go precedent + spec §2.3.
func TestMemories_V2Off_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/memories")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404 when v2 is off", resp.StatusCode)
	}
}

// TestMemories_V2On_EmptyTable — gate on, no memory rows written.
// The render must succeed with a 200 and an "empty" banner so
// operators know the table exists but is idle.
func TestMemories_V2On_EmptyTable(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/memories")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, body)
	}
	bs := string(body)
	if !strings.Contains(bs, "No memory-bus rows yet") {
		t.Error("empty-state banner missing from render")
	}
	if !strings.Contains(bs, "Total rows: 0") {
		t.Error("zero-count header missing from render")
	}
	// Security headers that match the share.go baseline.
	for _, name := range []string{"X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if resp.Header.Get(name) == "" {
			t.Errorf("missing security header %s", name)
		}
	}
}

// TestMemories_GroupsRenderedInOrder — insert rows in three groups
// and assert the Permanent → Always → Global → Older Sessions order
// holds in the rendered HTML (spec §6.1). The check uses strings.Index
// so renaming headings doesn't silently break the ordering invariant.
func TestMemories_GroupsRenderedInOrder(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	now := time.Now()
	if err := db.insertMemoryForTest("permanent", "k-perm", "operator", "perm body", now); err != nil {
		t.Fatalf("seed permanent: %v", err)
	}
	if err := db.insertMemoryForTest("always", "k-always", "operator", "always body", now); err != nil {
		t.Fatalf("seed always: %v", err)
	}
	if err := db.insertMemoryForTest("global", "k-global", "system", "global body", now); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	if err := db.insertMemoryForTest("session", "k-sess", "worker:1", "session body", now); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	resp, err := http.Get(s.URL + "/memories")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	bs := string(body)
	iPerm := strings.Index(bs, "Permanent")
	iAlways := strings.Index(bs, "Always")
	iGlobal := strings.Index(bs, "Global")
	iOlder := strings.Index(bs, "Older Sessions")

	if iPerm < 0 || iAlways < 0 || iGlobal < 0 || iOlder < 0 {
		t.Fatalf("missing group heading(s): perm=%d always=%d global=%d older=%d", iPerm, iAlways, iGlobal, iOlder)
	}
	if !(iPerm < iAlways && iAlways < iGlobal && iGlobal < iOlder) {
		t.Errorf("group order wrong: perm=%d < always=%d < global=%d < older=%d",
			iPerm, iAlways, iGlobal, iOlder)
	}

	// Each seeded key must appear in the body.
	for _, k := range []string{"k-perm", "k-always", "k-global", "k-sess"} {
		if !strings.Contains(bs, k) {
			t.Errorf("rendered body missing memory key %q", k)
		}
	}
	if !strings.Contains(bs, "Total rows: 4") {
		t.Error("total count header not reflecting seeded row count")
	}
}

// TestMemories_UnknownScope_FallsToOther — a row with a scope the
// spec §6.1 doesn't define must still render under "Other" so a
// forward-compat scope value stays visible instead of silently
// dropping.
func TestMemories_UnknownScope_FallsToOther(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	if err := db.insertMemoryForTest("novel-scope", "k-novel", "system", "future body", time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := http.Get(s.URL + "/memories")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	bs := string(body)
	if !strings.Contains(bs, "Other") {
		t.Error("Other group heading not emitted for unknown scope")
	}
	if !strings.Contains(bs, "k-novel") {
		t.Error("novel-scope row missing from render")
	}
}

// TestMemories_TemplateAutoEscape — memory content is attacker-
// influenceable (workers write arbitrary bytes). Verify html/template
// escapes it rather than emitting raw script tags. A regression to
// text/template would make this test fail even though the handler
// still "works" for ASCII content.
func TestMemories_TemplateAutoEscape(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	if err := db.insertMemoryForTest("permanent", "k-xss",
		"<script>alert(1)</script>",
		"<script>alert(2)</script>", time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := http.Get(s.URL + "/memories")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	bs := string(body)
	if strings.Contains(bs, "<script>alert(1)</script>") {
		t.Error("raw <script> tag emitted for author — auto-escape broken")
	}
	if strings.Contains(bs, "<script>alert(2)</script>") {
		t.Error("raw <script> tag emitted for content — auto-escape broken")
	}
	if !strings.Contains(bs, "&lt;script&gt;") {
		t.Error("angle brackets not escaped in rendered output")
	}
}

// TestTruncateContent — rune-safe truncation contract. Multi-byte
// strings must not split mid-codepoint; the ellipsis must only
// appear when truncation happened.
func TestTruncateContent(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"truncate-me-please", 8, "truncate…"},
		{"中文字符测试abc", 4, "中文字符…"},
		{"anything", 0, ""},
	}
	for _, c := range cases {
		got := truncateContent(c.in, c.n)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestGroupMemories_EmptyGroupsRetained — an empty table still emits
// all four §6.1 group headings so the layout shape is stable
// across empty/populated states.
func TestGroupMemories_EmptyGroupsRetained(t *testing.T) {
	groups := groupMemories(nil)
	if len(groups) != 4 {
		t.Fatalf("len(groups)=%d, want 4", len(groups))
	}
	wantLabels := []string{"Permanent", "Always", "Global", "Older Sessions"}
	for i, g := range groups {
		if g.Label != wantLabels[i] {
			t.Errorf("group[%d].Label=%q, want %q", i, g.Label, wantLabels[i])
		}
		if len(g.Memories) != 0 {
			t.Errorf("group[%d] should be empty, got %d rows", i, len(g.Memories))
		}
	}
}

// newUIServerWithDB is a variant of newUIServer that lets a test
// supply its own DB handle so seeded rows remain visible to the
// handler. The default newUIServer builds its own DB, which would
// orphan any rows inserted through a caller-held handle.
func newUIServerWithDB(t *testing.T, db *DB) *httptest.Server {
	t.Helper()
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mountUI(mux, db)
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}
