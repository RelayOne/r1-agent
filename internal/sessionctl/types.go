// Package sessionctl implements a Unix socket + (later) HTTP JSON-RPC surface
// for controlling a running Stoke chat-descent session: pause/resume, budget
// top-ups, approvals, injection, and takeover handoff.
//
// This file defines the wire types and verb constants. See server.go and
// client.go for the transport layer. Signaler is the OS-abstracted
// process-group pause primitive; see signaler_unix.go / signaler_other.go.
package sessionctl

import (
	"encoding/json"
	"io"
)

// Verb constants -- exhaustive list of RPC verbs the server understands.
const (
	VerbStatus          = "status"
	VerbApprove         = "approve"
	VerbOverride        = "override"
	VerbBudgetAdd       = "budget_add"
	VerbPause           = "pause"
	VerbResume          = "resume"
	VerbInject          = "inject"
	VerbTakeoverRequest = "takeover_request"
	VerbTakeoverRelease = "takeover_release"
)

// Request is one sessionctl JSON-RPC call.
type Request struct {
	Verb      string          `json:"verb"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Token     string          `json:"token,omitempty"`
	KeepAlive bool            `json:"keep_alive,omitempty"`
}

// Response is the reply to a Request.
type Response struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
	EventID   string          `json:"event_id,omitempty"`
}

// Handler runs a verb. Returns (Response.Data, Response.Error, EventID). If
// errMsg is non-empty, Response.OK is set to false by the server.
type Handler func(req Request) (data json.RawMessage, errMsg string, eventID string)

// Opts configures Server.
type Opts struct {
	SocketDir string             // default "/tmp"
	SessionID string             // socket path = <SocketDir>/stoke-<SessionID>.sock
	HTTPAddr  string             // "" = socket only
	AuthToken string             // required for HTTP; bypassed for socket
	Handlers  map[string]Handler // verb -> handler
}

// Signaler abstracts process-group signaling for tests and non-POSIX builds.
type Signaler interface {
	Pause(pgid int) error
	Resume(pgid int) error
}

// ReadRequest reads one NDJSON line from r and decodes into Request.
func ReadRequest(r io.Reader) (Request, error) {
	var req Request
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	return req, err
}

// WriteResponse writes one Response as a single NDJSON line to w.
func WriteResponse(w io.Writer, resp Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
