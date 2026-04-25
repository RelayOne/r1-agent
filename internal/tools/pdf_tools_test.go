package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFReadNotAPDF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fake.pdf")
	os.WriteFile(p, []byte("this is not a pdf"), 0o600) //nolint:errcheck

	reg := NewRegistry(dir)
	_, err := reg.Handle(nil, "pdf_read", toJSON(map[string]string{"path": "fake.pdf"})) //nolint:staticcheck
	if err == nil {
		t.Error("pdf_read on non-PDF file should return error")
	}
	if !strings.Contains(err.Error(), "PDF") {
		t.Errorf("error should mention PDF, got: %v", err)
	}
}

func TestPDFReadMissingFile(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(nil, "pdf_read", toJSON(map[string]string{"path": "missing.pdf"})) //nolint:staticcheck
	if err == nil {
		t.Error("pdf_read on missing file should return error")
	}
}

func TestPDFReadPathConfinement(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(nil, "pdf_read", toJSON(map[string]string{"path": "../../etc/passwd"})) //nolint:staticcheck
	if err == nil {
		t.Error("pdf_read should reject paths escaping workDir")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error should mention escapes, got: %v", err)
	}
}

func TestPDFReadSimplePDF(t *testing.T) {
	// Minimal valid PDF with one BT...ET block containing a Tj string.
	// This tests the pure-Go stream extractor path.
	pdfContent := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n" +
		"stream\nBT\n/F1 12 Tf\n(Hello from R1) Tj\nET\nendstream\n%%EOF\n"

	dir := t.TempDir()
	p := filepath.Join(dir, "test.pdf")
	os.WriteFile(p, []byte(pdfContent), 0o600) //nolint:errcheck

	reg := NewRegistry(dir)
	result, err := reg.Handle(nil, "pdf_read", toJSON(map[string]string{"path": "test.pdf"})) //nolint:staticcheck
	if err != nil {
		t.Fatalf("pdf_read on valid PDF should not error: %v", err)
	}
	// The pure-Go extractor should find the text or return a "no extractable text" message.
	if result == "" {
		t.Error("pdf_read should return non-empty result")
	}
}

func TestPDFReadInvalidJSON(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(nil, "pdf_read", json.RawMessage(`{bad json`)) //nolint:staticcheck
	if err == nil {
		t.Error("pdf_read with invalid JSON should return error")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error should mention invalid input, got: %v", err)
	}
}

func TestParsePageRange(t *testing.T) {
	cases := []struct {
		input      string
		wantF      int
		wantL      int
		wantOK     bool
	}{
		{"1-5", 1, 5, true},
		{"3", 3, 3, true},
		{"10-20", 10, 20, true},
		{"", 0, 0, false},
		{"abc", 0, 0, false},
	}
	for _, c := range cases {
		f, l, ok := parsePageRange(c.input)
		if ok != c.wantOK {
			t.Errorf("parsePageRange(%q) ok=%v, want %v", c.input, ok, c.wantOK)
			continue
		}
		if ok && (f != c.wantF || l != c.wantL) {
			t.Errorf("parsePageRange(%q) = (%d,%d), want (%d,%d)", c.input, f, l, c.wantF, c.wantL)
		}
	}
}

func TestDecodeHexPDFString(t *testing.T) {
	cases := []struct {
		hex  string
		want string
		ok   bool
	}{
		{"48656c6c6f", "Hello", true},
		{"576f726c64", "World", true},
		{"zz", "", false}, // invalid hex
	}
	for _, c := range cases {
		got, ok := decodeHexPDFString(c.hex)
		if ok != c.ok {
			t.Errorf("decodeHexPDFString(%q) ok=%v, want %v", c.hex, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("decodeHexPDFString(%q) = %q, want %q", c.hex, got, c.want)
		}
	}
}

func TestDecodePDFString(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`hello`, "hello"},
		{`line\none`, "line\none"},
		{`tab\there`, "tab\there"},
		{`escape\\slash`, `escape\slash`},
		{`paren\)end`, "paren)end"},
	}
	for _, c := range cases {
		got := decodePDFString(c.input)
		if got != c.want {
			t.Errorf("decodePDFString(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
