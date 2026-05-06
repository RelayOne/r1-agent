// Package heroa is the Heroa managed-runtime SDK for Go.
//
// The canonical entry point is Client.Deploy, which mirrors the TypeScript
// heroa.deploy() primitive described in scope §5. Construct a Client with
// New(Config{APIKey: ...}) and call client.Deploy(ctx, DeployRequest{...})
// from inside an in-flight agent. The Instance returned exposes the same
// fields as the TypeScript SDK (ID, URL, Region, Size, ExpiresAt, ...).
//
// Errors surface as typed *HeroaError values keyed by ErrorCode (ten
// values mirroring the control plane's api.ErrorCode enum). Idempotency
// keys are auto-derived from sha256(canonicalized(request)) unless the
// caller overrides via DeployRequest.IdempotencyKey.
package heroa

// SDKVersion is the current SDK build tag.
const SDKVersion = "0.1.0-h4"

// Regions lists the five public GA regions plus the sovereign substrate.
// BYOC and on-prem regions are prefixed string literals (byoc:<id>, onprem:<id>).
var Regions = []string{
	"us-east",
	"us-west",
	"eu-west",
	"asia-pacific",
	"bc-sovereign",
}

// ErrorCode is the machine-readable discriminator returned by the control
// plane. Values mirror internal/api.ErrorCode in the Heroa repo.
type ErrorCode string

// Error codes. Each value maps to a deterministic *HeroaError.
const (
	ErrCodeRegionNotAllowed       ErrorCode = "region_not_allowed"
	ErrCodeRegionCapacity         ErrorCode = "region_capacity"
	ErrCodeTemplateNotFound       ErrorCode = "template_not_found"
	ErrCodeTemplateRegionExcluded ErrorCode = "template_region_excluded"
	ErrCodeQuotaExceeded          ErrorCode = "quota_exceeded"
	ErrCodeAuth                   ErrorCode = "auth"
	ErrCodeIdempotencyConflict    ErrorCode = "idempotency_conflict"
	ErrCodePlacementFailed        ErrorCode = "placement_failed"
	ErrCodeValidation             ErrorCode = "validation"
	ErrCodeInternal               ErrorCode = "internal"
)

// ErrorCodes enumerates every ErrorCode for exhaustive branch-coverage.
var ErrorCodes = []ErrorCode{
	ErrCodeRegionNotAllowed,
	ErrCodeRegionCapacity,
	ErrCodeTemplateNotFound,
	ErrCodeTemplateRegionExcluded,
	ErrCodeQuotaExceeded,
	ErrCodeAuth,
	ErrCodeIdempotencyConflict,
	ErrCodePlacementFailed,
	ErrCodeValidation,
	ErrCodeInternal,
}

// SizeShape is the (cpus, memory_mb) tuple a size label maps to.
type SizeShape struct {
	CPUs     int
	MemoryMB int
}

// SizeShapes maps scope §2.3 size labels to their default resource shapes.
var SizeShapes = map[string]SizeShape{
	"nano":   {CPUs: 1, MemoryMB: 256},
	"small":  {CPUs: 1, MemoryMB: 512},
	"medium": {CPUs: 2, MemoryMB: 2048},
	"large":  {CPUs: 4, MemoryMB: 8192},
	"xl":     {CPUs: 8, MemoryMB: 16384},
}
