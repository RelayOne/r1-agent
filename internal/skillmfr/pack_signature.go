package skillmfr

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const PackSignatureFile = "pack.sig.json"

var (
	ErrPackUnsigned         = errors.New("skillmfr: pack unsigned")
	ErrPackSignatureInvalid = errors.New("skillmfr: pack signature invalid")
)

type PackSignature struct {
	Algorithm  string `json:"algorithm"`
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"public_key"`
	PackDigest string `json:"pack_digest"`
	Signature  string `json:"signature"`
}

type packDigestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type packDigestEnvelope struct {
	Files []packDigestFile `json:"files"`
}

func SignPack(packRoot, keyID string, privateKey ed25519.PrivateKey) (*PackSignature, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("skillmfr: invalid ed25519 private key length %d", len(privateKey))
	}
	if _, err := LoadPack(packRoot); err != nil {
		return nil, err
	}
	digest, err := computePackDigest(packRoot)
	if err != nil {
		return nil, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if strings.TrimSpace(keyID) == "" {
		keyID = derivePackKeyID(publicKey)
	}
	signature := ed25519.Sign(privateKey, []byte(digest))
	return &PackSignature{
		Algorithm:  "ed25519",
		KeyID:      keyID,
		PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
		PackDigest: digest,
		Signature:  base64.StdEncoding.EncodeToString(signature),
	}, nil
}

func WritePackSignature(packRoot string, signature *PackSignature) error {
	if signature == nil {
		return fmt.Errorf("skillmfr: nil pack signature")
	}
	payload, err := json.MarshalIndent(signature, "", "  ")
	if err != nil {
		return fmt.Errorf("skillmfr: marshal pack signature: %w", err)
	}
	path := filepath.Join(packRoot, PackSignatureFile)
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("skillmfr: write pack signature %q: %w", path, err)
	}
	return nil
}

func ReadPackSignature(packRoot string) (*PackSignature, error) {
	path := filepath.Join(packRoot, PackSignatureFile)
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrPackUnsigned
		}
		return nil, fmt.Errorf("skillmfr: read pack signature %q: %w", path, err)
	}
	var signature PackSignature
	if err := json.Unmarshal(payload, &signature); err != nil {
		return nil, fmt.Errorf("%w: parse pack signature %q: %v", ErrPackSignatureInvalid, path, err)
	}
	if signature.Algorithm != "ed25519" {
		return nil, fmt.Errorf("%w: unsupported pack signature algorithm %q", ErrPackSignatureInvalid, signature.Algorithm)
	}
	if strings.TrimSpace(signature.KeyID) == "" {
		return nil, fmt.Errorf("%w: pack signature key_id required", ErrPackSignatureInvalid)
	}
	if strings.TrimSpace(signature.PublicKey) == "" {
		return nil, fmt.Errorf("%w: pack signature public_key required", ErrPackSignatureInvalid)
	}
	if strings.TrimSpace(signature.PackDigest) == "" {
		return nil, fmt.Errorf("%w: pack signature pack_digest required", ErrPackSignatureInvalid)
	}
	if strings.TrimSpace(signature.Signature) == "" {
		return nil, fmt.Errorf("%w: pack signature signature required", ErrPackSignatureInvalid)
	}
	return &signature, nil
}

func VerifyPackSignature(packRoot string) (*PackSignature, error) {
	signature, err := ReadPackSignature(packRoot)
	if err != nil {
		return nil, err
	}
	digest, err := computePackDigest(packRoot)
	if err != nil {
		return nil, err
	}
	if digest != signature.PackDigest {
		return nil, fmt.Errorf("%w: pack digest mismatch: got %s want %s", ErrPackSignatureInvalid, digest, signature.PackDigest)
	}
	publicKey, err := base64.StdEncoding.DecodeString(signature.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: decode public key: %v", ErrPackSignatureInvalid, err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: invalid public key length %d", ErrPackSignatureInvalid, len(publicKey))
	}
	sigBytes, err := base64.StdEncoding.DecodeString(signature.Signature)
	if err != nil {
		return nil, fmt.Errorf("%w: decode signature: %v", ErrPackSignatureInvalid, err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(signature.PackDigest), sigBytes) {
		return nil, fmt.Errorf("%w: ed25519 verification failed", ErrPackSignatureInvalid)
	}
	return signature, nil
}

func VerifyPackSignatureIfPresent(packRoot string) (*PackSignature, error) {
	signature, err := VerifyPackSignature(packRoot)
	if err != nil {
		if errors.Is(err, ErrPackUnsigned) {
			return nil, nil
		}
		return nil, err
	}
	return signature, nil
}

func derivePackKeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "ed25519:" + hex.EncodeToString(sum[:8])
}

func computePackDigest(packRoot string) (string, error) {
	info, err := os.Stat(packRoot)
	if err != nil {
		return "", fmt.Errorf("skillmfr: stat %q: %w", packRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skillmfr: %q is not a directory", packRoot)
	}
	files := make([]packDigestFile, 0, 8)
	err = filepath.WalkDir(packRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(packRoot, path)
		if err != nil {
			return fmt.Errorf("relative path for %q: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == PackSignatureFile {
			if d.IsDir() {
				return fmt.Errorf("%w: signature path %q is a directory", ErrPackSignatureInvalid, path)
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: pack contains symlink %q", ErrPackSignatureInvalid, path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: pack contains unsupported file %q", ErrPackSignatureInvalid, path)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read pack file %q: %w", path, err)
		}
		sum := sha256.Sum256(payload)
		files = append(files, packDigestFile{
			Path:   rel,
			SHA256: hex.EncodeToString(sum[:]),
		})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	payload, err := json.Marshal(packDigestEnvelope{Files: files})
	if err != nil {
		return "", fmt.Errorf("skillmfr: marshal pack digest: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
