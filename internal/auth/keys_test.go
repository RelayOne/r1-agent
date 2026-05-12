package auth

// keys_test.go — coverage for the KeyMaterial loader cascade.
//
// FileSource generates a fresh RSA-2048 keypair on first use,
// persists it with 0600/0644 mode discipline, and re-reads it on
// subsequent calls. EnvSource flips between HS256 and RS256 based on
// which env vars are present. The cascade walks sources in priority
// order; ErrUnsupported / ErrNoKeyMaterial fall through.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvSource_HS256(t *testing.T) {
	env := map[string]string{
		"AUTH_JWT_SECRET": strings.Repeat("k", 40),
	}
	src := EnvSource{Getenv: func(k string) string { return env[k] }}
	mat, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mat.Algorithm != "HS256" {
		t.Errorf("Algorithm = %q, want HS256", mat.Algorithm)
	}
	if len(mat.HMACSecret) != 40 {
		t.Errorf("HMACSecret len = %d, want 40", len(mat.HMACSecret))
	}
	if mat.KID == "" {
		t.Error("KID empty")
	}
}

func TestEnvSource_HS256_TooShort(t *testing.T) {
	env := map[string]string{"AUTH_JWT_SECRET": "short"}
	src := EnvSource{Getenv: func(k string) string { return env[k] }}
	_, err := src.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AUTH_JWT_SECRET") {
		t.Errorf("want >=32 chars error, got %v", err)
	}
}

func TestEnvSource_RS256(t *testing.T) {
	privPEM, pubPEM := genRSAKeyPEMs(t)
	env := map[string]string{
		"AUTH_JWT_PRIVATE_KEY": string(privPEM),
		"AUTH_JWT_PUBLIC_KEY":  string(pubPEM),
	}
	src := EnvSource{Getenv: func(k string) string { return env[k] }}
	mat, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mat.Algorithm != "RS256" {
		t.Errorf("Algorithm = %q, want RS256", mat.Algorithm)
	}
	if mat.RSAPrivate == nil || mat.RSAPublic == nil {
		t.Error("RSA keys not set")
	}
	if mat.KID == "" {
		t.Error("KID empty")
	}
}

func TestEnvSource_RS256_MissingPrivate(t *testing.T) {
	_, pubPEM := genRSAKeyPEMs(t)
	env := map[string]string{"AUTH_JWT_PUBLIC_KEY": string(pubPEM)}
	src := EnvSource{Getenv: func(k string) string { return env[k] }}
	_, err := src.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AUTH_JWT_PRIVATE_KEY") {
		t.Errorf("want AUTH_JWT_PRIVATE_KEY error, got %v", err)
	}
}

func TestEnvSource_NilGetenv(t *testing.T) {
	src := EnvSource{}
	_, err := src.Load(context.Background())
	if !errors.Is(err, ErrNoKeyMaterial) {
		t.Errorf("want ErrNoKeyMaterial, got %v", err)
	}
}

func TestEnvSource_Empty(t *testing.T) {
	src := EnvSource{Getenv: func(string) string { return "" }}
	_, err := src.Load(context.Background())
	if !errors.Is(err, ErrNoKeyMaterial) {
		t.Errorf("want ErrNoKeyMaterial, got %v", err)
	}
}

func TestFileSource_FirstUseGeneratesKeypair(t *testing.T) {
	dir := t.TempDir()
	src := FileSource{Dir: dir}
	mat, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mat.Algorithm != "RS256" {
		t.Errorf("Algorithm = %q, want RS256", mat.Algorithm)
	}
	if mat.RSAPrivate == nil || mat.RSAPublic == nil {
		t.Fatal("RSA keys not generated")
	}
	// Files persisted with correct modes.
	privPath := filepath.Join(dir, "jwt-priv.pem")
	pubPath := filepath.Join(dir, "jwt-pub.pem")
	if info, err := os.Stat(privPath); err != nil {
		t.Errorf("stat priv: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("priv mode = %o, want 0600", info.Mode().Perm())
	}
	if info, err := os.Stat(pubPath); err != nil {
		t.Errorf("stat pub: %v", err)
	} else if info.Mode().Perm() != 0o644 {
		t.Errorf("pub mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestFileSource_ReloadsExistingKeypair(t *testing.T) {
	dir := t.TempDir()
	src := FileSource{Dir: dir}
	first, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	// Same modulus = same key persisted across calls.
	if first.RSAPrivate.N.Cmp(second.RSAPrivate.N) != 0 {
		t.Error("FileSource generated a new key on reload (should re-read persisted key)")
	}
	if first.KID != second.KID {
		t.Errorf("KID changed: %q != %q", first.KID, second.KID)
	}
}

func TestFileSource_HS256SecretFile(t *testing.T) {
	dir := t.TempDir()
	secret := strings.Repeat("z", 40)
	// Add a trailing newline to verify trimming.
	if err := os.WriteFile(filepath.Join(dir, "jwt-secret"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	src := FileSource{Dir: dir}
	mat, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mat.Algorithm != "HS256" {
		t.Errorf("Algorithm = %q, want HS256", mat.Algorithm)
	}
	if string(mat.HMACSecret) != secret {
		t.Errorf("HMACSecret = %q, want %q (newline should be trimmed)", mat.HMACSecret, secret)
	}
}

func TestFileSource_RS256WinsOverHS256(t *testing.T) {
	dir := t.TempDir()
	privPEM, pubPEM := genRSAKeyPEMs(t)
	if err := os.WriteFile(filepath.Join(dir, "jwt-priv.pem"), privPEM, 0o600); err != nil {
		t.Fatalf("write priv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwt-pub.pem"), pubPEM, 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwt-secret"), []byte(strings.Repeat("h", 40)), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	src := FileSource{Dir: dir}
	mat, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mat.Algorithm != "RS256" {
		t.Errorf("Algorithm = %q, want RS256 (RSA files should win)", mat.Algorithm)
	}
}

func TestFileSource_PartialFilesRefused(t *testing.T) {
	dir := t.TempDir()
	privPEM, _ := genRSAKeyPEMs(t)
	if err := os.WriteFile(filepath.Join(dir, "jwt-priv.pem"), privPEM, 0o600); err != nil {
		t.Fatalf("write priv: %v", err)
	}
	src := FileSource{Dir: dir}
	_, err := src.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "partial key material") {
		t.Errorf("want partial-material error, got %v", err)
	}
}

func TestFileSource_EmptyDir(t *testing.T) {
	src := FileSource{Dir: ""}
	_, err := src.Load(context.Background())
	if !errors.Is(err, ErrNoKeyMaterial) {
		t.Errorf("want ErrNoKeyMaterial, got %v", err)
	}
}

func TestSecretManagerSource_Unsupported(t *testing.T) {
	src := SecretManagerSource{}
	_, err := src.Load(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
	if src.Name() != "secret-manager" {
		t.Errorf("Name = %q", src.Name())
	}
}

func TestLoadKeyMaterial_FallsThroughUnsupported(t *testing.T) {
	dir := t.TempDir()
	mat, err := LoadKeyMaterial(context.Background(),
		SecretManagerSource{},               // returns ErrUnsupported
		EnvSource{Getenv: func(string) string { return "" }}, // returns ErrNoKeyMaterial
		FileSource{Dir: dir},                // generates new keypair
	)
	if err != nil {
		t.Fatalf("LoadKeyMaterial: %v", err)
	}
	if mat == nil || mat.Algorithm != "RS256" {
		t.Errorf("unexpected mat: %+v", mat)
	}
}

func TestLoadKeyMaterial_NoSources(t *testing.T) {
	_, err := LoadKeyMaterial(context.Background())
	if err == nil {
		t.Error("want error from empty sources")
	}
}

func TestLoadKeyMaterial_AllExhausted(t *testing.T) {
	_, err := LoadKeyMaterial(context.Background(),
		EnvSource{Getenv: func(string) string { return "" }},
	)
	if !errors.Is(err, ErrNoKeyMaterial) {
		t.Errorf("want ErrNoKeyMaterial, got %v", err)
	}
}

func TestDefaultKeySources_HasThreeSources(t *testing.T) {
	t.Setenv("R1_HOME", t.TempDir())
	srcs := DefaultKeySources(context.Background())
	if len(srcs) != 3 {
		t.Errorf("len = %d, want 3", len(srcs))
	}
	names := []string{srcs[0].Name(), srcs[1].Name(), srcs[2].Name()}
	want := []string{"secret-manager", "env", "file"}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("source[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestJwtServiceFromKeyMaterial_HS256(t *testing.T) {
	mat := &KeyMaterial{
		Algorithm:  "HS256",
		HMACSecret: []byte(strings.Repeat("k", 32)),
		KID:        "kid-1",
	}
	svc, err := JwtServiceFromKeyMaterial("iss", "aud", mat, 0, 0)
	if err != nil {
		t.Fatalf("JwtServiceFromKeyMaterial: %v", err)
	}
	if svc.Algorithm() != "HS256" {
		t.Errorf("Algorithm = %q", svc.Algorithm())
	}
	if svc.KID() != "kid-1" {
		t.Errorf("KID = %q", svc.KID())
	}
}

func TestJwtServiceFromKeyMaterial_RS256(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	mat := &KeyMaterial{
		Algorithm:  "RS256",
		RSAPrivate: priv,
		RSAPublic:  &priv.PublicKey,
		KID:        "kid-rs",
	}
	svc, err := JwtServiceFromKeyMaterial("iss", "aud", mat, 0, 0)
	if err != nil {
		t.Fatalf("JwtServiceFromKeyMaterial: %v", err)
	}
	if svc.Algorithm() != "RS256" {
		t.Errorf("Algorithm = %q", svc.Algorithm())
	}
	// Verify the service can round-trip with the loaded key material.
	tok, err := svc.Sign(map[string]any{"x": "y"}, SignOptions{Subject: "u"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	v, err := svc.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Payload["x"] != "y" {
		t.Errorf("payload[x] = %v", v.Payload["x"])
	}
}

func TestJwtServiceFromKeyMaterial_NilMaterial(t *testing.T) {
	_, err := JwtServiceFromKeyMaterial("iss", "aud", nil, 0, 0)
	if err == nil {
		t.Error("want error from nil material")
	}
}

func TestParseRSAKeyMaterial_MismatchedHalves(t *testing.T) {
	priv1PEM, _ := genRSAKeyPEMs(t)
	_, pub2PEM := genRSAKeyPEMs(t)
	_, err := parseRSAKeyMaterial(priv1PEM, pub2PEM)
	if err == nil || !strings.Contains(err.Error(), "moduli differ") {
		t.Errorf("want moduli-differ error, got %v", err)
	}
}

func TestParseRSAKeyMaterial_PKCS1Private(t *testing.T) {
	// Test the PKCS1 private-key parse path (older OpenSSL output).
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pkcs1DER := x509.MarshalPKCS1PrivateKey(priv)
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1DER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	mat, err := parseRSAKeyMaterial(privPEM, pubPEM)
	if err != nil {
		t.Fatalf("parseRSAKeyMaterial PKCS1: %v", err)
	}
	if mat.RSAPrivate == nil {
		t.Error("private key not parsed")
	}
}

// genRSAKeyPEMs is a test helper that returns a fresh PKCS8 private +
// SPKI public pair as PEM bytes. The pair is matched (priv.N == pub.N)
// so parseRSAKeyMaterial accepts them.
func genRSAKeyPEMs(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return
}
