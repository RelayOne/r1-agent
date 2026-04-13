package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactCoversKnownShapes(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		mustStrip []string
		mustKeep  []string
	}{
		{
			name:      "anthropic key in argv",
			input:     "stoke --native-api-key sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			mustStrip: []string{"sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			mustKeep:  []string{"--native-api-key", "REDACTED"},
		},
		{
			name:      "litellm master key",
			input:     "LITELLM_MASTER_KEY=sk-litellm-local-4d22e2193a25a6b86467dec34ef20043 other=ok",
			mustStrip: []string{"4d22e2193a25a6b86467dec34ef20043"},
			mustKeep:  []string{"LITELLM_MASTER_KEY", "other=ok"},
		},
		{
			name:      "github token in env assignment",
			input:     `GITHUB_TOKEN="ghp_abcdefghijklmnopqrstuvwxyz0123456789"`,
			mustStrip: []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
			mustKeep:  []string{"GITHUB_TOKEN"},
		},
		{
			name:      "bearer header",
			input:     "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aaaaa.bbbbb",
			mustStrip: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aaaaa.bbbbb"},
			mustKeep:  []string{"Authorization", "Bearer"},
		},
		{
			name:      "db url with password",
			input:     "postgres://stoke:s3cretpassword@db.example.com:5432/app",
			mustStrip: []string{"s3cretpassword"},
			mustKeep:  []string{"postgres://stoke:", "@db.example.com:5432/app"},
		},
		{
			name:      "pem private key block",
			input:     "prefix\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----\nsuffix",
			mustStrip: []string{"MIIEow"},
			mustKeep:  []string{"prefix", "suffix", "REDACTED-PRIVATE-KEY"},
		},
		{
			name:      "no secret passes through unchanged",
			input:     "running session S1 — 3 tasks, 12 ACs, cost_usd=0.42",
			mustStrip: nil,
			mustKeep:  []string{"running session S1", "cost_usd=0.42"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Redact(tc.input)
			for _, needle := range tc.mustStrip {
				if strings.Contains(out, needle) {
					t.Fatalf("expected %q to be stripped, output was:\n%s", needle, out)
				}
			}
			for _, needle := range tc.mustKeep {
				if !strings.Contains(out, needle) {
					t.Fatalf("expected %q to remain, output was:\n%s", needle, out)
				}
			}
		})
	}
}

func TestRedactingWriterStripsSecrets(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	payload := []byte(`{"level":"info","msg":"starting","api_key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"}`)
	n, err := w.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(payload) {
		t.Fatalf("writer must report original length, got %d want %d", n, len(payload))
	}
	got := buf.String()
	if strings.Contains(got, "sk-ant-api03-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("secret leaked through writer: %s", got)
	}
	if !strings.Contains(got, "starting") {
		t.Fatalf("non-secret content dropped: %s", got)
	}
}

func TestFastPathReturnsOriginalSlice(t *testing.T) {
	in := []byte("2026-04-13 12:00:00 INFO session=S1 task=T3 ok benign output")
	out := RedactBytes(in)
	if &out[0] != &in[0] {
		t.Fatalf("fast path should return original slice without reallocation")
	}
}
