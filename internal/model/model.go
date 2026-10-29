// Package model holds the GitHub data as the dashboard sees it: pull
// requests, issues, comments, reviews and check contexts, each tagged
// with the repository it came from. Workflows attach their own state to
// an item through Ext, keyed by workflow name, so the core never imports
// a workflow package.
package model

import (
	"fmt"
	"strings"
	"time"
)

// Repo is an "owner/name" pair.
type Repo struct {
	Owner string
	Name  string
}

// ParseRepo splits "owner/name"; ok is false for anything else.
func ParseRepo(s string) (r Repo, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 || strings.Count(s, "/") != 1 {
		return Repo{}, false
	}
	return Repo{Owner: s[:i], Name: s[i+1:]}, true
}

// String is "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// IsZero reports an unset repo.
func (r Repo) IsZero() bool { return r.Owner == "" && r.Name == "" }

// Actor is a GitHub login.
type Actor struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

// Label is a label name.
type Label struct {
	Name string `json:"name"`
}

// Labels is a GraphQL label connection.
type Labels struct {
	Nodes []Label `json:"nodes"`
}

// Names lists the label names.
func (l Labels) Names() []string {
	out := make([]string, 0, len(l.Nodes))
	for _, n := range l.Nodes {
		out = append(out, n.Name)
	}
	return out
}

// Has reports whether a label is present (exact match).
func (l Labels) Has(name string) bool {
	for _, n := range l.Nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

// Comment is an issue or PR comment.
type Comment struct {
	Author    Actor     `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// Comments is a comment connection.
type Comments struct {
	TotalCount int       `json:"totalCount"`
	Nodes      []Comment `json:"nodes"`
}

// Review is a PR review.
type Review struct {
	Author      Actor     `json:"author"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
	Body        string    `json:"body"`
}

// Reviews is a review connection.
type Reviews struct {
	TotalCount int      `json:"totalCount"`
	Nodes      []Review `json:"nodes"`
}

// CheckContext is one entry of a commit's status-check rollup: either a
// CheckRun (Name/Status/Conclusion) or a StatusContext (Context/State/
// Description), told apart by Typename.
type CheckContext struct {
	Typename    string `json:"__typename"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Conclusion  string `json:"conclusion,omitempty"`
	Context     string `json:"context,omitempty"`
	State       string `json:"state,omitempty"`
	Description string `json:"description,omitempty"`
}

// Rollup is a commit's statusCheckRollup.
type Rollup struct {
	State    string `json:"state"`
	Contexts struct {
		TotalCount int            `json:"totalCount"`
		Nodes      []CheckContext `json:"nodes"`
	} `json:"contexts"`
}

// Commit is the last commit of a PR as the list query reads it.
type Commit struct {
	CommittedDate time.Time `json:"committedDate"`
	Rollup        *Rollup   `json:"statusCheckRollup"`
}

type commitNode struct {
	Commit Commit `json:"commit"`
}

// Commits is the commits(last: 1) connection.
type Commits struct {
	Nodes []commitNode `json:"nodes"`
}

// Last is the newest commit, or nil.
func (c Commits) Last() *Commit {
	if len(c.Nodes) == 0 {
		return nil
	}
	return &c.Nodes[0].Commit
}

// OpinionatedReviews is latestOpinionatedReviews.
type OpinionatedReviews struct {
	Nodes []Review `json:"nodes"`
}

// MergeCommit is mergeCommit { oid }.
type MergeCommit struct {
	Oid string `json:"oid"`
}

// PR is a pull request as the list, detail and release queries return it.
// Fields the list query does not fetch stay zero until the detail loads.
type PR struct {
	Repo Repo `json:"-"`

	Number           int                `json:"number"`
	Title            string             `json:"title"`
	Body             string             `json:"body"`
	IsDraft          bool               `json:"isDraft"`
	ReviewDecision   string             `json:"reviewDecision"`
	State            string             `json:"state"`
	UpdatedAt        time.Time          `json:"updatedAt"`
	CreatedAt        time.Time          `json:"createdAt"`
	MergedAt         *time.Time         `json:"mergedAt"`
	HeadRefName      string             `json:"headRefName"`
	HeadRefOid       string             `json:"headRefOid"`
	BaseRefName      string             `json:"baseRefName"`
	Additions        int                `json:"additions"`
	Deletions        int                `json:"deletions"`
	ChangedFiles     int                `json:"changedFiles"`
	MergeStateStatus string             `json:"mergeStateStatus"`
	MergeCommit      *MergeCommit       `json:"mergeCommit"`
	Author           Actor              `json:"author"`
	Labels           Labels             `json:"labels"`
	Opinions         OpinionatedReviews `json:"latestOpinionatedReviews"`
	Commits          Commits            `json:"commits"`
	Comments         Comments           `json:"comments"`
	Reviews          Reviews            `json:"reviews"`

	// Ext carries per-workflow annotations, keyed by workflow name.
	Ext map[string]any `json:"-"`
}

// Issue is an issue as the list and detail queries return it.
type Issue struct {
	Repo Repo `json:"-"`

	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
	Author    Actor     `json:"author"`
	Labels    Labels    `json:"labels"`
	Assignees struct {
		Nodes []Actor `json:"nodes"`
	} `json:"assignees"`
	Comments Comments `json:"comments"`

	Ext map[string]any `json:"-"`
}

// Ident identifies a row across refreshes: (repo, number). The repo
// carries its owner, so acme/api#5 and beta/api#5 are different rows.
type Ident struct {
	Repo   Repo
	Number int
}

// String is "owner/name#N".
func (id Ident) String() string { return fmt.Sprintf("%s#%d", id.Repo, id.Number) }

// Ident is the PR's row identity.
func (p *PR) Ident() Ident { return Ident{p.Repo, p.Number} }

// Ident is the issue's row identity.
func (i *Issue) Ident() Ident { return Ident{i.Repo, i.Number} }

// ShortSHA is the first seven characters of a commit SHA, the length
// GitHub shows; "?" for an empty one.
func ShortSHA(sha string) string {
	if sha == "" {
		return "?"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Ref is "#N" with one repo configured, "repo#N" with several.
func Ref(repo Repo, number int, multi bool) string {
	if multi {
		return fmt.Sprintf("%s#%d", repo.Name, number)
	}
	return fmt.Sprintf("#%d", number)
}

// HeadCommit is the PR's newest commit, or nil.
func (p *PR) HeadCommit() *Commit { return p.Commits.Last() }

// Get reads a workflow annotation.
func (p *PR) Get(key string) any {
	if p.Ext == nil {
		return nil
	}
	return p.Ext[key]
}

// Set writes a workflow annotation.
func (p *PR) Set(key string, v any) {
	if p.Ext == nil {
		p.Ext = map[string]any{}
	}
	p.Ext[key] = v
}

// MyReviewState is the viewer's latest opinionated review state ("" when
// none), compared case-insensitively by login.
func (p *PR) MyReviewState(me string) string {
	for _, r := range p.Opinions.Nodes {
		if strings.EqualFold(r.Author.Login, me) {
			return r.State
		}
	}
	return ""
}

// RateLimit is GraphQL's rateLimit {} block.
type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// WorkflowRun is the subset of an Actions run the dashboard reads.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	RunAttempt int       `json:"run_attempt"`
	Event      string    `json:"event"`
	// DisplayTitle is the run's title as the Actions page shows it (a
	// workflow's run-name, else the commit headline).
	DisplayTitle string `json:"display_title"`
}

// WorkflowJob is one job of an Actions run with its steps, as
// GET /actions/runs/{id}/jobs returns them (the latest attempt).
type WorkflowJob struct {
	ID          int64          `json:"id"`
	RunID       int64          `json:"run_id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`     // queued, in_progress, completed, waiting
	Conclusion  string         `json:"conclusion"` // success, failure, cancelled, skipped, ...
	HTMLURL     string         `json:"html_url"`
	RunAttempt  int            `json:"run_attempt"`
	StartedAt   time.Time      `json:"started_at"`
	CompletedAt time.Time      `json:"completed_at"` // zero while the job runs
	Steps       []WorkflowStep `json:"steps"`
}

// WorkflowStep is one step of a job: its name as the workflow file
// names it, and when it ran.
type WorkflowStep struct {
	Number      int       `json:"number"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"` // zero until the step ends
}

// PendingDeployment is one environment a run waits on.
type PendingDeployment struct {
	Environment struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"environment"`
	WaitTimer             int    `json:"wait_timer"`
	WaitTimerStartedAt    string `json:"wait_timer_started_at"`
	CurrentUserCanApprove bool   `json:"current_user_can_approve"`
}

// PRFile is one changed file of a PR.
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

// RepoListing is an org repo as org_repos returns it.
type RepoListing struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// Ruleset is a repo ruleset summary.
type Ruleset struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
}

// NewCommits builds a commits(last: 1) connection around one commit
// (fixtures and tests).
func NewCommits(c Commit) Commits {
	return Commits{Nodes: []commitNode{{Commit: c}}}
}
