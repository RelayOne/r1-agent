package main

import (
	"testing"
)

func TestFileStore_SaveAndLoad(t *testing.T) {
	fs := NewFileStore()

	if err := fs.Save("key1", []byte("value1")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := fs.Load("key1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(data) != "value1" {
		t.Fatalf("expected %q, got %q", "value1", string(data))
	}
}

func TestDBStore_SaveAndLoad(t *testing.T) {
	ds := NewDBStore()

	if err := ds.Save("key1", []byte("value1")); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := ds.Load("key1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(data) != "value1" {
		t.Fatalf("expected %q, got %q", "value1", string(data))
	}
}

func TestFileStore_EmptyKey(t *testing.T) {
	fs := NewFileStore()
	if err := fs.Save("", []byte("data")); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestDBStore_NotFound(t *testing.T) {
	ds := NewDBStore()
	_, err := ds.Load("missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}
