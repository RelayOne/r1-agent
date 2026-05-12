// events.go — canonical names of the stderr envelopes that the
// `--one-shot` hardening layer emits. Centralized so the markdown
// runbook in docs/integrations/relaygate-r1-stage.md can be cross-
// checked against the code by a doc-test (see doctest_test.go) and
// so the events never drift from string-literal-soup elsewhere in
// the package.
//
// Every event is line-delimited JSON on stderr — stdout stays the
// single-Response invariant for the parser on the RelayGate side.
//
// Spec: specs/oneshot-production-hardening.md §T6.3.
package oneshot

// Stderr envelope event names. Always emitted as
// {"event":<name>,...}\n on stderr. Never on stdout.
const (
	// EventMemoryLimitHit — emitted when the process hit a hard
	// memory ceiling (RLIMIT_AS) or detected an OOM during runtime.
	// Companion exit code: ExitMemory (3).
	EventMemoryLimitHit = "oneshot.memory.limit_hit"

	// EventTimeout — emitted when --timeout fired before the verb
	// completed. The staging buffer is dropped on this path so the
	// stdout invariant (complete-or-empty) holds.
	// Companion exit code: ExitTimeout (4).
	EventTimeout = "oneshot.timeout"

	// EventShutdown — emitted when a SIGINT / SIGTERM landed before
	// the verb completed. Carries the resolved signal name.
	// Companion exit codes: ExitSIGINT (130) or ExitSIGTERM (143).
	EventShutdown = "oneshot.shutdown"

	// EventAuditDropped — emitted at exit when the audit drain
	// queue had unsent envelopes (queue overflow or context
	// timeout). Exit code is unchanged — the verb succeeded;
	// only telemetry was lost.
	EventAuditDropped = "oneshot.audit.dropped"

	// EventAuditFailed — emitted by the audit worker after 3
	// retries of a single envelope all failed. The verb's exit
	// code is unchanged; this is a stderr-only diagnostic.
	EventAuditFailed = "oneshot.audit.failed"
)

// AuditSchemaVersion is the canonical schema identifier stamped
// into every audit envelope and the X-R1-Audit-SchemaVersion
// header. Bump when the AuditEnvelope shape changes.
const AuditSchemaVersion = "r1.audit.v1"

// AuditSigHeader, AuditCorrelationIDHeader, and
// AuditSchemaVersionHeader are the request headers the audit
// worker stamps onto every POST. Exported so the doctest can
// cross-check the markdown runbook and so RelayGate-side code can
// import the constants instead of stringly-typing them.
const (
	AuditSigHeader             = "X-R1-Audit-Sig"
	AuditCorrelationIDHeader   = "X-R1-Audit-CorrelationID"
	AuditSchemaVersionHeader   = "X-R1-Audit-SchemaVersion"
)
