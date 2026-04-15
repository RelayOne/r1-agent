// Package plan — sow_convert_chunked.go
//
// Chunked prose-to-SOW conversion. Replaces the monolithic
// ConvertProseToSOW path that issued ONE giant LLM call, which on
// large SOWs (1500+ lines of prose, 20+ sessions) regularly took 15+
// minutes and sometimes hung indefinitely on LiteLLM/upstream slow
// responses.
//
// Architecture:
//
//   Phase 1: ExtractSkeleton(prose) → SOW skeleton with sessions
//            stubbed out (id, title, description, outputs only — NO
//            tasks, NO ACs). Small LLM call (~3-8k output).
//
//   Phase 2: For each session stub, ExpandSession(prose, stub) →
//            full Session with tasks + ACs. Each call ~3-8k output.
//            Runs concurrently (up to maxParallel).
//
//   Phase 3: Reassemble skeleton + expanded sessions → SOW.
//            Run autoFillMissingACFields + autoCleanTaskDeps +
//            autoAddMissingInfra + ValidateSOW.
//
// Per-call timeouts prevent the indefinite-hang failure mode. A
// per-session failure is retried once; persistent failure leaves
// the session with an explanatory empty-tasks stand-in rather
// than aborting the whole conversion.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// chunkedSkeletonPrompt asks the model to extract just the
// high-level structure of the SOW: project id, name, stack, and a
// list of session stubs (id, title, description, outputs). No
// tasks, no acceptance criteria.
const chunkedSkeletonPrompt = `You are reading a free-form project specification and extracting its high-level structure as JSON. DO NOT generate tasks or acceptance criteria — those come in a later pass.

Output ONLY the JSON object below — no prose, no markdown fences, no commentary. Start with '{' and end with '}'.

Schema:
{
  "id": "string (short slug for the project)",
  "name": "string (human-readable name)",
  "description": "string (1-2 sentence project summary)",
  "stack": {
    "language": "typescript|python|go|rust|...",
    "framework": "next|react-native|actix-web|... (optional)",
    "monorepo": {"tool": "turborepo|nx|cargo-workspace|...", "manager": "pnpm|npm|yarn", "packages": ["..."]},
    "infra": [{"name": "postgres|redis|...", "version": "15", "env_vars": ["DATABASE_URL"]}]
  },
  "sessions": [
    {
      "id": "S1",
      "phase": "foundation|core|integration",
      "title": "Short session name",
      "description": "1-2 sentence what this session delivers",
      "outputs": ["short artifact names — 2-4 words each, e.g. 'monorepo skeleton', 'auth flow', 'web dashboard'"]
    }
  ]
}

RULES:
1. Session count scales with spec size. A 1500-line spec with 8 deliverable areas → 8-15 sessions. A small CLI spec → 2-4 sessions. Don't compress for compression's sake.
2. The first session is foundation: monorepo skeleton, deps, config, hello-world build pass.
3. The last session is integration / polish / docs / deployment configs.
4. Session.outputs are the critical glue: downstream sessions reference these as Inputs to form the dependency DAG. Use 2-4 word artifact names.
5. Infer the stack from the prose. If TypeScript+Next.js, set language and framework. If Postgres mentioned, add to stack.infra with the env vars it needs.
6. If the spec is huge, prefer MORE sessions (each smaller) over FEWER sessions (each larger). The execution layer will further split anything too big.

PROSE INPUT:
`

// chunkedSessionPrompt asks the model to expand ONE session of an
// already-extracted skeleton into full tasks + ACs. The full prose
// is passed as context so the model can find the spec details
// relevant to this session.
const chunkedSessionPrompt = `You are expanding ONE session of a Statement of Work into its concrete tasks + acceptance criteria. The session's id, title, description, and outputs are already determined. Your job: emit the JSON for THIS session only — including its tasks (with files + dependencies) and acceptance_criteria.

Output ONLY a JSON session object — no prose, no markdown fences, no commentary, no top-level SOW wrapper. Start with '{' and end with '}'.

Schema:
{
  "id": "EXACTLY the session id provided below",
  "phase": "same phase",
  "title": "same title",
  "description": "same description (or improve it if needed)",
  "tasks": [
    {
      "id": "TN — unique across the whole SOW; use numbers far enough apart that other sessions' tasks won't collide (e.g. S3 → T30, T31, T32 ...)",
      "description": "single specific sentence",
      "files": ["paths the task creates or modifies"],
      "dependencies": ["task IDs from PRIOR sessions (look at the inputs list); never reference a task in a later session"],
      "type": "refactor|typesafety|docs|security|architecture|devops|concurrency|review (optional)"
    }
  ],
  "acceptance_criteria": [
    {
      "id": "ACN — unique across the whole SOW",
      "description": "what passes mean",
      "command": "shell command that exits 0; OR file_exists / content_match per the schema"
    }
  ],
  "inputs": ["artifact names from earlier sessions this session depends on"],
  "outputs": ["copy from the skeleton — these are the artifact names downstream sessions reference"]
}

RULES (follow these or this session's tasks will fail at execution):

  a. TASK COUNT scales with session scope. A small infra session needs 5-10 tasks; a major deliverable needs 15-30. Tasks should be small enough to run in a few tool calls each.

  b. Every task description is a SINGLE specific sentence. No bullet lists, no vague goals.

  c. ACCEPTANCE CRITERIA hygiene:
     - Commands run in current working directory; no '$REPO_URL', no 'mktemp', no 'git clone'
     - No '|| echo ok' / '|| true' fallbacks (turn failures into lies)
     - No long-running processes ('next dev', 'expo start', 'vitest' without 'run')
     - No Playwright / Cypress / Puppeteer (no browser binaries available)
     - Prefer 'pnpm --filter <pkg> <script>' over 'cd dir && cmd'
     - Prefer direct binaries ('tsc', 'vitest run') over 'npx'
     - file_exists OK for config artifacts (package.json, ci.yml) but pair with build/test for source files
     - 3-5 ACs per session is the sweet spot; the FIRST AC should be a build/test that catches compilation
     - If an AC uses a tool, that tool must be in package.json or stack.infra

  d. DEPENDENCIES: only reference task IDs in EARLIER sessions (per the session.inputs hints) or earlier tasks IN THIS session. Forward references break the DAG.

  e. SCAFFOLDING: every declared deliverable surface needs scaffolding tasks. Next.js app → app/layout.tsx, app/page.tsx, per-route page.tsx, next.config.js, tailwind.config.ts. Expo app → App.tsx, navigation, per-screen, app.json, metro.config.js. Backend → server entry, route registration, per-endpoint handler, middleware. Don't ship "implement the X app" as one task — apps are dozens of tasks.

SESSION TO EXPAND (id, title, description, outputs are FIXED; you are filling in tasks + ACs):
`

// ConvertProseToSOWChunked is the chunked alternative to
// ConvertProseToSOW. It runs in three phases:
//
//   1. Skeleton extraction: small LLM call returns project + sessions
//      (titles + descriptions + outputs only).
//   2. Per-session expansion: one LLM call per session, parallel up
//      to maxParallel. Each call returns full tasks + ACs.
//   3. Reassembly + validation.
//
// Failure handling: per-call timeout (10 min default per call), one
// retry per session on transient errors. A session that exhausts
// retries gets a stand-in note in its description and an empty
// task list, surfaced in the returned SOW so the operator sees
// what failed rather than aborting the whole conversion.
//
// Returns the parsed SOW + raw JSON of the assembled result.
func ConvertProseToSOWChunked(ctx context.Context, prose string, prov provider.Provider, model string, maxParallel int) (*SOW, []byte, error) {
	if strings.TrimSpace(prose) == "" {
		return nil, nil, fmt.Errorf("empty prose")
	}
	if prov == nil {
		return nil, nil, fmt.Errorf("no provider configured")
	}
	if maxParallel < 1 {
		maxParallel = 4
	}

	// Phase 1: skeleton.
	skel, err := extractSkeleton(ctx, prose, prov, model)
	if err != nil {
		return nil, nil, fmt.Errorf("skeleton extraction: %w", err)
	}
	fmt.Printf("  ⚡ chunked convert: skeleton has %d sessions; expanding each in parallel (max %d)\n", len(skel.Sessions), maxParallel)

	// Phase 2: per-session expansion. Worker pool bounded by
	// maxParallel; each worker runs expandSession with timeout +
	// one retry.
	type result struct {
		idx     int
		session Session
		err     error
	}
	expanded := make([]Session, len(skel.Sessions))
	results := make(chan result, len(skel.Sessions))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, stub := range skel.Sessions {
		wg.Add(1)
		go func(i int, stub Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			full, err := expandSessionWithRetry(ctx, prose, &skel.Stack, stub, prov, model)
			elapsed := time.Since(start).Round(time.Second)
			if err != nil {
				fmt.Printf("     ⚠ session %s expand failed (%s): %v\n", stub.ID, elapsed, err)
				// Placeholder so reassembly stays consistent and the
				// operator sees what failed.
				full = stub
				full.Description = stub.Description + " [EXPAND FAILED: " + err.Error() + "]"
			} else {
				fmt.Printf("     ✓ session %s expanded in %s (%d tasks, %d ACs)\n", stub.ID, elapsed, len(full.Tasks), len(full.AcceptanceCriteria))
			}
			results <- result{idx: i, session: full, err: err}
		}(i, stub)
	}
	wg.Wait()
	close(results)
	for r := range results {
		expanded[r.idx] = r.session
	}

	// Phase 3: reassemble + validate.
	out := &SOW{
		ID:          skel.ID,
		Name:        skel.Name,
		Description: skel.Description,
		Stack:       skel.Stack,
		Sessions:    expanded,
	}
	autoFillMissingACFields(out)
	autoCleanTaskDeps(out)
	autoAddMissingInfra(out)
	if errs := ValidateSOW(out); len(errs) > 0 {
		return out, nil, fmt.Errorf("chunked convert produced invalid SOW: %s", strings.Join(errs, "; "))
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	return out, raw, nil
}

// extractSkeleton issues the phase-1 LLM call and parses the
// skeleton SOW.
func extractSkeleton(ctx context.Context, prose string, prov provider.Provider, model string) (*SOW, error) {
	userText := chunkedSkeletonPrompt + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	skelCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(skelCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 16000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("skeleton truncated at max_tokens — increase budget")
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("skeleton returned no usable content (stop_reason=%q)", resp.StopReason)
	}
	var skel SOW
	if _, err := jsonutil.ExtractJSONInto(raw, &skel); err != nil {
		return nil, fmt.Errorf("parse skeleton: %w", err)
	}
	if len(skel.Sessions) == 0 {
		return nil, fmt.Errorf("skeleton produced zero sessions")
	}
	return &skel, nil
}

// expandSessionWithRetry runs expandSession with one retry on
// transient error. Per-call timeout is enforced inside expandSession.
func expandSessionWithRetry(ctx context.Context, prose string, stack *StackSpec, stub Session, prov provider.Provider, model string) (Session, error) {
	full, err := expandSession(ctx, prose, stack, stub, prov, model)
	if err == nil {
		return full, nil
	}
	// One retry on first failure (covers transient LLM glitches /
	// rate limits without compounding wall clock).
	full, err2 := expandSession(ctx, prose, stack, stub, prov, model)
	if err2 != nil {
		return Session{}, fmt.Errorf("first attempt: %v; retry: %v", err, err2)
	}
	return full, nil
}

// expandSession issues one phase-2 LLM call to fill in tasks + ACs
// for a single session stub.
func expandSession(ctx context.Context, prose string, stack *StackSpec, stub Session, prov provider.Provider, model string) (Session, error) {
	stubBlob, err := json.MarshalIndent(stub, "", "  ")
	if err != nil {
		return Session{}, fmt.Errorf("marshal stub: %w", err)
	}
	stackBlob, _ := json.MarshalIndent(stack, "", "  ")
	userText := chunkedSessionPrompt + string(stubBlob) +
		"\n\nSTACK CONTEXT:\n" + string(stackBlob) +
		"\n\nFULL SPEC PROSE (find the parts relevant to this session):\n" + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	sessCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(sessCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 32000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return Session{}, err
	}
	if resp.StopReason == "max_tokens" {
		return Session{}, fmt.Errorf("session truncated at max_tokens — session may be too large for one expansion")
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return Session{}, fmt.Errorf("empty response (stop_reason=%q)", resp.StopReason)
	}
	var sess Session
	if _, err := jsonutil.ExtractJSONInto(raw, &sess); err != nil {
		return Session{}, fmt.Errorf("parse session: %w", err)
	}
	// Force the ID + outputs to match the stub even if the model
	// drifted — these are load-bearing for the DAG.
	sess.ID = stub.ID
	if len(sess.Outputs) == 0 {
		sess.Outputs = stub.Outputs
	}
	if strings.TrimSpace(sess.Title) == "" {
		sess.Title = stub.Title
	}
	if strings.TrimSpace(sess.Description) == "" {
		sess.Description = stub.Description
	}
	return sess, nil
}

// callChatWithCtx wraps prov.Chat with a context-aware timeout.
// The provider interface itself is synchronous; this goroutine +
// channel pattern lets the caller bail out when ctx is canceled
// (e.g. our 10-minute per-call timeout) instead of blocking
// indefinitely on a hung LiteLLM upstream — the run-20 failure
// mode where the conversion call never returned.
func callChatWithCtx(ctx context.Context, prov provider.Provider, req provider.ChatRequest) (*provider.ChatResponse, error) {
	type chatResult struct {
		resp *provider.ChatResponse
		err  error
	}
	ch := make(chan chatResult, 1)
	go func() {
		resp, err := prov.Chat(req)
		ch <- chatResult{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("chat timed out after %v: %w", deadlineDuration(ctx), ctx.Err())
	case r := <-ch:
		return r.resp, r.err
	}
}

// deadlineDuration returns how long the context allows from now
// until its deadline (or "no deadline" when it has none). Cosmetic
// helper for the timeout error message.
func deadlineDuration(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return time.Until(d).Round(time.Second)
	}
	return 0
}
