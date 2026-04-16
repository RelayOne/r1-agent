// send-on-behalf-of.cedar
//
// Grants messaging capability scoped to an explicit recipient allow-
// list carried on the principal's delegation context. Used for
// "respond to customer support inbound" agents that should only
// reply to declared counterparties.
//
// Stacks on top of default-blocked.cedar (which forbids
// send_message by default); this bundle's permit fires only when
// BOTH scope_send_message is true AND the recipient is in the
// allow-list the DCT attached.

permit (
    principal,
    action == Action::"send_message",
    resource
)
when {
    principal has scope_send_message &&
    principal.scope_send_message == true &&
    principal has send_allowlist &&
    resource has recipient &&
    principal.send_allowlist.contains(resource.recipient)
};

// Email reply / forward are considered forms of sending and gated
// the same way.
permit (
    principal,
    action in [Action::"email_reply", Action::"email_forward"],
    resource
)
when {
    principal has scope_send_message &&
    principal.scope_send_message == true &&
    principal has send_allowlist &&
    resource has recipient &&
    principal.send_allowlist.contains(resource.recipient)
};

// No draft creation without the same allow-list — drafts are
// often the precursor to accidental sends.
permit (
    principal,
    action == Action::"email_create_draft",
    resource
)
when {
    principal has scope_send_message &&
    principal.scope_send_message == true &&
    principal has send_allowlist &&
    resource has recipient &&
    principal.send_allowlist.contains(resource.recipient)
};
