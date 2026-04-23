// Package tools defines the tool authorization model for stance workers.
// Each role has a fixed set of authorized tools; the harness checks
// authorization before permitting any tool invocation.
package tools

// ToolName identifies a tool available to stance workers.
type ToolName string

const (
	ToolFileRead           ToolName = "file_read"
	ToolFileWrite          ToolName = "file_write"
	ToolCodeRun            ToolName = "code_run"
	ToolWebSearch          ToolName = "web_search"
	ToolWebFetch           ToolName = "web_fetch"
	ToolLedgerQuery        ToolName = "ledger_query"
	ToolLedgerWrite        ToolName = "ledger_write"
	ToolSkillImportPropose ToolName = "skill_import_propose"
	ToolBusPublish         ToolName = "bus_publish"
	ToolResearchRequest    ToolName = "research_request"
	ToolEnvExec            ToolName = "env_exec"
	ToolEnvCopyIn          ToolName = "env_copy_in"
	ToolEnvCopyOut         ToolName = "env_copy_out"

	// ToolReportEnvIssue is the descent-hardening spec-1 item 6 tool.
	// Dev and Reviewer stances advertise it so a worker can declare
	// an environment blocker without burning LLM reasoning budget.
	// The native-runtime handler lives in cmd/stoke/sow_env_issue.go.
	ToolReportEnvIssue ToolName = "report_env_issue"
)

// roleTools maps each role to its authorized tool set.
var roleTools = map[string][]ToolName{
	"dev": {
		ToolFileRead, ToolFileWrite, ToolCodeRun,
		ToolEnvExec, ToolEnvCopyIn, ToolEnvCopyOut,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
		ToolReportEnvIssue,
	},
	"reviewer": {
		ToolFileRead, ToolCodeRun,
		ToolEnvExec,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
		ToolReportEnvIssue,
	},
	"judge": {
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
	"cto": {
		ToolFileRead, ToolWebSearch, ToolWebFetch,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
	"lead_engineer": {
		ToolFileRead, ToolFileWrite, ToolWebSearch, ToolWebFetch,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
	"po": {
		ToolFileRead, ToolFileWrite, ToolWebSearch, ToolWebFetch,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
	"researcher": {
		ToolFileRead, ToolWebSearch, ToolWebFetch,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
		ToolSkillImportPropose,
	},
	"qa_lead": {
		ToolFileRead, ToolCodeRun, ToolWebSearch, ToolWebFetch,
		ToolEnvExec, ToolEnvCopyIn, ToolEnvCopyOut,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
	"stakeholder": {
		ToolFileRead, ToolLedgerQuery, ToolBusPublish, ToolResearchRequest,
	},
	"sdm": {
		ToolLedgerQuery, ToolBusPublish,
	},
	"vp_eng": {
		ToolFileRead, ToolWebSearch,
		ToolLedgerQuery, ToolBusPublish, ToolResearchRequest,
	},
	"lead_designer": {
		ToolFileRead, ToolWebSearch,
		ToolLedgerQuery, ToolLedgerWrite, ToolBusPublish, ToolResearchRequest,
	},
}

// DefaultToolsForRole returns the default authorized tools for a stance role.
// Returns nil if the role is unknown.
func DefaultToolsForRole(role string) []ToolName {
	t, ok := roleTools[role]
	if !ok {
		return nil
	}
	out := make([]ToolName, len(t))
	copy(out, t)
	return out
}

// IsAuthorized checks if a tool call is allowed for the given role.
func IsAuthorized(role string, tool ToolName) bool {
	for _, t := range roleTools[role] {
		if t == tool {
			return true
		}
	}
	return false
}
