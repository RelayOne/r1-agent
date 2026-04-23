// Package main — memories_crud_test.go
//
// work-stoke TASK 14 regression tests. Covers the POST/PUT/DELETE
// handlers added in memories.go plus the X-R1-Admin-Pass gate that
// writes scope=="always" sit behind.
//
// Tests are organised around the TASK 14 acceptance matrix:
//
//  1. Normal-scope POST succeeds without a header.
//  2. scope=="always" POST is fail-closed on (missing header / wrong
//     header / correct header); only the correct-header case yields a
//     2xx status.
//  3. PUT mutates content + memory_type of an existing row.
//  4. DELETE returns 204 and leaves the row gone.
//
// Every test sets R1_SERVER_UI_V2=1 because the CRUD handlers are
// gated behind the same flag as the existing read-only view; without
// the flag the route 404s and the assertions would be meaningless.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// postMemoryHelper wraps the JSON-encode + POST + status assertion
// into one call so each acceptance-matrix case stays a short, readable
// sequence. Returns the status code + decoded response body as a map
// so callers can fish out the autoincrement id. The passphrase
// argument is an empty string for "no header set" (matches the 401
// case); any non-empty value sets the X-R1-Admin-Pass header verbatim.
//
// Returning int+map (rather than *http.Response) means bodyclose sees
// resp.Body.Close via defer in this function — satisfies the linter
// and also guarantees the body is closed synchronously before the
// helper returns, not on a t.Cleanup tick that may run during a
// later test if the goroutine is slow.
func postMemoryHelper(t *testing.T, baseURL string, req memoryWriteRequest, pass string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/memories", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if pass != "" {
		httpReq.Header.Set(adminPassHeader, pass)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	// Empty / non-JSON error bodies are fine — the caller asserts on
	// status first, then inspects the decoded body only on the happy
	// path. We swallow decode errors on the error path so a 401 with a
	// plain-text body doesn't fail the test before the status check.
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestMemoriesPOST_NormalScope_200 — a non-"always" scope (permanent)
// posts succeed without any admin header. Confirms the gate is scoped
// to scope=="always" and doesn't leak into general write paths.
func TestMemoriesPOST_NormalScope_200(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_ADMIN_PASS", "ignored-for-this-scope")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, body := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:      "permanent",
		Key:        "t14-normal",
		Content:    "non-always scope body",
		MemoryType: "semantic",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200 for non-always scope POST", status)
	}
	if body["ok"] != true {
		t.Errorf("ok=%v, want true", body["ok"])
	}
	// ID must be a positive integer so the subsequent PUT/DELETE tests
	// can address the row. JSON numbers decode to float64 by default.
	idFloat, ok := body["id"].(float64)
	if !ok || idFloat <= 0 {
		t.Errorf("id=%v (type %T), want positive number", body["id"], body["id"])
	}
}

// TestMemoriesPOST_ScopeAlways_MissingPass_401 — scope=="always" + no
// header → 401. The passphrase env var being set matters: the handler
// must fail closed on a missing header regardless of server config.
func TestMemoriesPOST_ScopeAlways_MissingPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_ADMIN_PASS", "correct-horse-battery-staple")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, _ := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:   "always",
		Key:     "t14-always-missing",
		Content: "should not land",
	}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 when X-R1-Admin-Pass missing", status)
	}

	// Row must not have landed in SQLite.
	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Key == "t14-always-missing" {
			t.Errorf("row %q leaked past the gate", r.Key)
		}
	}
}

// TestMemoriesPOST_ScopeAlways_WrongPass_401 — scope=="always" + wrong
// header → 401. Covers the case where an attacker supplies an
// arbitrary non-empty header hoping timing differs from the
// missing-header path. The comparison uses constant-time compare, so
// both paths must land on the same 401 outcome.
func TestMemoriesPOST_ScopeAlways_WrongPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_ADMIN_PASS", "correct-horse-battery-staple")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, _ := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:   "always",
		Key:     "t14-always-wrong",
		Content: "should not land",
	}, "nope-wrong-value")
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 on wrong passphrase", status)
	}

	rows, err := db.ListMemories(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Key == "t14-always-wrong" {
			t.Errorf("row %q leaked past the gate", r.Key)
		}
	}
}

// TestMemoriesPOST_ScopeAlways_CorrectPass_200 — scope=="always" +
// matching X-R1-Admin-Pass → 200 + row lands in SQLite. Exercises the
// happy path end-to-end: handler → raw-SQL insert → re-read via
// ListMemories finds the row.
func TestMemoriesPOST_ScopeAlways_CorrectPass_200(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_ADMIN_PASS", "correct-horse-battery-staple")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	status, body := postMemoryHelper(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-always-ok",
		Content:    "admin-approved body",
		MemoryType: "episodic",
	}, "correct-horse-battery-staple")
	if status != http.StatusOK {
		t.Fatalf("status=%d, want 200 on correct passphrase", status)
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

// TestMemoriesPUT_UpdatesRow — PUT /api/memories/{id} rewrites the
// content + memory_type columns in place. We seed a non-"always" row
// so the passphrase gate stays out of scope, issue the PUT, and
// re-fetch via ListMemories to confirm the content mutated.
func TestMemoriesPUT_UpdatesRow(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	// Seed the row directly via the raw-SQL helper + the CRUD helper
	// so we get back the autoincrement id. insertMemoryForTest doesn't
	// surface the id, so we use the CRUD POST to seed + learn the id
	// in one round trip.
	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:      "global",
		Key:        "t14-put-target",
		Content:    "original body",
		MemoryType: "initial",
	}, "")

	putBody, _ := json.Marshal(memoryUpdateRequest{
		Content:    "rewritten body",
		MemoryType: "semantic",
	})
	req, err := http.NewRequest(http.MethodPut,
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
	// memory_type is not on MemoryRow, so check the SQL directly.
	var memType string
	if err := db.sql.QueryRow(
		`SELECT memory_type FROM stoke_memory_bus WHERE id = ?`, id,
	).Scan(&memType); err != nil {
		t.Fatalf("scan memory_type: %v", err)
	}
	if memType != "semantic" {
		t.Errorf("memory_type=%q, want semantic", memType)
	}
}

// TestMemoriesDELETE_Removes — DELETE /api/memories/{id} must return
// 204 No Content and leave zero rows with that id behind. A second
// DELETE for the same id must yield 404 so callers can tell "already
// gone" from "removed just now".
func TestMemoriesDELETE_Removes(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:      "global",
		Key:        "t14-delete-target",
		Content:    "about to be removed",
		MemoryType: "ephemeral",
	}, "")

	// First DELETE — 204 + row disappears.
	req, err := http.NewRequest(http.MethodDelete,
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

	// Second DELETE — 404 because the row is already gone. Proves the
	// handler distinguishes "already removed" from a successful
	// removal rather than silently returning 204 for both.
	req2, _ := http.NewRequest(http.MethodDelete,
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

// TestMemoriesPUT_ScopeAlways_MissingPass_401 — guard against a
// regression where the update path forgets to re-check the passphrase
// against the stored row's scope. We seed a scope=="always" row
// (passphrase + header supplied at seed time) then PUT against it with
// no header; the handler must reject without touching the row.
func TestMemoriesPUT_ScopeAlways_MissingPass_401(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_ADMIN_PASS", "seed-pass")
	db := newTestDB(t)
	s := newUIServerWithDB(t, db)

	id := seedMemoryForCRUD(t, s.URL, memoryWriteRequest{
		Scope:      "always",
		Key:        "t14-put-always",
		Content:    "should stay put",
		MemoryType: "locked",
	}, "seed-pass")

	putBody, _ := json.Marshal(memoryUpdateRequest{
		Content:    "attempted overwrite",
		MemoryType: "hacked",
	})
	req, _ := http.NewRequest(http.MethodPut,
		s.URL+"/api/memories/"+strconv.FormatInt(id, 10),
		bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	// No X-R1-Admin-Pass header — must be rejected.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}

	// Row content must be unchanged.
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

// seedMemoryForCRUD is a test helper: issues a POST /api/memories via
// the public handler so it exercises the same path production writes
// use, then returns the autoincrement id the handler reports. Using
// the real handler (not insertMemoryForTest) means seed + assert
// share one code path and any regression in INSERT semantics fails
// every dependent test rather than hiding behind a mocked helper.
func seedMemoryForCRUD(t *testing.T, baseURL string, req memoryWriteRequest, pass string) int64 {
	t.Helper()
	// Give each call a unique Key when the caller forgot to set one so
	// repeated seeds within one test don't collide on the UNIQUE
	// (scope, scope_target, key) constraint. The timestamp suffix is
	// unique to nanosecond resolution which is comfortably below the
	// test throughput.
	if req.Key == "" {
		req.Key = "seed-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	status, body := postMemoryHelper(t, baseURL, req, pass)
	if status != http.StatusOK {
		// Surface the body for debugging — the helper already closed
		// resp.Body but saved the decoded map.
		t.Fatalf("seed POST status=%d body=%v", status, body)
	}
	idFloat, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("seed POST returned no id: %v", body)
	}
	return int64(idFloat)
}

// TestMemoriesCRUD_V2Off_404 — sanity check that the three new routes
// stay dark when R1_SERVER_UI_V2 is unset. Matches the share.go and
// settings.go precedent: v2-only paths must 404 in MVP mode so
// unauthenticated scanners can't enumerate them.
func TestMemoriesCRUD_V2Off_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s := newUIServer(t)

	// POST
	resp, err := http.Post(s.URL+"/api/memories", "application/json",
		strings.NewReader(`{"scope":"permanent","content":"x","key":"k"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST status=%d, want 404 when v2 off", resp.StatusCode)
	}

	// PUT
	req, _ := http.NewRequest(http.MethodPut, s.URL+"/api/memories/1",
		strings.NewReader(`{"content":"x"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT status=%d, want 404 when v2 off", resp.StatusCode)
	}

	// DELETE
	req, _ = http.NewRequest(http.MethodDelete, s.URL+"/api/memories/1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE status=%d, want 404 when v2 off", resp.StatusCode)
	}
}
