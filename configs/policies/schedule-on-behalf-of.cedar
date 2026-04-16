// schedule-on-behalf-of.cedar
//
// Grants calendar write capability (create / update / delete events)
// scoped to a declared calendar allow-list. Used for "schedule my
// meetings" agents.

permit (
    principal,
    action in [
        Action::"calendar_create_event",
        Action::"calendar_update_event",
        Action::"calendar_move_event"
    ],
    resource
)
when {
    principal has scope_calendar_write &&
    principal.scope_calendar_write == true &&
    principal has calendar_allowlist &&
    resource has calendar_id &&
    principal.calendar_allowlist.contains(resource.calendar_id)
};

// Deletion is gated tighter — requires an explicit destructive flag
// in addition to the write scope. Most scheduling agents should NOT
// have delete authority; let the user remove events themselves.
permit (
    principal,
    action == Action::"calendar_delete_event",
    resource
)
when {
    principal has scope_calendar_write &&
    principal.scope_calendar_write == true &&
    principal has allow_destructive &&
    principal.allow_destructive == true &&
    principal has calendar_allowlist &&
    resource has calendar_id &&
    principal.calendar_allowlist.contains(resource.calendar_id)
};
