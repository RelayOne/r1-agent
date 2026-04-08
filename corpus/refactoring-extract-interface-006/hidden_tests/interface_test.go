package main

import (
	"os"
	"strings"
	"testing"
)

// TestStoreInterfaceExists verifies that a Store interface is defined in the source.
func TestStoreInterfaceExists(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal("cannot read main.go:", err)
	}
	src := string(data)

	// Check for interface definition (case-insensitive on the name, but "Store" is expected).
	if !strings.Contains(src, "interface") {
		t.Fatal("expected a Store interface to be defined in main.go")
	}

	// Check the interface has Save and Load methods.
	if !strings.Contains(src, "Save(") {
		t.Fatal("Store interface should declare a Save method")
	}
	if !strings.Contains(src, "Load(") {
		t.Fatal("Store interface should declare a Load method")
	}
}

// TestFileStoreSatisfiesInterface verifies FileStore can be used as a Store.
func TestFileStoreSatisfiesInterface(t *testing.T) {
	var s Store = NewFileStore()
	if err := s.Save("iface-test", []byte("data")); err != nil {
		t.Fatalf("FileStore via Store interface Save failed: %v", err)
	}
	got, err := s.Load("iface-test")
	if err != nil {
		t.Fatalf("FileStore via Store interface Load failed: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("expected %q, got %q", "data", string(got))
	}
}

// TestDBStoreSatisfiesInterface verifies DBStore can be used as a Store.
func TestDBStoreSatisfiesInterface(t *testing.T) {
	var s Store = NewDBStore()
	if err := s.Save("iface-test", []byte("data")); err != nil {
		t.Fatalf("DBStore via Store interface Save failed: %v", err)
	}
	got, err := s.Load("iface-test")
	if err != nil {
		t.Fatalf("DBStore via Store interface Load failed: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("expected %q, got %q", "data", string(got))
	}
}

// TestCopyDataFunction verifies the CopyData function works across store types.
func TestCopyDataFunction(t *testing.T) {
	src := NewFileStore()
	dst := NewDBStore()

	src.Save("transfer", []byte("payload-123"))

	if err := CopyData(src, dst, "transfer"); err != nil {
		t.Fatalf("CopyData failed: %v", err)
	}

	got, err := dst.Load("transfer")
	if err != nil {
		t.Fatalf("destination Load after CopyData failed: %v", err)
	}
	if string(got) != "payload-123" {
		t.Fatalf("expected %q, got %q", "payload-123", string(got))
	}
}

// TestCopyDataReverse verifies CopyData from DBStore to FileStore.
func TestCopyDataReverse(t *testing.T) {
	src := NewDBStore()
	dst := NewFileStore()

	src.Save("reverse", []byte("reverse-data"))

	if err := CopyData(src, dst, "reverse"); err != nil {
		t.Fatalf("CopyData failed: %v", err)
	}

	got, err := dst.Load("reverse")
	if err != nil {
		t.Fatalf("destination Load after CopyData failed: %v", err)
	}
	if string(got) != "reverse-data" {
		t.Fatalf("expected %q, got %q", "reverse-data", string(got))
	}
}
