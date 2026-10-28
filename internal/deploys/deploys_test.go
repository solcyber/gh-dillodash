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
		st := Compute(c.runs, c.pending, now)
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
