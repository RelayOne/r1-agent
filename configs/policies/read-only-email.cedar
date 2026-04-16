// read-only-email.cedar
//
// Grants read access to the user's inbox without reply / forward /
// send. Useful for "summarize my unread messages" agents that
// should NEVER accidentally reply.

permit (
    principal,
    action in [
        Action::"email_list_messages",
        Action::"email_get_message",
        Action::"email_search",
        Action::"email_list_threads",
        Action::"email_get_thread",
        Action::"email_list_labels"
    ],
    resource
)
when { principal has scope_email_read && principal.scope_email_read == true };

// Explicit forbids — these stack above any companion bundle's
// permits unless a matching write-scope flag is set in the DCT.
forbid (
    principal,
    action in [
        Action::"email_send",
        Action::"email_reply",
        Action::"email_forward",
        Action::"email_create_draft",
        Action::"email_delete_message",
        Action::"email_trash_message",
        Action::"email_modify_labels"
    ],
    resource
)
unless { principal has scope_email_write && principal.scope_email_write == true };
