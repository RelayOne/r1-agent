package chat

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/provider"
)

// Dispatcher is what cmd/r1 implements to connect chat tool calls to
// the real Stoke pipeline. Each method takes a plain description string
// (what the model paraphrased out of the conversation) and runs the
// corresponding command. The returned string is surfaced back to the
// model as the tool_result so it can summarize the outcome to the user.
//
// Implementations run synchronously on the caller's goroutine. That is
// intentional — the REPL and TUI shell want "> tool running..." ↔
// "< tool done" to be linear so the user always knows what is happening.
type Dispatcher interface {
	// Scope starts a read-only scoping session to flesh out a feature
	// before committing to a build.
	Scope(description string) (string, error)
	// Build runs the /run single-task pipeline (plan → execute →
	// verify) on the described task.
	Build(description string) (string, error)
	// Ship runs the build → review → fix loop until ship-ready.
	Ship(description string) (string, error)
	// Plan generates a task plan without executing.
	Plan(description string) (string, error)
	// Audit runs the multi-persona audit on the current repo.
	Audit() (string, error)
	// Scan runs the deterministic code scanner. If securityOnly is
	// true, only security rules fire.
	Scan(securityOnly bool) (string, error)
	// Status shows the current session dashboard.
	Status() (string, error)
	// SOW executes a Statement of Work file (.json/.yaml/.md/.txt) via
	// the multi-session SOW pipeline. Used when the user has a structured
	// scope larger than a single task — chat agrees, dispatches once,
	// stoke runs every session through the same native runner the chat
	// session is connected to.
	SOW(filePath string) (string, error)
}

// ImageAwareDispatcher is an optional extension of Dispatcher for
// implementations that want to receive per-turn image attachments. If a
// Dispatcher satisfies this interface, RunToolCall calls SetTurnImages
// just before the underlying Scope/Build/Ship/... method so the
// dispatcher can fold the paths into the downstream prompt.
//
// Kept as a separate interface so existing Dispatcher implementations
// in tests and third-party embedders don't need to grow a method.
type ImageAwareDispatcher interface {
	Dispatcher
	// SetTurnImages is called with the user-typed image paths for the
	// turn that triggered this dispatch. A nil/empty slice means no
	// images were attached. Implementations should treat this as
	// turn-scoped: the next call resets the set.
	SetTurnImages(paths []string)
}

// DispatcherTools returns the provider.ToolDef slice that backs the
// chat system prompt's dispatcher tool list. Schemas are intentionally
// minimal — one required field ("description") on most tools — so the
// model can emit them without needing to know about Stoke-internal
// knobs. Knobs belong in the underlying CLI, not the chat surface.
func DispatcherTools() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "dispatch_scope",
			Description: "Start an interactive scoping session to flesh out a feature or change before committing to a build. Use this when the user wants to plan before building (e.g. 'ya make that a scope', 'let's scope it out', 'plan this before we build'). The description should paraphrase the discussion into 1-3 sentences capturing the goal and key decisions.",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{
						"type":        "string",
						"description": "1-3 sentence paraphrase of the agreed-upon scope, self-contained (the downstream pipeline does not see this chat history).",
					},
				},
				"required": []string{"description"},
			}),
		},
		{
			Name:        "dispatch_build",
			Description: "Run a single task through the full plan → execute → verify pipeline. Equivalent to /run. Use this when the user confirms they want to build the discussed change (e.g. 'ya build that', 'do it', 'let's build').",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{
						"type":        "string",
						"description": "1-2 sentence description of the build task, self-contained.",
					},
				},
				"required": []string{"description"},
			}),
		},
		{
			Name:        "dispatch_ship",
			Description: "Run the build → review → fix loop until the change is ship-ready. Equivalent to /ship. Use when the user says 'ship it' or 'make it production-ready'.",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{
						"type":        "string",
						"description": "1-2 sentence description of what to ship.",
					},
				},
				"required": []string{"description"},
			}),
		},
		{
			Name:        "dispatch_plan",
			Description: "Generate a task plan without executing. Use when the user says 'plan that out' or 'what's the plan' and wants a written plan before deciding whether to build.",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{
						"type":        "string",
						"description": "1-2 sentence description of the goal to plan.",
					},
				},
				"required": []string{"description"},
			}),
		},
		{
			Name:        "dispatch_audit",
			Description: "Run the multi-persona AI audit on the current repo. Takes no arguments. Use when the user says 'audit the code' or 'run a review'.",
			InputSchema: mustSchema(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}),
		},
		{
			Name:        "dispatch_scan",
			Description: "Run the deterministic code scanner. Use when the user says 'scan for bugs' or 'scan for security issues'. Set security_only=true for just the security rules.",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"security_only": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, only security rules run.",
					},
				},
			}),
		},
		{
			Name:        "show_status",
			Description: "Show the current session dashboard. Takes no arguments. Use when the user says 'what's running' or 'status'.",
			InputSchema: mustSchema(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}),
		},
		{
			Name:        "dispatch_sow",
			Description: "Run a Statement of Work file through the multi-session SOW pipeline. Use when the user has a structured spec on disk (a .json/.yaml/.md/.txt file) bigger than a single task — for example 'build the SOW at /path/to/sow.md', 'execute that scope', or 'run the contractor spec'. The pipeline decomposes prose into sessions, runs each session through the same native runner the chat is using, and gates session-to-session transitions on acceptance criteria. Pass the absolute path of the SOW file. Do NOT use this for ad-hoc single tasks — use dispatch_build for those.",
			InputSchema: mustSchema(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute path to the SOW file (.json, .yaml, .yml, .md, .txt). Required.",
					},
				},
				"required": []string{"file_path"},
			}),
		},
	}
}

// DispatchToolArgs is a convenience struct for pulling the common
// "description" field out of a tool input. Caller passes the raw JSON
// from the model; ExtractDescription handles both map and JSON Raw.
type DispatchToolArgs struct {
	Description  string `json:"description"`
	SecurityOnly bool   `json:"security_only"`
	FilePath     string `json:"file_path"`
}

// ExtractArgs decodes the model's tool input into a DispatchToolArgs.
// A missing or malformed body yields an empty struct (no error) so the
// caller can always check the fields directly.
func ExtractArgs(input json.RawMessage) DispatchToolArgs {
	var a DispatchToolArgs
	if len(input) == 0 {
		return a
	}
	_ = json.Unmarshal(input, &a)
	return a
}

// RunToolCall is the glue between the chat session and a Dispatcher. It
// decodes the tool name/args and calls the corresponding Dispatcher
// method, returning the human-readable result that goes back to the
// model as tool_result content.
//
// Unknown tool names return an error so the model learns to pick from
// the advertised list.
//
// For image-aware dispatch, use RunToolCallWithImages and pass the
// per-turn image paths (typically from Session.LastTurnImages()).
func RunToolCall(d Dispatcher, name string, input json.RawMessage) (string, error) {
	return RunToolCallWithImages(d, name, input, nil)
}

// RunToolCallWithImages is RunToolCall plus the per-turn image paths
// attached to the user message that triggered this dispatch. When d
// implements ImageAwareDispatcher, the paths are pushed via
// SetTurnImages before the underlying method fires; otherwise the
// paths are ignored (backwards compatible with plain Dispatcher).
func RunToolCallWithImages(d Dispatcher, name string, input json.RawMessage, imagePaths []string) (string, error) {
	if ia, ok := d.(ImageAwareDispatcher); ok {
		// Always call SetTurnImages, even with nil, so a prior turn's
		// images cannot leak forward. The dispatcher is responsible
		// for clearing its own state on a nil/empty list.
		ia.SetTurnImages(imagePaths)
	}
	args := ExtractArgs(input)
	switch name {
	case "dispatch_scope":
		return d.Scope(args.Description)
	case "dispatch_build":
		return d.Build(args.Description)
	case "dispatch_ship":
		return d.Ship(args.Description)
	case "dispatch_plan":
		return d.Plan(args.Description)
	case "dispatch_audit":
		return d.Audit()
	case "dispatch_scan":
		return d.Scan(args.SecurityOnly)
	case "show_status":
		return d.Status()
	case "dispatch_sow":
		return d.SOW(args.FilePath)
	}
	return "", unknownToolError{name: name}
}

type unknownToolError struct{ name string }

func (e unknownToolError) Error() string { return "chat: unknown tool: " + e.name }

// mustSchema marshals a schema map into json.RawMessage. Panics only if
// json.Marshal fails, which should never happen for a well-formed
// literal schema.
func mustSchema(m map[string]interface{}) json.RawMessage {
	raw, err := json.Marshal(m)
	if err != nil {
		panic("chat: schema marshal: " + err.Error())
	}
	return raw
}
