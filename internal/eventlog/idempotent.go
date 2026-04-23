package eventlog

// idempotentTools is the set of tool names considered idempotent for the
// purpose of replay / orphan handling. Read-only or pure tools (their
// call can be replayed with the same or no effect) live in this table.
// Mutating tools — write_file, edit, apply_patch, git commit, bash (not
// explicitly marked read-only), exec — default to false.
//
// The table mirrors executor-foundation §idempotent. Update both in lock
// step.
var idempotentTools = map[string]bool{
	"read_file":            true,
	"read":                 true,
	"grep":                 true,
	"glob":                 true,
	"ls":                   true,
	"bash_readonly":        true,
	"browser_extract_text": true,
	"web_search":           true,
	"web_fetch":            true,
}

// IsIdempotentTool reports whether name is a known idempotent (replayable)
// tool. Unknown names default to false — callers must treat unknown tools
// as mutating unless they have explicit evidence otherwise.
func IsIdempotentTool(name string) bool {
	return idempotentTools[name]
}
