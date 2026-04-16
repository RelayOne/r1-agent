// read-only-calendar.cedar
//
// Grants read access to the user's calendar without any modification
// capability. Used for delegation scopes like:
//
//   /delegate @agent scope=read-only-calendar expiry=7d
//
// Stacks on top of default-blocked.cedar; additive only.

permit (
    principal,
    action in [
        Action::"calendar_list_events",
        Action::"calendar_get_event",
        Action::"calendar_search",
        Action::"calendar_list_calendars"
    ],
    resource
)
when { principal has scope_calendar_read && principal.scope_calendar_read == true };

// Explicit forbid on every mutating calendar action so this bundle
// can be stacked with a broader scope bundle (e.g. schedule-on-
// behalf-of) without accidentally granting write access — the
// forbid here overrides any permit from the companion bundle
// unless the companion's DCT carries scope_calendar_write.
forbid (
    principal,
    action in [
        Action::"calendar_create_event",
        Action::"calendar_update_event",
        Action::"calendar_delete_event",
        Action::"calendar_move_event"
    ],
    resource
)
unless { principal has scope_calendar_write && principal.scope_calendar_write == true };
