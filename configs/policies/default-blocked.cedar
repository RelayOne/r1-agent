// default-blocked.cedar
//
// Base deny-list every delegated session inherits unless an explicit
// scope override in a layered policy grants the capability. This is
// the "default deny" posture: no ambient authority. Actions the agent
// needs must be named in a stacked policy bundle.
//
// Consumed by internal/supervisor/ via the TrustPlane Cedar SDK.
// Layered under any role-specific bundle (personal-assistant,
// coding-team, etc.) so the forbid rules fire first.

// Recursive delegation is globally forbidden. An agent acting under
// a DelegationToken can't further delegate its authority onward
// unless the parent DCT explicitly granted `scope: delegate`.
forbid (
    principal,
    action == Action::"delegate",
    resource
)
unless { principal has allow_delegate && principal.allow_delegate == true };

// Blocks agents from sending messages (email, Slack, DMs) on the
// user's behalf without an explicit `send_message` scope in the DCT.
// The real enforcement lives in the tool-call gateway; this policy
// is the Cedar half of the belt-and-suspenders.
forbid (
    principal,
    action == Action::"send_message",
    resource
)
unless { principal has scope_send_message && principal.scope_send_message == true };

// Memory writes across agent boundaries are denied by default. A
// delegated sub-agent shouldn't silently mutate the delegator's
// semantic memory; that's how multi-agent systems develop drift.
forbid (
    principal,
    action == Action::"memory_write",
    resource
)
unless { principal has scope_memory_write && principal.scope_memory_write == true };

// Arbitrary code execution (the Hermes "execute_code" action) is
// the highest-blast-radius capability. Default deny; opt-in only via
// an explicit DCT scope AND a matching sandbox configuration.
forbid (
    principal,
    action == Action::"execute_code",
    resource
);
