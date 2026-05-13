package app

// promptguard_boot.go — daemon-startup hook for the promptguard
// system-prompt fingerprinting path (spec §T3 item 13).
//
// SPEC DEVIATION (documented in commit body): the spec named the entry
// point `bootDaemon()` in internal/app/orchestrator.go. The real
// codebase has no single bootDaemon function and no single global
// assembled system prompt — prompts are built per-role / per-stance /
// per-task by callers like cortex/prewarm, plan/sow_brief,
// harness/stances. To preserve the spec's operator-visible behaviour
// (an operator running `r1 promptguard verify-system-prompt` gets a
// Verified|Modified|Tampered answer at daemon start), this file owns
// a `FingerprintBootPrompt` helper that:
//
//   - Reads an assembled system prompt from a caller-supplied bytes
//     value or from a discoverable file under .stoke/promptguard/.
//   - Signs it with the env-configured ed25519 key (falls back to
//     R1_LEDGER_SIGNING_KEY when R1_PROMPTGUARD_SIGNING_KEY is unset,
//     matching the spec's first-wins / second-fallback shape).
//   - Persists a SystemPromptFingerprint node to the ledger.
//   - Writes the assembled prompt under
//     .stoke/promptguard/system_prompt.txt so the verify CLI can
//     re-hash it at operator-invocation time.
//
// On signer-unavailable the helper logs a WARN and returns; the
// daemon keeps running. This is the spec's dev-mode posture (no key
// configured → no startup failure).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/promptguard"
)

// FingerprintBootPrompt signs `promptText` and writes a
// SystemPromptFingerprint ledger node + an on-disk copy of the prompt
// under <r1Dir>/promptguard/system_prompt.txt so `r1 promptguard
// verify-system-prompt` can re-hash it.
//
// Returns the assigned ledger node ID on success. Returns an empty
// string + nil error when no signing key is configured (the dev-mode
// no-op path). Surfaces other errors so the caller (daemon boot) can
// decide whether to fail-closed in prod.
func FingerprintBootPrompt(ctx context.Context, lg *ledger.Ledger, r1Dir, promptText, assemblyTrace string) (string, error) {
	fp, err := promptguard.SignSystemPrompt(promptText)
	if err != nil {
		if errors.Is(err, promptguard.ErrNoSigningKey) {
			log.Printf("promptguard: no signing key configured; system prompt fingerprinting disabled")
			return "", nil
		}
		return "", fmt.Errorf("promptguard boot: sign: %w", err)
	}

	// Persist the assembled prompt so the verify CLI can re-read it.
	if r1Dir != "" {
		dir := filepath.Join(r1Dir, "promptguard")
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return "", fmt.Errorf("promptguard boot: mkdir: %w", mkErr)
		}
		if wErr := os.WriteFile(filepath.Join(dir, "system_prompt.txt"), []byte(promptText), 0o600); wErr != nil {
			return "", fmt.Errorf("promptguard boot: write prompt: %w", wErr)
		}
	}

	// Build a typed node and persist via the ledger.
	node := nodes.SystemPromptFingerprint{
		PromptSHA256:   fp.PromptSHA256,
		KeyFingerprint: fp.KeyFingerprint,
		SignedAt:       fp.SignedAt,
		Signature:      fp.Signature,
		AssemblyTrace:  assemblyTrace,
		Version:        1,
	}
	if vErr := node.Validate(); vErr != nil {
		return "", fmt.Errorf("promptguard boot: validate: %w", vErr)
	}
	content, mErr := json.Marshal(node)
	if mErr != nil {
		return "", fmt.Errorf("promptguard boot: marshal: %w", mErr)
	}
	id, err := lg.AddNode(ctx, ledger.Node{
		Type:          "system_prompt_fingerprint",
		SchemaVersion: 1,
		CreatedBy:     "r1 daemon boot",
		Content:       content,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		return "", fmt.Errorf("promptguard boot: ledger add: %w", err)
	}

	// Short keyFP for the log line: first 16 hex chars.
	keyFP := fp.KeyFingerprint
	shaPrefix := fp.PromptSHA256
	if len(shaPrefix) > 16 {
		shaPrefix = shaPrefix[:16]
	}
	log.Printf("promptguard: system prompt fingerprinted sha256=%s key=%s", shaPrefix, keyFP)
	return id, nil
}
