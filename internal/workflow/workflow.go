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
	// Sort orders rows of this kind in the refilter: smaller first.
	Title func(row Row) string
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

// NightRow is the Meta key a window sub-header row carries its night
// (YYYY-MM-DD) under.
const NightRow = "night"

// Workflow is the hook set. Base provides no-op defaults.
type Workflow interface {
	Name() string
	Order() int

	// Worker goroutine.
	Refresh(ctx context.Context, gh ghapi.Client, repos []model.Repo, prs []*model.PR, issues []*model.Issue) (State, []string)
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
func (Base) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (State, []string) {
	return nil, nil
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
