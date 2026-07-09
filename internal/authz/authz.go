// Package authz wires the canonical ActionAuthorizer seam
// (internal/honestcrypto) into R1's tool boundary: a session runs under a signed
// permitted-action spec, and a tool the spec does not permit is rejected before
// it executes (reject-before-execute, fail-closed). The spec is loaded via the
// SeedResolver path (alongside seed profiles), the default backend is a signed
// allowlist, and a stronger authorizer swaps in by config with zero caller
// changes (see the swap-proof test).
package authz

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// SpecFileName / PubKeyFileName are the files an action-spec profile carries
// under <seedsPath>/<profile>/.
const (
	SpecFileName   = "action-spec.json"
	PubKeyFileName = "action-pubkey.pem"
)

// LoadFromSeedProfile loads a SignedActionSpec + principal public key from
// <seedsPath>/<profile>/ and constructs a fail-closed SignedAllowlistAuthorizer.
//
// Returns (nil, nil) when the profile carries no action-spec.json — authz is
// simply disabled for that profile (opt-in). Returns an error only when a spec
// exists but is malformed, missing its pubkey, or wrongly signed: a bad spec
// must fail the session closed, never silently run unauthorized.
func LoadFromSeedProfile(seedsPath, profile string) (honestcrypto.ActionAuthorizer, error) {
	if seedsPath == "" || profile == "" {
		return nil, nil
	}
	dir := filepath.Join(seedsPath, profile)
	specPath := filepath.Join(dir, SpecFileName)
	specBytes, err := os.ReadFile(specPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no spec => authz disabled for this profile
	}
	if err != nil {
		return nil, fmt.Errorf("authz: read %s: %w", specPath, err)
	}
	var spec honestcrypto.SignedActionSpec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return nil, fmt.Errorf("authz: parse %s: %w", specPath, err)
	}
	pubPath := filepath.Join(dir, PubKeyFileName)
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("authz: read %s (required when %s is present): %w", pubPath, SpecFileName, err)
	}
	authorizer, err := honestcrypto.NewSignedAllowlistAuthorizer(spec, string(pubBytes), nil)
	if err != nil {
		return nil, fmt.Errorf("authz: %w", err) // fail-closed: bad signature / expiry parse
	}
	return authorizer, nil
}

// ToolAction maps a tool name to the action string an ActionAuthorizer evaluates.
// R1 authorizes tools by their name directly (spec allowlists carry tool names or
// "*"), so the mapping is the identity — kept as a function so a future scheme
// (namespacing, resource-derived actions) has one place to change.
func ToolAction(toolName string) string { return toolName }

// Decide authorizes a tool call against an authorizer. A nil authorizer means
// authz is disabled and everything is allowed (opt-in seam).
func Decide(authorizer honestcrypto.ActionAuthorizer, toolName string, params map[string]any) honestcrypto.AuthorizationResult {
	if authorizer == nil {
		return honestcrypto.AuthorizationResult{Decision: honestcrypto.DecisionAllow, Reason: "authz disabled"}
	}
	return authorizer.Authorize(honestcrypto.ActionRequest{Action: ToolAction(toolName), Params: params})
}

// DecisionRecord expresses an authorization decision as a canonical
// honest-crypto record so it is auditable and anchorable through the ledger seam.
func DecisionRecord(subject, toolName string, res honestcrypto.AuthorizationResult, specHash, ts string) (honestcrypto.Record, error) {
	return honestcrypto.MakeRecord(honestcrypto.NewRecordInput{
		Subject:  subject,
		Producer: "r1",
		Kind:     "authz.decision",
		TS:       ts,
		Body: map[string]any{
			"tool":     toolName,
			"action":   ToolAction(toolName),
			"decision": string(res.Decision),
			"reason":   res.Reason,
			"specHash": specHash,
		},
		PrevHash: nil,
	})
}
