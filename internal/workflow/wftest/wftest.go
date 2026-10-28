// Package wftest is a scripted workflow.App for workflow tests: prompts
// answer from queues, status messages are recorded, and the GitHub
// client is the fake. The original tests replaced ui_confirm /
// ui_prompt / ui_pick the same way.
package wftest

import (
	"context"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// App is the scripted app.
type App struct {
	mu sync.Mutex

	Cfg     *config.Config
	Effects *fx.Fx
	Fake    *fake.Client
	User    string
	RepoL   []model.Repo

	// Scripted answers, consumed in order; an empty queue answers false /
	// "" (cancel) / -1.
	Confirms []bool
	Prompts  []string
	Picks    []int

	// What was asked, in order.
	Questions []string
	Prompted  []string
	PromptOps []workflow.PromptOpts
	Menus     [][]string

	// Status messages, in order, as "ok: ...", "notice: ...", "fail: ...",
	// "status: ...", "flash: ...".
	Log []string

	Celebrated []bool
	Launched   int
	Refreshes  []bool
	Soon       []time.Duration

	States    map[string]any
	PRs       []*model.PR
	IssueList []*model.Issue
	Approved  map[model.Ident]string
	DetailItm any
	Listed    *model.PR
	Kind      string
	ViewName  string
	RowList   []workflow.Row
	Sel       int
	WidthN    int
	Bumps     int
	Opened    []string

	// Editor scripts EditInEditor: returns (text, ok).
	Editor func(seed string) (string, bool)
	// Runner scripts RunExternal.
	Runner func(argv []string) error
}

// New makes an app on the fake client as user.
func New(cfg *config.Config, user string, repos ...model.Repo) *App {
	if cfg == nil {
		cfg = config.Default()
	}
	if len(repos) == 0 {
		repos = cfg.Reviews.Repos
	}
	return &App{Cfg: cfg, Effects: fx.New(), Fake: fake.New(user), User: user, RepoL: repos, States: map[string]any{},
		Approved: map[model.Ident]string{}, ViewName: "detail", Kind: "pr", WidthN: 120, Sel: -1}
}

var _ workflow.App = (*App)(nil)

func (a *App) log(s string) {
	a.mu.Lock()
	a.Log = append(a.Log, s)
	a.mu.Unlock()
}

// Last returns the last log line, or "".
func (a *App) Last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.Log) == 0 {
		return ""
	}
	return a.Log[len(a.Log)-1]
}

func (a *App) Confirm(q string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Questions = append(a.Questions, q)
	if len(a.Confirms) == 0 {
		return false
	}
	v := a.Confirms[0]
	a.Confirms = a.Confirms[1:]
	return v
}

func (a *App) Prompt(p string, o workflow.PromptOpts) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Prompted = append(a.Prompted, p)
	a.PromptOps = append(a.PromptOps, o)
	if len(a.Prompts) == 0 {
		return "", false
	}
	v := a.Prompts[0]
	a.Prompts = a.Prompts[1:]
	if o.OnChange != nil {
		o.OnChange(v)
	}
	return v, true
}

func (a *App) Pick(title string, labels []string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Menus = append(a.Menus, append([]string{title}, labels...))
	if len(a.Picks) == 0 {
		return -1
	}
	v := a.Picks[0]
	a.Picks = a.Picks[1:]
	return v
}

func (a *App) OK(msg string)     { a.log("ok: " + msg) }
func (a *App) Notice(msg string) { a.log("notice: " + msg) }
func (a *App) Fail(msg string)   { a.log("fail: " + msg) }
func (a *App) Status(msg string) { a.log("status: " + msg) }
func (a *App) Flash(msg string)  { a.log("flash: " + msg) }
func (a *App) Celebrate(big bool) {
	a.mu.Lock()
	a.Celebrated = append(a.Celebrated, big)
	a.mu.Unlock()
}
func (a *App) Launch()          { a.mu.Lock(); a.Launched++; a.mu.Unlock() }
func (a *App) GH() ghapi.Client { return a.Fake }
func (a *App) Me() string       { return a.User }
func (a *App) Repos() []model.Repo {
	return a.RepoL
}
func (a *App) MultiRepo() bool        { return len(a.RepoL) > 1 }
func (a *App) Config() *config.Config { return a.Cfg }
func (a *App) State(name string) any  { a.mu.Lock(); defer a.mu.Unlock(); return a.States[name] }
func (a *App) SetState(name string, s any) {
	a.mu.Lock()
	a.States[name] = s
	a.mu.Unlock()
}
func (a *App) AllPRs() []*model.PR    { return a.PRs }
func (a *App) Issues() []*model.Issue { return a.IssueList }
func (a *App) LocalApproved(pr *model.PR) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	head, ok := a.Approved[pr.Ident()]
	return ok && (head == "" || pr.HeadRefOid == "" || head == pr.HeadRefOid)
}
func (a *App) MarkApproved(pr *model.PR) {
	a.mu.Lock()
	a.Approved[pr.Ident()] = pr.HeadRefOid
	a.mu.Unlock()
}
func (a *App) Detail() any             { return a.DetailItm }
func (a *App) DetailListed() *model.PR { return a.Listed }
func (a *App) DetailKind() string      { return a.Kind }
func (a *App) CurrentView() string     { return a.ViewName }
func (a *App) Rows() []workflow.Row    { return a.RowList }
func (a *App) Selected() (workflow.Row, bool) {
	if a.Sel >= 0 && a.Sel < len(a.RowList) && a.RowList[a.Sel].Kind != "header" {
		return a.RowList[a.Sel], true
	}
	return workflow.Row{}, false
}
func (a *App) Now() time.Time { return clock.Now() }
func (a *App) Refresh(again bool) {
	a.mu.Lock()
	a.Refreshes = append(a.Refreshes, again)
	a.mu.Unlock()
}
func (a *App) RefreshSoon(d time.Duration) { a.mu.Lock(); a.Soon = append(a.Soon, d); a.mu.Unlock() }
func (a *App) Width() int                  { return a.WidthN }
func (a *App) Fx() *fx.Fx                  { return a.Effects }
func (a *App) Context() context.Context    { return context.Background() }
func (a *App) BumpDetail()                 { a.mu.Lock(); a.Bumps++; a.mu.Unlock() }
func (a *App) EditInEditor(seed string) (string, bool, error) {
	if a.Editor == nil {
		return seed, true, nil
	}
	t, ok := a.Editor(seed)
	return t, ok, nil
}
func (a *App) RunExternal(argv []string) error {
	if a.Runner == nil {
		return nil
	}
	return a.Runner(argv)
}
func (a *App) OpenDetail(repo model.Repo, number int, kind string, listed any) {
	a.mu.Lock()
	a.Opened = append(a.Opened, model.Ref(repo, number, true)+":"+kind)
	a.mu.Unlock()
}
