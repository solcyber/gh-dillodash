// Package workflow defines how an optional workflow (a comment protocol a
// bot in your repos speaks) plugs into the dashboard. The core knows the
// generic pull request: a workflow can claim items for its own list
// sections, classify them with its own chips, add cards to the detail
// view, take over the approve key, gate merges and deployments, add keys
// of its own, highlight its markdown, and place things on the fortress
// map. Everything is optional: embed Base and override what you need.
package workflow

import (
	"context"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/markdown"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// Classification is a PR's chip as a workflow sees it.
type Classification struct {
	Label    string // chip text, e.g. "applied, needs review"
	Style    string // theme style name, e.g. "chip.await"
	NeedsYou bool   // counted in "N need you"; the title drawn gold
	Done     bool   // the title drawn dimmed (approved by me, draft)
}

// Row is one list row: a section header (Kind "header", Item a string)
// or an item of some kind ("pr", "issue", or a workflow's own kind).
type Row struct {
	Kind string
	Item any
	// Meta carries whatever the kind's renderer needs (a night for a
	// sub-header, a workflow state for a synthetic row).
	Meta map[string]any
}

// Ident identifies a row across refreshes.
func (r Row) Ident() model.Ident {
	switch v := r.Item.(type) {
	case *model.PR:
		return v.Ident()
	case *model.Issue:
		return v.Ident()
	case Identer:
		return v.Ident()
	}
	return model.Ident{}
}

// Identer is implemented by workflow items that are neither PRs nor issues.
type Identer interface{ Ident() model.Ident }

// Layout is a row kind's column widths for one frame, by column name.
type Layout map[string]int

// RowKind renders rows of one kind.
type RowKind struct {
	Icon string // glyph name
	// Layout computes the column widths for the rows of this kind at
	// width w; numW is the "#number" column width shared by every kind.
	Layout func(app App, items []Row, w, numW, repoW int) Layout
	// Segs renders one row (after the gutter); pos is the row's place on
	// screen, which staggers ambient effects.
	Segs func(app App, row Row, L Layout, w, pos int) []text.Seg
	// URL is the row's web URL, or "".
	URL func(app App, row Row) string
	// Open handles Enter on the row; false lets the core open the item.
	Open func(app App, row Row) bool
	// Title is the row's plain title (the core renders rows through Segs
	// and does not read it; a workflow may use it for its own rows).
	Title func(row Row) string
	// Live reports a row whose rendering changes between refreshes (a
	// release in flight): the dashboard keeps its frame clock running
	// for it instead of sleeping until the next refresh.
	Live func(app App, row Row) bool
}

// Key is a key binding a workflow adds.
type Key struct {
	View     string // "list" | "detail" | "issue" | "any"
	Ch       string // the key, e.g. "w", "M", "D"
	Label    string // key-bar label
	Priority int    // key-bar order; lower is kept longer when narrow
	// Handler runs on the action goroutine: it may block on prompts and
	// GitHub calls.
	Handler func(app App)
}

// InlinePattern is a markdown inline highlighter: a regexp matched
// against inline text, and the segments a match renders as.
type InlinePattern = markdown.InlinePattern

// Prompter is what an action may ask of the person: all three block the
// action goroutine until the dashboard has an answer.
type Prompter interface {
	// Confirm asks a y/N question.
	Confirm(question string) bool
	// Prompt reads a line; ok is false on Esc. Opts tune the prompt.
	Prompt(prompt string, opts PromptOpts) (string, bool)
	// Pick shows a menu; -1 on Esc.
	Pick(title string, labels []string) int
}

// PromptOpts tunes a line prompt.
type PromptOpts struct {
	Initial string
	// OnChange is called with the text as it is typed (a live filter).
	OnChange func(string)
	// MinLen, when > 0, draws the reason meter and refuses Enter below it;
	// Forbid lists characters the text may not contain.
	MinLen int
	Forbid string
	// Note is drawn under the prompt (what the reason is for).
	Note string
	// Refused marks the prompt as re-asked after a refusal (red footer).
	Refused bool
}

// App is what a workflow may call: the dashboard's services. The ui
// package implements it.
type App interface {
	Prompter
	// Status line.
	OK(msg string)
	Notice(msg string)
	Fail(msg string)
	Status(msg string)
	// Effects.
	Celebrate(big bool)
	Launch()
	Flash(msg string) // a message ahead of a blocking fetch
	// Data.
	GH() ghapi.Client
	Me() string
	Repos() []model.Repo
	MultiRepo() bool
	Config() *config.Config
	State(workflow string) any // the workflow's last applied state
	SetState(workflow string, s any)
	AllPRs() []*model.PR
	Issues() []*model.Issue
	LocalApproved(pr *model.PR) bool
	MarkApproved(pr *model.PR)
	Detail() any // *model.PR or *model.Issue, or nil
	DetailListed() *model.PR
	Selected() (Row, bool)
	Rows() []Row
	Now() time.Time
	// Refresh asks for a background refresh; again=true queues one even
	// while one is in flight.
	Refresh(again bool)
	// RefreshSoon pulls the next refresh in to at most d from now.
	RefreshSoon(d time.Duration)
	Width() int
	// Fx is the effect set, for row renderers (number pulses, glints).
	Fx() *fx.Fx
	// EditInEditor runs $EDITOR seeded with text; ok false on cancel.
	EditInEditor(seed string) (string, bool, error)
	// RunExternal runs argv on the terminal (a pager).
	RunExternal(argv []string) error
	// OpenDetail opens an item's detail view (on the action goroutine).
	OpenDetail(repo model.Repo, number int, kind string, listed any)
	// BumpDetail re-renders the detail view.
	BumpDetail()
	// CurrentView is "list" | "detail" | "files" | "patch" | "fortress".
	CurrentView() string
	DetailKind() string
	Context() context.Context
}

// IApproved reports a review approval of pr by the viewer that still
// stands: one posted with a this session (for the current head), or
// the viewer's review on GitHub. The core's a refuses such a PR unless
// a workflow takes it first; a workflow that does must not submit a
// second review.
func IApproved(app App, pr *model.PR) bool {
	return app.LocalApproved(pr) || pr.MyReviewState(app.Me()) == "APPROVED"
}

// Refresh results: a workflow computes State on the worker goroutine and
// hands it to Apply on the UI goroutine.
type State any

// QueuedProvider is implemented by a workflow state that holds items
// another workflow queued for the maintenance window (the release
// workflow's queued releases), so the window workflow can list them under
// their night without importing the release package.
type QueuedProvider interface {
	QueuedForWindow() []*model.PR
}

// ScheduledProvider is implemented by a workflow state that holds items
// already on a night's manifest (the scheduled label), so the window
// workflow keeps reading manifests for them after the freeze.
type ScheduledProvider interface {
	ScheduledForWindow() []*model.PR
}

// CIKey is the Ext key under which the core writes the CI cluster of a
// PR a workflow contributed through CIProvider.
const CIKey = "ci"

// CIProvider is implemented by a workflow state that lists PRs outside
// the review repos (the release workflow's releases) whose CI the core
// computes like every review PR's: the GraphQL rollup when the token can
// see it, else the Actions fallback, one cache and one call for all.
type CIProvider interface {
	CIPRs() []*model.PR
}

// DeployProvider is implemented by a workflow whose rows can carry
// deployments to approve (the release workflow's merged PRs, the waiting
// runs), so the list's key bar shows D while it is enabled.
type DeployProvider interface {
	ProvidesDeploys() bool
}

// RowDeployer is implemented by a workflow whose own list rows carry
// deployments D approves with no merged PR behind them (a waiting run).
// The core offers it the selected row before DeployTarget and the
// merged-PR path; handled false lets D go on. It runs on the action
// goroutine, so it may prompt and call GitHub.
type RowDeployer interface {
	DeployRow(app App, row Row) (handled bool)
}

// Poller is a workflow with items in flight that are cheaper to watch
// than a whole refresh is (a release moving through another repo's
// Actions run). Between refreshes, while Live says so, the dashboard
// runs Poll on its own goroutine every PollEvery and hands the patch to
// ApplyPoll on the UI goroutine. Poll reads its own state under its own
// lock and returns new values; it never touches the State the UI holds.
// err is only ever a rate limit, which pauses polling and refreshing
// alike; any other failure goes in the patch, for the poller's own card
// to show. A Poll with nothing to watch returns (nil, nil).
type Poller interface {
	PollEvery() time.Duration
	Live(app App) bool
	Poll(ctx context.Context, gh ghapi.Client) (patch any, err error)
	ApplyPoll(app App, patch any)
}

// ManifestItem is what a window workflow's state tells another workflow
// about one manifest item (a release queued for the night), without
// importing it: the item's id, its status text as the conductor writes
// it, the night, the manifest's phase, the slot (RFC 3339) and whether
// the item is final. Issue is the manifest issue's number.
type ManifestItem struct {
	ID, Status, Night, Phase, StartUTC string
	Final                              *bool
	Issue                              int
}

// ManifestItemProvider is implemented by a window workflow's state: the
// manifest item of a release PR (repo short name and number), newest
// night first.
type ManifestItemProvider interface {
	ManifestItem(repo string, number int) (ManifestItem, bool)
}

// NightRow is the Meta key a window sub-header row carries its night
// (YYYY-MM-DD) under.
const NightRow = "night"

// Workflow is the hook set. Base provides no-op defaults.
type Workflow interface {
	Name() string
	Order() int

	// Worker goroutine. err is only ever a rate limit (ghapi.IsRateLimit)
	// that must back the whole refresh off; anything else a workflow
	// meets is a warning, and the state it returns alongside is what it
	// could still assemble.
	Refresh(ctx context.Context, gh ghapi.Client, repos []model.Repo, prs []*model.PR, issues []*model.Issue) (State, []string, error)
	Annotate(prs []*model.PR, issues []*model.Issue, s State)

	// UI goroutine, after a refresh lands.
	Apply(app App, s State)
	AfterApply(app App)

	// Classification.
	Claims(row Row) bool
	Classify(pr *model.PR, listed *model.PR, me string, localApproved bool) *Classification
	SearchText(row Row) string
	BotLogins() []string
	ChipStyles() map[string]string
	NeedsYou() []string

	// List.
	Sections(app App, terms []string, me string) []Row
	RowKinds() map[string]RowKind
	HeaderExtra(app App, row Row, w int) (countdown string, segs []text.Seg)
	SummarySegs(app App) []text.Seg

	// Detail.
	EnrichDetail(ctx context.Context, gh ghapi.Client, pr *model.PR, listed *model.PR)
	DetailCards(app App, pr *model.PR, listed *model.PR, width int) []text.Line

	// Actions (action goroutine).
	Approve(app App, pr *model.PR, listed *model.PR) bool
	MergeBlock(pr *model.PR, me string) string
	MergeConfirm(app App, pr *model.PR) (ok bool, handled bool)
	DeployTarget(app App) *model.PR
	DeployComment(pr *model.PR, sha string) string
	Keys() []Key
	HelpRows() [][2]string
	LegendRows() [][]text.Seg

	// Markdown.
	InlinePatterns() []InlinePattern
	LineRenderer(line string, width int) []text.Line
	CodeLineSegs(line, lang string) []text.Seg

	// Fortress.
	FortressItems(app App, add func(item any))
	FortressLaneLabel(repo model.Repo) string
}

// Base is an empty workflow to embed.
type Base struct{}

func (Base) Name() string { return "" }
func (Base) Order() int   { return 50 }
func (Base) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (State, []string, error) {
	return nil, nil, nil
}
func (Base) Annotate([]*model.PR, []*model.Issue, State) {}
func (Base) Apply(App, State)                            {}
func (Base) AfterApply(App)                              {}
func (Base) Claims(Row) bool                             { return false }
func (Base) Classify(*model.PR, *model.PR, string, bool) *Classification {
	return nil
}
func (Base) SearchText(Row) string                                            { return "" }
func (Base) BotLogins() []string                                              { return nil }
func (Base) ChipStyles() map[string]string                                    { return nil }
func (Base) NeedsYou() []string                                               { return nil }
func (Base) Sections(App, []string, string) []Row                             { return nil }
func (Base) RowKinds() map[string]RowKind                                     { return nil }
func (Base) HeaderExtra(App, Row, int) (string, []text.Seg)                   { return "", nil }
func (Base) SummarySegs(App) []text.Seg                                       { return nil }
func (Base) EnrichDetail(context.Context, ghapi.Client, *model.PR, *model.PR) {}
func (Base) DetailCards(App, *model.PR, *model.PR, int) []text.Line           { return nil }
func (Base) Approve(App, *model.PR, *model.PR) bool                           { return false }
func (Base) MergeBlock(*model.PR, string) string                              { return "" }
func (Base) MergeConfirm(App, *model.PR) (bool, bool)                         { return false, false }
func (Base) DeployTarget(App) *model.PR                                       { return nil }
func (Base) DeployComment(*model.PR, string) string                           { return "" }
func (Base) Keys() []Key                                                      { return nil }
func (Base) HelpRows() [][2]string                                            { return nil }
func (Base) LegendRows() [][]text.Seg                                         { return nil }
func (Base) InlinePatterns() []InlinePattern                                  { return nil }
func (Base) LineRenderer(string, int) []text.Line                             { return nil }
func (Base) CodeLineSegs(string, string) []text.Seg                           { return nil }
func (Base) FortressItems(App, func(any))                                     {}
func (Base) FortressLaneLabel(model.Repo) string                              { return "" }

// Context lets workflows find each other at construction (explicit
// cross-workflow dependencies: the window's clock, the plan parser).
type Context struct {
	Config *config.Config
	Repos  []model.Repo
	byName map[string]Workflow
}

// Get returns an enabled workflow by name, or nil.
func (c *Context) Get(name string) Workflow {
	if c == nil {
		return nil
	}
	return c.byName[name]
}

// Constructor builds a workflow from the config; nil means "not enabled".
type Constructor func(ctx *Context) (Workflow, error)

// Registry maps config table names to constructors. Workflow packages
// register themselves in init().
var Registry = map[string]Constructor{}

// Register adds a constructor.
func Register(name string, c Constructor) { Registry[name] = c }
