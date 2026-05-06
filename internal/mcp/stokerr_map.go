// stokerr_map.go — map every error returned from an r1.* tool handler to a
// stokerr/ taxonomy code so the Slack-style envelope (envelope.go) carries a
// stable error_code for downstream agents.
//
// Per specs/agentic-test-harness.md §3 ("Existing Patterns to Follow"):
//
//   > Reuse stokerr/ taxonomy for every error response. Map every internal
//   > error to one of the 10 codes; never return a raw Go error string.
//
// Behavior:
//   - If the error is already a *stokerr.Error, its Code is used verbatim.
//   - Else: classify by sentinel/structural cues (context.DeadlineExceeded,
//     os.ErrNotExist, fs.ErrPermission, etc.). Fall back to ErrInternal.
//   - The mapping returns the canonical string form of the code (lower
//     snake_case) to match the JSON wire shape.
package mcp

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/stokerr"
)

// MapErrorToTaxonomy classifies an error against the stokerr/ taxonomy.
// Returns (code, message). When err is nil it returns ("", "") so callers
// can use the same call site for both branches without nil checks.
func MapErrorToTaxonomy(err error) (code, message string) {
	if err == nil {
		return "", ""
	}
	// 1. Direct stokerr.*Error path: use the carried code verbatim.
	var se *stokerr.Error
	if errors.As(err, &se) {
		return string(se.Code), se.Message
	}
	// 2. Structural sentinels.
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return string(stokerr.ErrTimeout), err.Error()
	case errors.Is(err, os.ErrNotExist), errors.Is(err, fs.ErrNotExist):
		return string(stokerr.ErrNotFound), err.Error()
	case errors.Is(err, os.ErrPermission), errors.Is(err, fs.ErrPermission):
		return string(stokerr.ErrPermission), err.Error()
	case errors.Is(err, os.ErrExist):
		return string(stokerr.ErrConflict), err.Error()
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		if nerr.Timeout() {
			return string(stokerr.ErrTimeout), err.Error()
		}
	}
	// 3. String-shape heuristics (last resort; deliberately narrow). These
	//    catch fmt.Errorf chains from legacy handlers that have not yet
	//    been migrated to stokerr/. New code must not rely on this branch.
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "is required"),
		strings.Contains(low, "invalid"),
		strings.Contains(low, "validation"):
		return string(stokerr.ErrValidation), err.Error()
	case strings.Contains(low, "not found"):
		return string(stokerr.ErrNotFound), err.Error()
	case strings.Contains(low, "timeout"):
		return string(stokerr.ErrTimeout), err.Error()
	case strings.Contains(low, "permission"), strings.Contains(low, "forbidden"):
		return string(stokerr.ErrPermission), err.Error()
	case strings.Contains(low, "conflict"), strings.Contains(low, "exists"):
		return string(stokerr.ErrConflict), err.Error()
	}
	// 4. Catch-all.
	return string(stokerr.ErrInternal), err.Error()
}

// EnvelopeFromError builds an error Envelope from an arbitrary error using
// MapErrorToTaxonomy, attaching the supplied tool name as Self. Convenience
// for handlers that want a single-line "return ErrEnvelope(...)" pattern.
func EnvelopeFromError(toolName string, err error, related ...string) Envelope {
	code, msg := MapErrorToTaxonomy(err)
	if code == "" {
		code = string(stokerr.ErrInternal)
		msg = "unknown error"
	}
	return ErrEnvelope(toolName, code, msg, related...)
}
