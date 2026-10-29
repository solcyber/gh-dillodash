// Package ui is the dashboard: a Bubble Tea program whose model holds
// the state (list rows, detail lines, status, effects) and paints each
// frame onto a canvas. Actions that need answers (approve, comment,
// merge) run on their own goroutine and block on prompts the UI loop
// shows, so a flow reads top to bottom.
package ui

import (
	"context"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TickMS is the frame interval while something animates (spinners,
// running CI); tickMS() stretches it when nothing does and idleWakeMS()
// sleeps to the next countdown boundary or the refresh.
const TickMS = 100

// Views.
const (
	ViewList     = "list"
	ViewDetail   = "detail"
	ViewFiles    = "files"
	ViewPatch    = "patch"
	ViewFortress = "fortress"
)

type sigEntry struct {
	updated time.Time
	failing bool
}

type trailEntry struct {
	row  int
	when float64
}

type glideEntry struct {
	from, to int
	start    float64
}

type mdKey struct {
	body string
	wrap int
	raw  bool
}

type detailStamp struct {
	ver   int
	width int
	raw   bool
}

type patchStamp struct {
	sel   int
	width int
}

type hit struct {
	line, start, end int
}

// Model is the dashboard state. One instance; Update and View run on the
// Bubble Tea goroutine, actions on their own goroutine, and both take mu.
// The refresh worker takes nothing: it captures what it needs when it
// starts and hands its result back as a message.
type Model struct {
	mu sync.Mutex

	cfg   *config.Config
	gh    ghapi.Client
	wf    *workflow.Set
	me    string
	repos []model.Repo
	multi bool
	// multiOwner: the review repos span owners, so refs carry the owner
	multiOwner bool
	host       string
	ctx        context.Context
	prog       *tea.Program
	ver        string

	fx         *fx.Fx
	w, h       int
	view       string
	drawn      string // what the last frame showed (view wipes)
	detailFrom string

	// list
	rows     []workflow.Row
	selected int
	listTop  int
	showAll  bool
	filter   string
	allPRs   []*model.PR
	issues   []*model.Issue
	prs      []*model.PR // the filtered PRs shown

	// status
	loaded     bool
	errText    string
	warn       string
	status     string
	statusKind string // "ok" | "warn" | "info"
	statusAt   float64

	// refresh
	refreshing bool
	// polling is a workflow poll in flight; nextPollAt is when the next
	// one is due on the frame clock (0: nothing in flight to watch).
	// refreshSeq counts the refreshes applied: a poll that started before
	// the last one carries older news than the refresh did.
	polling         bool
	nextPollAt      float64
	refreshSeq      int
	refreshAgain    bool
	ciFlush         bool
	nextRefreshAt   float64
	lastRefresh     float64
	lastRefreshWall string
	backoff         ghapi.Backoff // the refresh loop's failure cadence and GitHub's wall
	ci              *ghapi.CIFallback
	ciClusters      map[model.Ident]model.CICluster

	approvedByMe map[model.Ident]string // ident -> head SHA at the time

	// detail
	detail       any // *model.PR | *model.Issue
	detailKind   string
	detailListed *model.PR
	detailLines  []text.Line
	detailVer    int
	detailStamp  detailStamp
	// mdMemo keeps the open item's rendered markdown while only its live
	// card moves (a release in flight rebuilds the detail every frame);
	// any other change to the detail clears it.
	mdMemo         map[mdKey][]text.Line
	detailLoadedAt float64
	scroll         int
	raw            bool

	// files / patch
	files       []model.PRFile
	fileSel     int
	filesTop    int
	patchLines  []text.Line
	patchScroll int
	patchStamp  patchStamp

	// search
	searchQ   string
	searchCur int
	hits      []hit
	hitsKey   string

	overlay   string // "help" while ? is up
	modal     widgets.Modal
	modalDone chan struct{}
	users     map[model.Repo][]model.Actor

	// effects
	sig       map[model.Ident]sigEntry
	trail     []trailEntry
	glide     map[string]glideEntry
	lastKey   float64
	ambient   bool
	animating bool

	wfState map[string]any

	// refreshStamp names the last landed refresh (the fortress rebuilds
	// its map when it changes)
	refreshStamp string
	// canvas is the frame buffer View reuses between frames
	canvas *canvas.Canvas
	// tickGen is the frame clock's generation (retick orphans old ticks)
	tickGen int
	// helpTop is the help/message overlay's scroll offset; lastMsg the
	// last status or error, whole, for the e overlay
	helpTop int
	lastMsg string
	// manualPending: r was pressed, so the next landing says "refreshed at"
	manualPending bool

	// the running action, if any; quitArmedAt is when q was last pressed
	// during one (a second press within quitArmSecs quits anyway)
	action      bool
	quitArmedAt float64

	// tp scripts prompt answers in tests; sendFn replaces the program
	tp     *testPrompter
	sendFn func(tea.Msg)

	fortress *fortressState

	deployCache deploys.Cache
}

// Options build a Model.
type Options struct {
	Config    *config.Config
	GH        ghapi.Client
	Workflows *workflow.Set
	Me        string
	Host      string
	Version   string
}

// New makes the model; Run starts it.
func New(ctx context.Context, o Options) *Model {
	repos := o.Config.Reviews.Repos
	m := &Model{
		cfg:             o.Config,
		gh:              o.GH,
		wf:              o.Workflows,
		me:              o.Me,
		repos:           repos,
		multi:           len(repos) > 1,
		multiOwner:      spansOwners(repos),
		host:            o.Host,
		ctx:             ctx,
		ver:             o.Version,
		fx:              fx.New(),
		view:            ViewList,
		detailFrom:      ViewList,
		statusKind:      "info",
		ci:              &ghapi.CIFallback{Recheck: time.Duration(o.Config.Reviews.CIRecheckSecs) * time.Second},
		backoff:         ghapi.Backoff{Refresh: time.Duration(o.Config.Display.RefreshSecs) * time.Second},
		deployCache:     deploys.Cache{StuckAfter: deployStuck(o.Config)},
		ciClusters:      map[model.Ident]model.CICluster{},
		approvedByMe:    map[model.Ident]string{},
		users:           map[model.Repo][]model.Actor{},
		glide:           map[string]glideEntry{},
		wfState:         map[string]any{},
		lastKey:         fx.Now(),
		lastRefreshWall: "never",
		w:               80,
		h:               24,
	}
	if m.wf == nil {
		m.wf, _ = workflow.NewSet(nil)
	}
	return m
}

// ---- status line (the newest message wins) ----

// setStatus sets an info message; it supersedes (clears) an older error.
// A message stays up for ToastSecs, or until a newer one replaces it.
func (m *Model) setStatus(msg string) {
	m.status = msg
	m.statusKind = "info"
	m.statusAt = fx.Now()
	if msg != "" {
		m.errText = ""
		m.lastMsg = msg
		m.fx.Start("toast", 0.25, nil)
	}
}

func (m *Model) ok(msg string) {
	m.setStatus(msg)
	m.statusKind = "ok"
}

// notice is a refusal or a cancellation: gold, with a shake, so it is seen.
func (m *Model) notice(msg string) {
	m.setStatus(msg)
	m.statusKind = "warn"
	m.fx.Start("toast.shake", 0.3, nil)
}

func (m *Model) fail(msg string) {
	m.errText = msg
	m.lastMsg = msg
	m.status = ""
	m.fx.Start("toast", 0.25, nil)
	m.fx.Start("toast.shake", 0.3, nil)
}

// celebrate is confetti for a posted approval; a smaller burst for a
// comment or a review. Only ever after the post succeeded.
func (m *Model) celebrate(big bool) {
	count := 16
	secs := 1.1
	if big {
		count, secs = 46, 1.7
	}
	m.fx.Start("confetti", secs, map[string]any{"seed": seed(), "count": count, "big": big})
}

// launch is the release launch. Only ever after a release posted; any
// key ends it (and still does what it does).
func (m *Model) launch() {
	m.fx.Stop("confetti")
	m.fx.Start("launch", fx.LaunchSecs, map[string]any{"seed": seed()})
}

func seed() uint64 { return uint64(time.Now().UnixNano()) }

// localApproved: approved via 'a' this session, for the PR's current head.
func (m *Model) localApproved(pr *model.PR) bool {
	head, ok := m.approvedByMe[pr.Ident()]
	if !ok {
		return false
	}
	return head == "" || pr.HeadRefOid == "" || pr.HeadRefOid == head
}

func (m *Model) iApproved(pr *model.PR) bool {
	return m.localApproved(pr) || pr.MyReviewState(m.me) == "APPROVED"
}

// repoOf is the repo an item belongs to (its own, else the first).
func (m *Model) repoOf(item any) model.Repo {
	switch v := item.(type) {
	case *model.PR:
		if !v.Repo.IsZero() {
			return v.Repo
		}
	case *model.Issue:
		if !v.Repo.IsZero() {
			return v.Repo
		}
	}
	if len(m.repos) > 0 {
		return m.repos[0]
	}
	return model.Repo{}
}

// reposText is "owner/name", or "owner/a, b, c" for several in one org,
// or "owner/a, other/b" across orgs.
func (m *Model) reposText() string {
	if len(m.repos) == 0 {
		return ""
	}
	if len(m.repos) == 1 {
		return m.repos[0].String()
	}
	same := true
	for _, r := range m.repos[1:] {
		if r.Owner != m.repos[0].Owner {
			same = false
		}
	}
	out := m.repos[0].String()
	for _, r := range m.repos[1:] {
		if same {
			out += ", " + r.Name
		} else {
			out += ", " + r.String()
		}
	}
	return out
}

// deployStuck is the release config's deploy_stuck_secs, or zero for
// the deploys package's default.
func deployStuck(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.ReleasePR != nil && cfg.ReleasePR.DeployStuckSecs > 0 {
		return time.Duration(cfg.ReleasePR.DeployStuckSecs) * time.Second
	}
	return 0
}

// deployRefresh is how soon to look again while a deploy waits or runs:
// the release config's deploy_refresh_secs, else the default.
func (m *Model) deployRefresh() time.Duration {
	if m.cfg != nil && m.cfg.ReleasePR != nil {
		return deploys.RefreshInterval(m.cfg.ReleasePR.DeployRefreshSecs)
	}
	return deploys.RefreshEvery
}

// spansOwners reports whether the repos belong to more than one owner.
func spansOwners(repos []model.Repo) bool {
	for _, r := range repos[min(1, len(repos)):] {
		if !strings.EqualFold(r.Owner, repos[0].Owner) {
			return true
		}
	}
	return false
}

// where names a PR or issue for headings and status messages: "#N" when
// repo is the only review repo, "repo#N" when several are configured or
// the item comes from elsewhere (a release repo), and "owner/repo#N"
// when the configured repos span owners.
func (m *Model) where(repo model.Repo, n int) string {
	if m.multiOwner {
		return model.Ident{Repo: repo, Number: n}.String()
	}
	foreign := len(m.repos) == 0 || repo != m.repos[0]
	return model.Ref(repo, n, m.multi || foreign)
}
