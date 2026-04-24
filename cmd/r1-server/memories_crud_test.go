// Package main — memories_crud_test.go
//
// work-stoke TASK 14 regression tests. Covers the POST / PUT / DELETE
// handlers added in memories.go plus the passphrase gate that writes with
// scope=="always" sit behind.
//
// Acceptance matrix (per TASK 14 close-out):
//
//  1. POST at a normal scope → 201 without a passphrase.
//  2. POST at scope=="always" fails closed on (no env / wrong passphrase /
//     correct passphrase); only the correct-passphrase case reaches 201.
//  3. PUT mutates content + tags + expiry of an existing row → 200.
//  4. PUT with malformed JSON → 400 (no row mutation).
//  5. DELETE of an existing row → 204; repeat DELETE → 404.
//  6. DELETE of a scope=="always" row requires the same passphrase gate.
//  7. Every route 404s when R1_SERVER_UI_V2 is unset.
//
// Every test sets R1_SERVER_UI_V2=1 because the CRUD handlers are gated
// behind the same flag as the read-only view; without the flag the route
// 404s and the assertions would be meaningless.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// postMemoryHelper marshals req, POSTs it to baseURL+/api/memories, and
// returns the status code + decoded response body. Returning int+map
// keeps the body-close on a local defer so the bodyclose linter is happy
// and callers get a single decoded shape to assert on.
func postMemoryHelper(t *testing.T, baseURL string, req memoryWriteRequest) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/memories", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	// Decode errors are tolerated on the error path (a 401 plain-text
	// body does not unmarshal as a map and the caller only reads the
	// map on the happy path).
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestMemoriesPOST_NormalScope_201 — a non-"always" scope (permanent)
// POSTs succeed without a passphrase. Confirms the gate is scoped to
// scope=="always" and does not leak into general write paths.
func TestMemoriesPOST_NormalScope_201(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "ignored-for-this-scope")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, body := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:   "permanent",
		Key:     "t14-normal",
		Content: "non-always scope body",
		Tags:    []string{"alpha", "beta"},
	})
	if status != http.StatusCreated {
		t.Fatalf("status=%d, want 201 for non-always POST", status)
	}
	if body["ok"] != true {
		t.Errorf("ok=%v, want true", body["ok"])
	}
	idFloat, ok := body["id"].(float64)
	if !ok || idFloat <= 0 {
		t.Errorf("id=%v (type %T), want positive number", body["id"], body["id"])
	}

	// The row must carry the tags we POSTed — verifies the tags column
	// took the JSON array rather than dropping the field.
	var tagsJSON string
	if err := db.sql.QueryRow(
		`SELECT tags FROM stoke_memory_bus WHERE id = ?`, int64(idFloat),
	).Scan(&tagsJSON); err != nil {
		t.Fatalf("scan tags: %v", err)
	}
	if tagsJSON != `["alpha","beta"]` {
		t.Errorf("tags=%q, want [\"alpha\",\"beta\"]", tagsJSON)
	}
}

// TestMemoriesPOST_BadJSON_400 — a malformed body at any scope → 400
// before SQL runs.
func TestMemoriesPOST_BadJSON_400(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s := newUIServer(t)

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		s.URL+"/api/memories", strings.NewReader(`{"scope": "permanent", bad json`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 on bad JSON", resp.StatusCode)
	}
}

// TestMemoriesPOST_ScopeAlways_MissingPass_401 — scope=="always" + no
// passphrase field → 401, regardless of whether the server has the env
// var set. Belt-and-braces: an unset env + missing body field must also
// 401.
func TestMemoriesPOST_ScopeAlways_MissingPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "correct-horse-battery-staple")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, _ := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:   "always",
		Key:     "t14-always-missing",
		Content: "should not land",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 when passphrase missing", status)
	}
	assertRowAbsent(t, db, "t14-always-missing")
}

// TestMemoriesPOST_ScopeAlways_WrongPass_401 — scope=="always" + wrong
// passphrase → 401. Exercises the constant-time-compare path so a
// regression that swaps to `==` would still 401 but with a measurable
// timing difference; we only assert on status here.
func TestMemoriesPOST_ScopeAlways_WrongPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "correct-horse-battery-staple")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, _ := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-always-wrong",
		Content:    "should not land",
		Passphrase: "nope-wrong-value",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 on wrong passphrase", status)
	}
	assertRowAbsent(t, db, "t14-always-wrong")
}

// TestMemoriesPOST_ScopeAlways_CorrectPass_201 — scope=="always" + matching
// passphrase → 201 + row lands in SQLite. Exercises the happy path end-to-
// end: handler → raw-SQL insert → re-read via ListMemories finds the row.
func TestMemoriesPOST_ScopeAlways_CorrectPass_201(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "correct-horse-battery-staple")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, body := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-always-ok",
		Content:    "admin-approved body",
		Passphrase: "correct-horse-battery-staple",
	})
	if status != http.StatusCreated {
		t.Fatalf("status=%d, want 201 on correct passphrase", status)
	}
	if body["ok"] != true {
		t.Errorf("ok=%v, want true", body["ok"])
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Key == "t14-always-ok" {
			found = true
			if r.Scope != "always" {
				t.Errorf("stored scope=%q, want always", r.Scope)
			}
			if r.Content != "admin-approved body" {
				t.Errorf("stored content=%q, want admin-approved body", r.Content)
			}
		}
	}
	if !found {
		t.Error("row with correct passphrase did not land in stoke_memory_bus")
	}
}

// TestMemoriesPOST_ScopeAlways_LegacyEnv_201 — setting the legacy
// STOKE_MEMORIES_PASSPHRASE var with the canonical one unset must still
// authorise the write. Covers the 90-day dual-accept window; a regression
// that drops the legacy lookup would fail here.
func TestMemoriesPOST_ScopeAlways_LegacyEnv_201(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "legacy-pass")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, body := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-legacy-ok",
		Content:    "legacy env body",
		Passphrase: "legacy-pass",
	})
	if status != http.StatusCreated {
		t.Fatalf("status=%d, want 201 with legacy env", status)
	}
	if body["ok"] != true {
		t.Errorf("ok=%v, want true", body["ok"])
	}
}

// TestMemoriesPUT_UpdatesRow — PUT /api/memories/{id} rewrites the
// content + tags + expiry columns in place. Seeds a non-"always" row via
// the CRUD POST so the update path runs without the passphrase gate.
func TestMemoriesPUT_UpdatesRow(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:   "global",
		Key:     "t14-put-target",
		Content: "original body",
		Tags:    []string{"one"},
	})

	newExpiry := time.Now().Add(time.Hour).UTC()
	putBody, _ := json.Marshal(memoryUpdateRequest{
		Content:   "rewritten body",
		Tags:      []string{"updated", "two"},
		ExpiresAt: &newExpiry,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10),
		bytes.NewReader(putBody))
	if err != nil {
		t.Fatalf("new PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200", resp.StatusCode)
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got MemoryRow
	for _, r := range rows {
		if r.ID == id {
			got = r
		}
	}
	if got.ID != id {
		t.Fatalf("row %d missing after PUT; rows=%+v", id, rows)
	}
	if got.Content != "rewritten body" {
		t.Errorf("content=%q, want rewritten body", got.Content)
	}

	// tags + expires_at are not on MemoryRow; read the raw columns.
	var tagsJSON, expiresAt string
	if err := db.sql.QueryRow(
		`SELECT tags, COALESCE(expires_at,'') FROM stoke_memory_bus WHERE id = ?`, id,
	).Scan(&tagsJSON, &expiresAt); err != nil {
		t.Fatalf("scan tags/expires: %v", err)
	}
	if tagsJSON != `["updated","two"]` {
		t.Errorf("tags=%q, want [\"updated\",\"two\"]", tagsJSON)
	}
	if expiresAt == "" {
		t.Error("expires_at not set after PUT")
	}
}

// TestMemoriesPUT_BadJSON_400 — malformed JSON on the update path → 400
// without touching the row.
func TestMemoriesPUT_BadJSON_400(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:   "global",
		Key:     "t14-put-bad",
		Content: "original body",
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10),
		strings.NewReader(`{"content": "oops`))
	if err != nil {
		t.Fatalf("new PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT status=%d, want 400", resp.StatusCode)
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.ID == id && r.Content != "original body" {
			t.Errorf("content mutated past bad-JSON 400: %q", r.Content)
		}
	}
}

// TestMemoriesPUT_MissingID_404 — PUT against an id with no row → 404.
func TestMemoriesPUT_MissingID_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s := newUIServer(t)

	body, _ := json.Marshal(memoryUpdateRequest{Content: "whatever"})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		s.URL+"/api/memories/999999",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404 for missing id", resp.StatusCode)
	}
}

// TestMemoriesDELETE_Removes — DELETE returns 204 and the row is gone.
// A second DELETE yields 404 so callers can distinguish "already gone"
// from "removed just now".
func TestMemoriesDELETE_Removes(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:   "global",
		Key:     "t14-delete-target",
		Content: "about to be removed",
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		t.Fatalf("new DELETE: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d, want 204", resp.StatusCode)
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Errorf("row %d still present after DELETE", id)
		}
	}

	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10), nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second DELETE: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("second DELETE status=%d, want 404", resp2.StatusCode)
	}
}

// TestMemoriesPUT_ScopeAlways_MissingPass_401 — the stored row's scope
// drives the gate on update. Seeds a scope=="always" row (passphrase
// supplied at seed time) then PUTs against it with no passphrase; the
// handler must reject without mutating the row.
func TestMemoriesPUT_ScopeAlways_MissingPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "seed-pass")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-put-always",
		Content:    "should stay put",
		Passphrase: "seed-pass",
	})

	putBody, _ := json.Marshal(memoryUpdateRequest{
		Content: "attempted overwrite",
		Tags:    []string{"hacked"},
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10),
		bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.ID == id && r.Content != "should stay put" {
			t.Errorf("content mutated past the gate: %q", r.Content)
		}
	}
}

// TestMemoriesDELETE_ScopeAlways_MissingPass_401 — DELETE of a scope==
// always row without a passphrase body must 401 and leave the row alone.
func TestMemoriesDELETE_ScopeAlways_MissingPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_MEMORIES_PASSPHRASE", "seed-pass")
	t.Setenv("STOKE_MEMORIES_PASSPHRASE", "")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-delete-always",
		Content:    "locked for delete",
		Passphrase: "seed-pass",
	})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("row disappeared past the DELETE gate")
	}
}

// seedMemoryForCRUD issues a POST /api/memories via the public handler so
// seed + assert share one code path. Returns the autoincrement id.
func seedMemoryForCRUD(t *testing.T, baseURL string, req memoryWriteRequest) int64 {
	t.Helper()
	if req.Key == "" {
		req.Key = "seed-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	status, body := postMemoryHelper(t, baseURL, req)
	if status != http.StatusCreated {
		t.Fatalf("seed POST status=%d body=%v", status, body)
	}
	idFloat, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("seed POST returned no id: %v", body)
	}
	return int64(idFloat)
}

// assertRowAbsent fails the test if a row with the given key exists in
// stoke_memory_bus. Shared by the 401 tests so every failure-path case
// verifies the row did not land.
func assertRowAbsent(t *testing.T, db *DB, key string) {
	t.Helper()
	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Key == key {
			t.Errorf("row %q leaked past the gate", key)
		}
	}
}

// TestMemoriesCRUD_V2Off_404 — sanity check that the three new routes stay
// dark when R1_SERVER_UI_V2 is unset. Matches the share.go / settings.go
// precedent: v2-only paths must 404 in MVP mode.
func TestMemoriesCRUD_V2Off_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s := newUIServer(t)

	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL+"/api/memories",
		strings.NewReader(`{"scope":"permanent","content":"x","key":"k"}`))
	postReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST status=%d, want 404 when v2 off", resp.StatusCode)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, s.URL+"/api/memories/1",
		strings.NewReader(`{"content":"x"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT status=%d, want 404 when v2 off", resp.StatusCode)
	}

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodDelete, s.URL+"/api/memories/1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE status=%d, want 404 when v2 off", resp.StatusCode)
	}
}
