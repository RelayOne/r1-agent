package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users (id, name, email) VALUES (1, 'alice', 'alice@example.com')`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGetUserHandler_ValidUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	DB = db

	req := httptest.NewRequest("GET", "/api/user?name=alice", nil)
	rec := httptest.NewRecorder()

	GetUserHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetUserHandler_MissingParam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	DB = db

	req := httptest.NewRequest("GET", "/api/user", nil)
	rec := httptest.NewRecorder()

	GetUserHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
