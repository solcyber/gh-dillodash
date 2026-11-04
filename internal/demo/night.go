package demo

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/ghapi/fake"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// The night demo (`--demo-night`) replays a manual release night in
// dillodash alone: after the freeze, an approved release the window
// bumped goes out with release NOW, then, once it is released, the next
// one in the lane (a database migration with a slot on the manifest):
// release NOW, the slot cancelled in the same flow, and the migration's
// two environments approved with D. The fixture moves on as the keys
// post: each post (and the refresh after the first release) is the
// conductor's and the release job's answer.

// The night's repos: the manifest repo, and the two release repos in
// lane order.
var (
	nightInfra = model.Repo{Owner: "acme", Name: "infra"}
	nightApp   = model.Repo{Owner: "acme", Name: "web-portal"}
	nightDB    = model.Repo{Owner: "acme", Name: "db-migrations"}
)

// NightEpoch is when the night demo is: five minutes before the start
// of the example window (config.DefaultMaintenanceWindow, 16:00 UTC),
// after its freeze, on a made-up night, with the migration's slot nine
// minutes into the window. The shots are drawn at this instant; the
// interactive replay starts at it and runs forward (NightClock), so the
// night reads the same whenever the demo runs.
var NightEpoch = time.Date(2031, 4, 16, 15, 55, 0, 0, time.UTC)

// The night's release numbers and the slot of the migration.
const (
	nightAppPR  = 212
	nightDBPR   = 64
	nightSlot   = 9 * time.Minute
	nightBudget = 25
)

// NightClock is the replay's clock: NightEpoch plus the time since the
// demo started. Install it as clock.Now.
func NightClock() func() time.Time {
	started := time.Now()
	return func() time.Time { return NightEpoch.Add(time.Since(started)) }
}

// Night stages.
const (
	NightStart       = iota // web-portal#212 bumped (approved), db-migrations#64 scheduled
	NightAppPosted          // release NOW posted on #212: dispatched
	NightAppReleased        // #212 released and deployed; #64 is next in the lane
	NightDBWaitsStg         // release NOW on #64: merged, its migration waits on stg
	NightDBWaitsProd        // stg approved: it waits on prod
	NightDBReleased         // prod approved: both released
	nightStages
)

// NightConfig is the night demo's config: the manifest repo reviewed,
// the two release repos in a lane, the window on, approvers checked.
func NightConfig() *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{nightInfra}
	cfg.GitHub.ApproversTeam = "acme/release-approvers"
	mw := config.DefaultMaintenanceWindow()
	mw.ManifestRepo = nightInfra
	mw.ManifestTitle = "Window {night} {hhmm} UTC"
	cfg.MaintenanceWindow = mw
	rp := config.DefaultReleasePR()
	rp.Auto = false
	rp.Repos = []model.Repo{nightApp, nightDB}
	rp.Org = "acme"
	rp.ApprovalQueuesForWindow = true
	rp.LaneOrder = []string{nightApp.Name, nightDB.Name}
	rp.ApplyBudgetMins = nightBudget
	cfg.ReleasePR = rp
	return cfg
}

// Night is the night demo's fake GitHub and where the night is.
type Night struct {
	GH *fake.Client

	mu    sync.Mutex
	stage int
	reads int // release reads since the stage began
	night string
	start time.Time
}

// NightClient is the fake at stage (NightStart for the demo); posts move
// it along.
func NightClient(stage int) *Night {
	n := &Night{GH: fake.New(Me)}
	n.night, n.start = tonight(NightConfig().MaintenanceWindow)
	n.GH.Rate = &model.RateLimit{Limit: 5000, Remaining: 4790, ResetAt: clock.Now().Add(41 * time.Minute)}
	n.GH.Teams = map[string][]string{"acme/release-approvers": {"alice", "carol"}}
	n.GH.Users = map[model.Repo][]model.Actor{nightInfra: {{Login: "alice"}, {Login: "bob"}, {Login: "carol"}}}
	n.GH.Open = map[model.Repo]ghapi.ListResult{nightInfra: {}}
	n.GH.Mutate(func() { n.load(stage) })
	n.GH.AfterCall = n.posted
	n.GH.BeforeOp = n.read
	return n
}

// tonight is the night under way in the configured window: today's,
// unless its start is more than twelve hours ahead (then the night
// that started yesterday is still the one under way), and its start.
func tonight(mw *config.MaintenanceWindowConfig) (string, time.Time) {
	local := mw.Local()
	now := clock.Now()
	day := local.Today(now)
	start := local.WallToUTC(day, mw.Start)
	if start.Sub(now) > 12*time.Hour {
		day = day.AddDate(0, 0, -1)
		start = local.WallToUTC(day, mw.Start)
	}
	return day.Format("2006-01-02"), start
}

// Stage is where the night is.
func (n *Night) Stage() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stage
}

// posted moves the night on: release NOW on the app, then on the
// migration; each deployment approval.
func (n *Night) posted(c fake.Call) {
	n.mu.Lock()
	next := n.stage
	switch {
	case c.Op == "comment" && strings.HasPrefix(c.Body, "/release-now") && c.Repo == nightApp && n.stage == NightStart:
		next = NightAppPosted
	case c.Op == "comment" && strings.HasPrefix(c.Body, "/release-now") && c.Repo == nightDB && n.stage == NightAppReleased:
		next = NightDBWaitsStg
	case c.Op == "approve_deployments" && (n.stage == NightDBWaitsStg || n.stage == NightDBWaitsProd):
		next = n.stage + 1
	}
	changed := next != n.stage
	n.stage, n.reads = next, 0
	n.mu.Unlock()
	if changed {
		n.GH.Mutate(func() { n.load(next) })
	}
}

// read lets the release job finish: the second release read after
// release NOW on the app (the refresh after the post shows it on its
// way, the next one shows it released).
func (n *Night) read(op string) {
	if op != "releases" {
		return
	}
	n.mu.Lock()
	n.reads++
	advance := n.stage == NightAppPosted && n.reads >= 2
	if advance {
		n.stage, n.reads = NightAppReleased, 0
	}
	n.mu.Unlock()
	if advance {
		n.GH.Mutate(func() { n.load(NightAppReleased) })
	}
}

// load writes the stage's answers into the fake (its lock held).
func (n *Night) load(stage int) {
	gh := n.GH
	slot := n.start.Add(nightSlot)
	appSHA, dbSHA := hexID(nightAppPR)[:40], hexID(nightDBPR)[:40]
	branch := "release/" + n.night

	app := pr(nightApp, nightAppPR, branch, "infra-bot", branch, 5*time.Hour, 1311, 402)
	app.ReviewDecision = "APPROVED"
	app.Opinions.Nodes = []model.Review{{Author: actor(Me), State: "APPROVED", SubmittedAt: at(4 * time.Hour)}}
	app.Commits = model.Commits{}
	db := pr(nightDB, nightDBPR, branch, "infra-bot", branch, 6*time.Hour, 96, 3)
	db.ReviewDecision = "APPROVED"
	db.Opinions.Nodes = []model.Review{{Author: actor(Me), State: "APPROVED", SubmittedAt: at(4 * time.Hour)}}
	db.Labels.Nodes = []model.Label{{Name: "window:scheduled"}}
	db.Commits = model.Commits{}

	nowCmd := func(reason string, age time.Duration) model.Comment {
		return model.Comment{Author: actor(Me), CreatedAt: at(age), Body: "/release-now \"" + reason + "\""}
	}
	outcome := func(head, sha string, age time.Duration) model.Comment {
		return bot(fmt.Sprintf("## %s\n\nOutcome: `%s`\nMerge: %s\nRun: https://github.com/acme/infra/actions/runs/%d\nApprover: @%s\n",
			head, map[bool]string{true: "released", false: "deploy_pending"}[head == "Released"], sha, 4070000+len(sha), Me), age)
	}
	run := func(id int64, name, status, conclusion, sha string) model.WorkflowRun {
		return model.WorkflowRun{ID: id, Name: name, Path: ".github/workflows/" + name + ".yml", Status: status, Conclusion: conclusion, HeadSHA: sha, CreatedAt: at(9 * time.Minute)}
	}
	merged := func(p *model.PR, sha string, age time.Duration) {
		p.State = "MERGED"
		m := at(age)
		p.MergedAt = &m
		p.MergeCommit = &model.MergeCommit{Oid: sha}
		p.Labels.Nodes = nil
	}

	comments := map[model.Ident][]model.Comment{}
	runs := map[string][]model.WorkflowRun{}
	waits := map[int64][]model.PendingDeployment{}
	dbStatus := ""
	if stage >= NightAppPosted {
		comments[app.Ident()] = []model.Comment{nowCmd("bumped from the window for time; the fix is waiting", 0)}
	}
	if stage >= NightAppReleased {
		merged(app, appSHA, 14*time.Minute)
		app.Labels.Nodes = []model.Label{{Name: "window:applied"}}
		// the flight took about twelve minutes
		comments[app.Ident()] = append(comments[app.Ident()], outcome("Released", appSHA, -11*time.Minute-40*time.Second))
		runs[appSHA] = []model.WorkflowRun{run(701, "deploy-web", "completed", "success", appSHA), run(702, "deploy-api", "completed", "success", appSHA)}
	}
	if stage >= NightDBWaitsStg {
		merged(db, dbSHA, 3*time.Minute)
		db.Labels.Nodes = []model.Label{{Name: "window:pending"}}
		comments[db.Ident()] = []model.Comment{nowCmd("next in the lane after the portal release", 0),
			outcome("Release merged; the deploy is WAITING on an environment approval", dbSHA, 0)}
		dbStatus = "cancelled by @" + Me
		env := "stg"
		if stage >= NightDBWaitsProd {
			env = "prod"
		}
		runs[dbSHA] = []model.WorkflowRun{run(801, "migrate", "waiting", "", dbSHA)}
		waits[801] = []model.PendingDeployment{pending(env, true, "team:release-approvers")}
		waits[801][0].Environment.ID = map[string]int64{"stg": 11, "prod": 12}[env]
	}
	if stage >= NightDBReleased {
		db.Labels.Nodes = []model.Label{{Name: "window:applied"}}
		runs[dbSHA] = []model.WorkflowRun{run(801, "migrate", "completed", "success", dbSHA)}
		waits = map[int64][]model.PendingDeployment{}
	}

	appStatus := "bumped"
	manifest := &model.Issue{Repo: nightInfra, Number: 20, Title: NightConfig().MaintenanceWindow.Title(n.night), State: "OPEN",
		CreatedAt: at(30 * time.Hour), UpdatedAt: at(time.Hour), Author: actor("github-actions")}
	manifest.Labels.Nodes = []model.Label{{Name: "window-manifest"}}
	manifest.Body = fmt.Sprintf("Tonight's window.\n\n<!-- window-manifest:begin\n{\"night\": %q, \"phase\": \"frozen\", \"attendees\": [%q], \"items\": ["+
		"{\"id\": \"rel:%s#%d\", \"kind\": \"app\", \"repo\": %q, \"pr\": %d, \"flag\": \"bumped\", \"status\": %q, \"reasons\": [\"does not fit: the estimated apply is over the window\"]}, "+
		"{\"id\": \"rel:%s#%d\", \"kind\": \"app\", \"repo\": %q, \"pr\": %d, \"flag\": \"scheduled\", \"start_utc\": %q, \"head_sha\": %q, \"status\": %q, \"reasons\": []}"+
		"]}\nwindow-manifest:end -->",
		n.night, Me, nightApp.Name, nightAppPR, nightApp.Name, nightAppPR, appStatus,
		nightDB.Name, nightDBPR, nightDB.Name, nightDBPR, slot.Format(time.RFC3339), db.HeadRefOid, dbStatus)
	manifest.Comments.Nodes = []model.Comment{bot("@"+Me+" is attending the night of "+n.night+".", 3*time.Hour)}
	manifest.Comments.TotalCount = 1

	gh.Releases = map[model.Repo]ghapi.ReleaseRepo{
		nightApp: {Name: nightApp.Name, PRs: []*model.PR{app}, ProdOid: "aaaa0212", StageOid: "aaaa0212"},
		nightDB:  {Name: nightDB.Name, PRs: []*model.PR{db}, ProdOid: "bbbb0064", StageOid: "bbbb0064"},
	}
	gh.Comments = comments
	gh.Runs = runs
	gh.Pending = waits
	gh.Labelled = map[model.Repo]map[string][]*model.Issue{nightInfra: {"window-manifest": {manifest}}}
	gh.Open = map[model.Repo]ghapi.ListResult{nightInfra: {Issues: []*model.Issue{manifest}}}
	gh.Issues = map[model.Ident]*model.Issue{manifest.Ident(): manifest}
	gh.PRs = map[model.Ident]*model.PR{}
	for _, p := range []*model.PR{app, db} {
		d := *p
		d.Body = "Today's cut for production."
		d.Comments.Nodes = comments[p.Ident()]
		d.Comments.TotalCount = len(d.Comments.Nodes)
		d.Reviews.Nodes = p.Opinions.Nodes
		d.Reviews.TotalCount = len(d.Reviews.Nodes)
		d.MergeStateStatus = "CLEAN"
		gh.PRs[p.Ident()] = &d
	}
	n.stage = stage
}
