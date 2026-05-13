// Package bitbucket is the BitBucket Pipelines + Pull Requests adapter (C5).
//
// It complements the YAML template generator in `internal/cicd` (which renders
// `bitbucket-pipelines.yml` files) by giving the agent a runtime API surface
// to trigger pipelines, poll status, fetch logs, post PR comments, and write
// commit statuses against api.bitbucket.org (Bitbucket Cloud) or a Bitbucket
// Data Center on-prem instance.
//
// Auth: workspace API tokens (bearer) or basic auth (email + token). Both
// patterns are documented in internal/skill/builtin/integrations/bitbucket.md
// and supported via `Config.BasicAuth`. OIDC-based exchange (preferred inside
// a pipeline step) is in auth.go.
//
// Stdlib only. Mirrors the GitLab adapter's structure (Config, Client, do,
// doRaw, applyAuth, APIError, IsNotFound, IsUnauthorized) so the parity-audit
// test (T23) sees the same surface across all three adapters.
package bitbucket

import "time"

// Pipeline mirrors the trimmed Bitbucket v2.0 pipeline payload.
type Pipeline struct {
	UUID        string    `json:"uuid"`
	BuildNumber int       `json:"build_number"`
	State       State     `json:"state"`
	Repository  RepoRef   `json:"repository,omitempty"`
	Target      Target    `json:"target,omitempty"`
	CreatedOn   time.Time `json:"created_on,omitempty"`
	CompletedOn time.Time `json:"completed_on,omitempty"`
	DurationSec int64     `json:"duration_in_seconds,omitempty"`
}

// IsTerminal reports whether p has reached a final state. The Bitbucket
// pipeline state machine collapses to COMPLETED for every final outcome —
// success, failure, error, and stopped all share that name and discriminate
// via the `Result.Name` substate.
func (p Pipeline) IsTerminal() bool {
	return p.State.Name == "COMPLETED"
}

// State is the wire shape of a pipeline / step state.
type State struct {
	Name   string      `json:"name"` // PENDING | IN_PROGRESS | COMPLETED
	Result StateResult `json:"result,omitempty"`
}

// StateResult discriminates the terminal flavor of State.Name == "COMPLETED".
type StateResult struct {
	Name string `json:"name"` // SUCCESSFUL | FAILED | STOPPED | ERROR
}

// RepoRef is the repository-reference shape embedded in pipelines.
type RepoRef struct {
	Type     string `json:"type,omitempty"`
	FullName string `json:"full_name,omitempty"`
	UUID     string `json:"uuid,omitempty"`
}

// Target is the trigger target embedded in pipelines (branch, commit, custom).
type Target struct {
	Type     string   `json:"type,omitempty"`
	RefType  string   `json:"ref_type,omitempty"`
	RefName  string   `json:"ref_name,omitempty"`
	Commit   Commit   `json:"commit,omitempty"`
	Selector Selector `json:"selector,omitempty"`
}

// Commit is the trimmed commit reference inside a Target.
type Commit struct {
	Hash string `json:"hash,omitempty"`
	Type string `json:"type,omitempty"`
}

// Selector controls custom-pipeline dispatch.
type Selector struct {
	Type    string `json:"type,omitempty"`    // branches | custom | tags | bookmarks
	Pattern string `json:"pattern,omitempty"` // matches the pipeline key under `custom:`
}

// Step is one step inside a pipeline.
type Step struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name,omitempty"`
	State  State  `json:"state"`
	LogURL string `json:"-"` // synthesized by the client
}

// CommitStatus is the shape for POST /commit/{sha}/statuses/build.
type CommitStatus struct {
	Key         string `json:"key"`         // "r1-verify"
	State       string `json:"state"`       // INPROGRESS | SUCCESSFUL | FAILED | STOPPED
	Name        string `json:"name"`        // shared.CommitStatusName == "R1 Verify"
	URL         string `json:"url"`
	Description string `json:"description"` // <= 140 chars; truncate client-side
}

// PRCommentInline is the optional inline-anchor block on a PR comment.
type PRCommentInline struct {
	Path string `json:"path"`
	To   int    `json:"to,omitempty"`   // new-side line
	From int    `json:"from,omitempty"` // old-side line
}

// PRComment is the shape for POST /pullrequests/{id}/comments.
type PRComment struct {
	Content PRCommentContent `json:"content"`
	Inline  *PRCommentInline `json:"inline,omitempty"`
}

// PRCommentContent wraps the raw markdown body.
type PRCommentContent struct {
	Raw string `json:"raw"`
}

// PullRequest is the trimmed PR shape used for HEAD-commit lookup.
type PullRequest struct {
	ID          int        `json:"id"`
	Title       string     `json:"title,omitempty"`
	State       string     `json:"state,omitempty"` // OPEN | MERGED | DECLINED | SUPERSEDED
	Source      PRBranch   `json:"source,omitempty"`
	Destination PRBranch   `json:"destination,omitempty"`
}

// PRBranch is the source/destination wrapper inside a PullRequest payload.
type PRBranch struct {
	Branch Branch `json:"branch,omitempty"`
	Commit Commit `json:"commit,omitempty"`
}

// Branch is the trimmed branch reference embedded in a PRBranch.
type Branch struct {
	Name string `json:"name,omitempty"`
}

// triggerPayload is the body of POST /repositories/{w}/{r}/pipelines/.
type triggerPayload struct {
	Target    Target       `json:"target"`
	Variables []triggerVar `json:"variables,omitempty"`
}

// triggerVar is one CI variable on a pipeline trigger.
type triggerVar struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Secured bool   `json:"secured"`
}
