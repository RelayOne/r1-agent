// Package oneshot is the CloudSwarm hero-skill bridge for
// discrete R1 primitives. CloudSwarm's supervisor sidecar
// spawns `stoke --one-shot <verb>` to invoke a single R1
// reasoning service (decompose / verify / critique) without
// paying the cost of spinning up a full interactive session,
// loading skills, or wiring a ledger.
//
// Spec: /home/eric/repos/plans/work-orders/scope/CLOUDSWARM-R1-
// INTEGRATION.md §5.6.1.
//
// Contract:
//   - Input:  path to a JSON file (or "-" for stdin) containing
//             the verb's request payload.
//   - Output: a single JSON object written to stdout followed by
//             a newline. Exit 0 on success; non-zero with a
//             machine-readable JSON error on stderr otherwise.
//
// Each verb is a thin wrapper over an existing internal/plan
// primitive — see decompose.go, verify.go, critique.go for the
// per-verb plumbing. A verb that can't service the request
// returns a Response with Status=="error" and a populated Data
// shape that includes an `error` field; the CLI still exits 0
// in that case because the program itself ran successfully.
// Hard runtime errors (e.g. failed JSON marshal) bubble up to
// the caller as Go errors and map to exit code 1.
package oneshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrUnknownVerb is returned by Dispatch when the verb is not
// one of the supported set. Exported so the CLI can distinguish
// "bad argument" (exit 2) from "runtime error" (exit 1).
var ErrUnknownVerb = errors.New("oneshot: unknown verb")

// SupportedVerbs is the canonical list in declaration order.
// Handy for --help text, contract tests, and the CloudSwarm
// supervisor's "did the R1 binary advertise this verb" probe.
var SupportedVerbs = []string{"decompose", "verify", "critique"}

// StatusOK is the Response.Status string emitted by a handler
// that ran to completion without error.
const StatusOK = "ok"

// StatusError is the Response.Status string emitted when the verb
// handler ran but could not produce a result (invalid input,
// missing preconditions, etc.). Program exit is still 0 — the
// caller reads Status to branch. StatusError in the top-level
// Response is mirrored by an `error` field inside Response.Data
// so CloudSwarm can surface the reason without a second parse.
const StatusError = "error"

// StatusScaffold is retained as the legacy status string from the
// pre-wiring scaffold phase. New code emits StatusOK / StatusError;
// the constant stays for callers that pinned to the old string.
const StatusScaffold = "scaffold"

// Request is the generic wrapper around a verb payload. The
// dispatcher only uses Verb; each verb handler unmarshals the
// Payload into its own typed shape. Exposed so alternate
// callers (tests, in-process drivers) can build a Request
// directly instead of marshaling JSON.
type Request struct {
	// Verb is one of SupportedVerbs.
	Verb string `json:"verb"`

	// Payload is the verb-specific request body. Opaque to
	// the dispatcher; each verb handler unmarshals into its
	// own typed shape.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is the top-level result written to stdout. Every
// verb guarantees these fields are populated; verb-specific
// payload lives under Data.
type Response struct {
	// Verb echoes the request verb.
	Verb string `json:"verb"`

	// Status is StatusOK on success, StatusError when the verb
	// could not produce a result.
	Status string `json:"status"`

	// Data is the verb-specific result payload. Shape depends
	// on the verb; see each verb handler.
	Data json.RawMessage `json:"data,omitempty"`

	// Note is a human-readable hint, surfaced in CloudSwarm
	// logs when Status != StatusOK.
	Note string `json:"note,omitempty"`
}

// Dispatch runs the named verb against the provided payload and
// returns the response. Returns ErrUnknownVerb for an unknown
// verb (wrapped so callers can errors.Is).
func Dispatch(verb string, payload json.RawMessage) (Response, error) {
	switch verb {
	case "decompose":
		return handleDecompose(payload)
	case "verify":
		return handleVerify(payload)
	case "critique":
		return handleCritique(payload)
	default:
		return Response{}, fmt.Errorf("%w: %q (supported: %v)",
			ErrUnknownVerb, verb, SupportedVerbs)
	}
}

// Run reads the JSON input from r, dispatches to the named verb,
// and writes the JSON result to w. Used by cmd/stoke to wire the
// CLI flag; also exported for in-process callers (tests).
//
// Input is the verb-specific payload. The CLI's `--one-shot <verb>`
// already pins the verb so we don't require the Request envelope.
func Run(verb string, r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("oneshot: read input: %w", err)
	}
	// Empty input is treated as an empty payload. Verb handlers
	// validate required fields themselves and emit a StatusError
	// response when a field is missing.
	payload := json.RawMessage(raw)
	if len(raw) == 0 {
		payload = nil
	}
	resp, err := Dispatch(verb, payload)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("oneshot: write response: %w", err)
	}
	return nil
}

// RunFromFile is a convenience used by the CLI when the operator
// passes --input /path. Reads the file, runs the verb, writes to
// stdout. Accepts "-" or "" to mean "read stdin" which matches
// the convention used by the rest of the stoke CLI.
func RunFromFile(verb, inputPath string, w io.Writer) error {
	if inputPath == "" || inputPath == "-" {
		return Run(verb, os.Stdin, w)
	}
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("oneshot: open input: %w", err)
	}
	defer f.Close()
	return Run(verb, f, w)
}

// errorResponse is a shared helper for verb handlers: build a
// uniform {status:"error", error:"..."} Response that still echoes
// the verb. Returned as (Response, nil) so the CLI exits 0 and
// CloudSwarm can parse the Response envelope on stdout rather than
// grepping stderr.
func errorResponse(verb, msg string) (Response, error) {
	body := map[string]string{
		"verb":   verb,
		"status": StatusError,
		"error":  msg,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("oneshot: marshal error: %w", err)
	}
	return Response{
		Verb:   verb,
		Status: StatusError,
		Data:   data,
		Note:   msg,
	}, nil
}
