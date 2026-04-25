// Package mcp — redact.go — registers active AuthEnv values with the
// logging redactor so auth tokens never leak into logs, event
// payloads, stream files, or any other egress path that flows
// through logging.Redact.
//
// See specs/mcp-client.md §Auth / Secret Handling #4 and §Security
// Threat Matrix ("Secret leak via tool-result echo"). The Registry
// calls RegisterAuthSecrets at construction time and invokes the
// returned unregister closure from Close so secrets are evicted from
// the process-wide redaction set when the registry is torn down.
//
// Semantics:
//   - For every ServerConfig whose AuthEnv is non-empty, read
//     os.Getenv(cfg.AuthEnv).
//   - If the env var is set and non-empty → register the value with
//     internal/logging (exact-match redactor).
//   - If the env var is configured but empty → return ErrAuthMissing
//     (so the caller can surface a clear configuration error at
//     startup, matching the spec's "fail-closed" posture).
//   - Duplicate AuthEnv entries across configs are de-duplicated
//     (we register each DISTINCT env-var name once); the returned
//     unregister closure unregisters exactly the set it added.
//
// The value appears in plaintext nowhere in this file — the env-var
// NAME is logged (safe), the value is only ever handed to Register.

package mcp

import (
	"os"

	"github.com/RelayOne/r1-agent/internal/logging"
)

// RegisterAuthSecrets reads the AuthEnv value from the process
// environment for each ServerConfig, registers non-empty values with
// the logging redactor, and returns a closure that unregisters every
// value it added.
//
// If ANY config has a non-empty AuthEnv whose env-var resolves to
// "", the function returns ErrAuthMissing WITHOUT registering
// anything and WITHOUT running any partial unregister — so callers
// can treat a non-nil error as "nothing changed". This matches the
// spec's fail-closed construction rule: the registry should refuse
// to come up if a server requires auth but the operator forgot to
// export the token.
//
// Duplicate env-var names across cfgs are de-duplicated. Two servers
// that both set AuthEnv="GITHUB_MCP_TOKEN" will register the same
// value once and the unregister closure will unregister it once.
//
// The returned closure is safe to call zero, one, or many times.
// (The underlying logging.Register closures are sync.Once-guarded.)
func RegisterAuthSecrets(cfgs []ServerConfig) (unregister func(), err error) {
	// Phase 1: classify every config. Collect the set of distinct
	// env-var NAMES we need to register, verifying that each one
	// resolves to a non-empty value. Fail-closed BEFORE touching
	// the global registry so a partial failure never leaves stray
	// entries behind.
	seenEnv := make(map[string]struct{}, len(cfgs))
	names := make([]string, 0, len(cfgs))
	for _, cfg := range cfgs {
		if cfg.AuthEnv == "" {
			continue
		}
		if _, dup := seenEnv[cfg.AuthEnv]; dup {
			continue
		}
		val := os.Getenv(cfg.AuthEnv)
		if val == "" {
			// Configured but missing → fail-closed.
			return nil, ErrAuthMissing
		}
		seenEnv[cfg.AuthEnv] = struct{}{}
		names = append(names, cfg.AuthEnv)
	}

	// Phase 2: register each distinct secret with the logging
	// redactor. Hold every returned closure in order so the
	// unregister pass can roll them back.
	closers := make([]func(), 0, len(names))
	for _, envName := range names {
		// Re-read here rather than capture the value from Phase 1
		// so we don't hold the secret in a local slice any longer
		// than necessary. os.Getenv is cheap.
		val := os.Getenv(envName)
		if val == "" {
			// Paranoia: the env var was unset between Phase 1 and
			// Phase 2. Roll back whatever we've registered so far
			// and report auth_missing — still fail-closed.
			for _, c := range closers {
				c()
			}
			return nil, ErrAuthMissing
		}
		closers = append(closers, logging.Register(val))
	}

	return func() {
		for _, c := range closers {
			c()
		}
	}, nil
}
