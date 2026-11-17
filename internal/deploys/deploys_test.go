package deploys

import (
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

func run(id int64, name, status, concl string, age time.Duration, now time.Time) model.WorkflowRun {
	return model.WorkflowRun{ID: id, Name: name, Status: status, Conclusion: concl, CreatedAt: now.Add(-age)}
}

func TestCompute(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 10, 0, 0, time.UTC)
	pend := func(env string, id int64, mine bool) model.PendingDeployment {
		var p model.PendingDeployment
		p.Environment.ID, p.Environment.Name, p.CurrentUserCanApprove = id, env, mine
		return p
	}
	cases := []struct {
		name    string
		runs    []model.WorkflowRun
		pending map[int64][]model.PendingDeployment
		phase   string
		text    string
	}{
		{"none", nil, nil, "none", "no Actions runs"},
		{"waiting mine", []model.WorkflowRun{run(1, "deploy", "waiting", "", time.Minute, now)},
			map[int64][]model.PendingDeployment{1: {pend("prod", 7, true)}}, "waiting", "approve: prod"},
		{"waiting theirs", []model.WorkflowRun{run(1, "deploy", "waiting", "", time.Minute, now)},
			map[int64][]model.PendingDeployment{1: {pend("prod", 7, false)}}, "waiting", "waits: prod"},
		{"running", []model.WorkflowRun{run(1, "deploy", "in_progress", "", 2*time.Minute, now)}, nil, "running", "deploying"},
		{"stuck", []model.WorkflowRun{run(1, "deploy", "queued", "", time.Hour, now)}, nil, "stuck", "stuck? 1h00m"},
		{"done beats cancelled", []model.WorkflowRun{run(1, "a", "completed", "success", time.Hour, now), run(2, "b", "completed", "cancelled", time.Hour, now)}, nil, "done", "deployed"},
		{"failed beats done", []model.WorkflowRun{run(1, "a", "completed", "success", time.Hour, now), run(2, "b", "completed", "failure", time.Hour, now)}, nil, "failed", "deploy failed"},
		{"waiting beats failed", []model.WorkflowRun{run(1, "a", "waiting", "", time.Minute, now), run(2, "b", "completed", "failure", time.Hour, now)},
			map[int64][]model.PendingDeployment{1: {pend("stg", 1, true)}}, "waiting", "approve: stg"},
	}
	for _, c := range cases {
		st := compute(c.runs, c.pending, now)
		if st.Phase != c.phase || st.Text != c.text {
			t.Errorf("%s: phase %q text %q, want %q %q", c.name, st.Phase, st.Text, c.phase, c.text)
		}
	}
}

func TestFmtSecs(t *testing.T) {
	for d, want := range map[time.Duration]string{12 * time.Second: "12s", 7 * time.Minute: "7m", 3*time.Hour + 5*time.Minute: "3h05m", -5 * time.Second: "0s"} {
		if got := FmtSecs(d); got != want {
			t.Errorf("FmtSecs(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestComputeWithStuckThreshold(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 10, 0, 0, time.UTC)
	runs := []model.WorkflowRun{run(1, "deploy", "in_progress", "", 10*time.Minute, now)}
	if st := compute(runs, nil, now); st.Phase != "running" {
		t.Errorf("default threshold: %q", st.Phase)
	}
	if st := ComputeWith(runs, nil, now, Options{StuckAfter: 5 * time.Minute}); st.Phase != "stuck" || st.Text != "stuck? 10m" {
		t.Errorf("5m threshold: %q %q", st.Phase, st.Text)
	}
	if st := ComputeWith(runs, nil, now, Options{}); st.Phase != "running" {
		t.Errorf("zero means the default: %q", st.Phase)
	}
	var c *Cache
	if c.Stuck() != StuckAfter || (&Cache{StuckAfter: time.Minute}).Stuck() != time.Minute {
		t.Error("Cache.Stuck")
	}
}

func TestRefreshInterval(t *testing.T) {
	if RefreshInterval(0) != RefreshEvery || RefreshInterval(-1) != RefreshEvery || RefreshInterval(45) != 45*time.Second {
		t.Error("RefreshInterval")
	}
}

// TestStuckIsIdleNotAge: a run that waited a night for an approval and
// started a minute ago is running, not stuck; a re-run's age is its own.
func TestStuckIsIdleNotAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	run := model.WorkflowRun{ID: 1, Name: "deploy", Status: "in_progress",
		CreatedAt: now.Add(-9 * time.Hour), UpdatedAt: now.Add(-time.Minute)}
	if st := ComputeWith([]model.WorkflowRun{run}, nil, now, Options{}); st.Phase != "running" {
		t.Errorf("approved a minute ago: phase %q (%s), want running", st.Phase, st.Text)
	}
	run.UpdatedAt = now.Add(-2 * time.Hour)
	if st := ComputeWith([]model.WorkflowRun{run}, nil, now, Options{}); st.Phase != "stuck" {
		t.Errorf("no change for 2h: phase %q, want stuck", st.Phase)
	}
	run.RunStartedAt, run.UpdatedAt = now.Add(-5*time.Minute), time.Time{}
	if st := ComputeWith([]model.WorkflowRun{run}, nil, now, Options{}); st.Phase != "running" {
		t.Errorf("a re-run started 5m ago: phase %q, want running", st.Phase)
	}
}

// TestFailedRunDoesNotEndARunningDeploy: a failed lint on the merge
// commit next to a running deploy reads deploying; code scanning's own
// runs are not deploys at all.
func TestFailedRunDoesNotEndARunningDeploy(t *testing.T) {
	now := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	runs := []model.WorkflowRun{
		{ID: 1, Name: "deploy", Status: "in_progress", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 2, Name: "lint", Status: "completed", Conclusion: "failure", CreatedAt: now.Add(-3 * time.Minute)},
	}
	if st := ComputeWith(runs, nil, now, Options{}); st.Phase != "running" || st.Text != "deploying · a run failed" {
		t.Errorf("phase %q text %q", st.Phase, st.Text)
	}
	runs = []model.WorkflowRun{
		{ID: 1, Name: "deploy", Status: "completed", Conclusion: "success", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: 3, Name: "CodeQL", Event: "dynamic", Status: "completed", Conclusion: "failure", CreatedAt: now.Add(-3 * time.Minute)},
	}
	if st := ComputeWith(runs, nil, now, Options{}); st.Phase != "done" {
		t.Errorf("a dynamic run failed: phase %q, want done", st.Phase)
	}
}

// TestAutomaticWaits: an environment with a bot among its reviewers is
// automation's to approve. It is not put to the viewer until it has
// waited past the grace, and one with only people is due at once.
func TestAutomaticWaits(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	isBot := func(l string) bool { return l == "release-bot" }
	pend := func(env string, logins ...string) model.PendingDeployment {
		var p model.PendingDeployment
		p.Environment.Name, p.CurrentUserCanApprove = env, true
		for _, l := range logins {
			var r model.DeploymentReviewer
			r.Type, r.Reviewer.Login = "User", l
			p.Reviewers = append(p.Reviewers, r)
		}
		return p
	}
	run := func(waited time.Duration) []model.WorkflowRun {
		return []model.WorkflowRun{{ID: 1, Name: "deploy", Status: "waiting", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-waited)}}
	}
	o := Options{IsBot: isBot}

	st := ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {pend("prod", "alice", "release-bot")}}, now, o)
	if st.Text != "waits: prod · automatic" || len(st.Due()) != 0 || st.LeftBy() != "release-bot" || len(st.Mine()) != 1 || !st.AllLeft() {
		t.Errorf("young automatic wait: %q due %v by %q mine %v", st.Text, st.Due(), st.LeftBy(), st.Mine())
	}
	st = ComputeWith(run(6*time.Minute), map[int64][]model.PendingDeployment{1: {pend("prod", "alice", "release-bot")}}, now, o)
	if st.Text != "approve: prod" || len(st.Due()) != 1 {
		t.Errorf("automation left it past the grace: %q due %v", st.Text, st.Due())
	}
	st = ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {pend("data", "alice")}}, now, o)
	if st.Text != "approve: data" || len(st.Due()) != 1 || len(st.Left()) != 0 {
		t.Errorf("people only: due at once: %q due %v", st.Text, st.Due())
	}
	// one of each: the viewer's own is named, the automatic one is not
	st = ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {pend("data", "alice"), pend("prod", "release-bot")}}, now, o)
	if st.Text != "approve: data" || st.AllLeft() || len(st.Left()) != 1 {
		t.Errorf("mixed: %q left %v", st.Text, st.Left())
	}
	// two runs on one environment name, one past the grace: it is due,
	// and the deploy is not "all left to automation"
	two := []model.WorkflowRun{
		{ID: 1, Name: "api", Status: "waiting", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: 2, Name: "web", Status: "waiting", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
	}
	st = ComputeWith(two, map[int64][]model.PendingDeployment{1: {pend("prod", "release-bot")}, 2: {pend("prod", "release-bot")}}, now, o)
	if st.Text != "approve: prod" || st.AllLeft() {
		t.Errorf("one of two runs past the grace: %q allLeft %v", st.Text, st.AllLeft())
	}
	// a wait nobody here can approve reads automatic only inside the grace
	notMine := pend("prod", "release-bot")
	notMine.CurrentUserCanApprove = false
	if st = ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {notMine}}, now, o); st.Text != "waits: prod · automatic" {
		t.Errorf("not mine, inside the grace: %q", st.Text)
	}
	if st = ComputeWith(run(time.Hour), map[int64][]model.PendingDeployment{1: {notMine}}, now, o); st.Text != "waits: prod" {
		t.Errorf("not mine, automation stalled: %q", st.Text)
	}
	// no grace at all
	if st = ComputeWith(run(time.Second), map[int64][]model.PendingDeployment{1: {pend("prod", "release-bot")}}, now, Options{IsBot: isBot, AutoGrace: -1}); st.Text != "approve: prod" {
		t.Errorf("a negative grace leaves nothing to automation: %q", st.Text)
	}
	// no bot list: as before
	st = ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {pend("prod", "alice", "release-bot")}}, now, Options{})
	if st.Text != "approve: prod" {
		t.Errorf("without IsBot every wait of mine is due: %q", st.Text)
	}
	// a team reviewer whose slug matches a bot login is not a bot
	var team model.PendingDeployment
	team.Environment.Name, team.CurrentUserCanApprove = "prod", true
	var tr model.DeploymentReviewer
	tr.Type, tr.Reviewer.Slug, tr.Reviewer.Login = "Team", "release-bot", "release-bot"
	team.Reviewers = []model.DeploymentReviewer{tr}
	if st = ComputeWith(run(time.Minute), map[int64][]model.PendingDeployment{1: {team}}, now, o); st.Text != "approve: prod" {
		t.Errorf("a team is not a bot: %q", st.Text)
	}
}
