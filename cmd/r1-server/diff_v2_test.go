// Package main — diff_v2_test.go
//
// Spec 4 §10 T9 — exercises the v2 template path of serveDiff
// (gated by R1_SERVER_UI_V2=1). The legacy fallback continues to
// be covered by TestServeDiff_HTMLDefault in diff_test.go.
package main

import (
	"io"
	"strings"
	"testing"
)

func TestServeDiff_HTMLUsesV2Template(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")

	db := newTestDB(t)
	seedDiffSession(t, db, "alpha", []eventSeed{{"task.start", `{"id":"t1"}`}})
	seedDiffSession(t, db, "beta", nil)

	srv := newDiffTestServer(t, db)
	resp, err := srv.Client().Get(srv.URL + "/diff/alpha/beta")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	for _, frag := range []string{
		`data-testid="diff-view"`,
		`data-testid="diff-table"`,
		`data-testid="diff-row"`,
		`Diff <code>alpha</code> vs <code>beta</code>`,
		`Content-diff`,
		// base.html shell signals
		`/ui/web/vendor/htmx.min.js`,
		`<script type="importmap">`,
	} {
		if !strings.Contains(bs, frag) {
			t.Errorf("v2 diff body missing %q", frag)
		}
	}
}
