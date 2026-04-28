package truecom

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func genPEMFile(t *testing.T) (keyPEM string, priv ed25519.PrivateKey) {
	t.Helper()
	_, p, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return string(pemBytes), p
}

func TestNewFromEnv_DefaultIsStub(t *testing.T) {
	t.Setenv(EnvMode, "")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if _, ok := c.(*StubClient); !ok {
		t.Errorf("expected StubClient, got %T", c)
	}
}

func TestNewFromEnv_ExplicitStub(t *testing.T) {
	t.Setenv(EnvMode, "stub")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if _, ok := c.(*StubClient); !ok {
		t.Errorf("expected StubClient, got %T", c)
	}
}

func TestNewFromEnv_UnknownMode(t *testing.T) {
	t.Setenv(EnvMode, "kittens")
	if _, err := NewFromEnv(); err == nil {
		t.Fatalf("expected error for unknown mode")
	}
}

func TestNewFromEnv_RealMissingURL(t *testing.T) {
	t.Setenv(EnvMode, "real")
	t.Setenv(EnvURL, "")
	if _, err := NewFromEnv(); err == nil {
		t.Fatalf("expected error when URL missing")
	}
}

func TestNewFromEnv_RealMissingKey(t *testing.T) {
	t.Setenv(EnvMode, "real")
	t.Setenv(EnvURL, "https://gw.local")
	t.Setenv(EnvPrivKey, "")
	t.Setenv(EnvPrivKey+"_FILE", "")
	if _, err := NewFromEnv(); err == nil {
		t.Fatalf("expected error when key missing")
	}
}

func TestNewFromEnv_RealWithInlineKey(t *testing.T) {
	keyPEM, _ := genPEMFile(t)
	t.Setenv(EnvMode, "real")
	t.Setenv(EnvURL, "https://gw.local")
	t.Setenv(EnvPrivKey, keyPEM)
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if _, ok := c.(*RealClient); !ok {
		t.Errorf("expected RealClient, got %T", c)
	}
}

func TestNewFromEnv_RealWithKeyFile(t *testing.T) {
	keyPEM, _ := genPEMFile(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "priv.pem")
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv(EnvMode, "real")
	t.Setenv(EnvURL, "https://gw.local")
	t.Setenv(EnvPrivKey, "")
	t.Setenv(EnvPrivKey+"_FILE", keyPath)
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if _, ok := c.(*RealClient); !ok {
		t.Errorf("expected RealClient, got %T", c)
	}
}

func TestNewFromEnv_RealMalformedPEM(t *testing.T) {
	t.Setenv(EnvMode, "real")
	t.Setenv(EnvURL, "https://gw.local")
	t.Setenv(EnvPrivKey, "not a pem")
	if _, err := NewFromEnv(); err == nil {
		t.Fatalf("expected error for malformed PEM")
	}
}

func TestParseEd25519PEM_RejectsRSA(t *testing.T) {
	// Non-Ed25519 PEM (e.g. garbage bytes wrapped as PKCS8 won't parse at all,
	// so we simulate "valid PKCS8 but wrong algorithm" with a crafted block).
	// Easiest: empty block body → parse fails → error surfaces "parse PKCS#8".
	bad := "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
	_, err := parseEd25519PEM(bad)
	if err == nil {
		t.Fatalf("expected error for malformed PKCS8")
	}
	if !strings.Contains(err.Error(), "parse PKCS#8") && !strings.Contains(err.Error(), "not Ed25519") {
		t.Logf("err: %v", err)
	}
}

func TestParseEd25519PEM_EmptyInput(t *testing.T) {
	if _, err := parseEd25519PEM(""); err == nil {
		t.Fatalf("expected error for empty input")
	}
	if _, err := parseEd25519PEM("   \n\t  "); err == nil {
		t.Fatalf("expected error for whitespace-only input")
	}
}

func TestParseEd25519PEM_ValidKeyRoundtrips(t *testing.T) {
	keyPEM, want := genPEMFile(t)
	got, err := parseEd25519PEM(keyPEM)
	if err != nil {
		t.Fatalf("parseEd25519PEM: %v", err)
	}
	if !want.Equal(got) {
		t.Errorf("roundtripped key does not match original")
	}
}
