package releasepr

import (
	"context"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/model"
)

func TestFollowDeployWhatMovesOnItsOwn(t *testing.T) {
	cases := []struct {
		name string
		dep  deploys.State
		want bool
	}{
		{"running", deploys.State{Phase: "running"}, true},
		{"waiting on me", deploys.State{Phase: "waiting", Waits: []deploys.Wait{{EnvName: "prod", Mine: true, Due: true}}}, false},
		{"waiting on somebody else", deploys.State{Phase: "waiting", Waits: []deploys.Wait{{EnvName: "prod"}}}, false},
		{"automation still has it", deploys.State{Phase: "waiting", Waits: []deploys.Wait{{EnvName: "stg", Left: true}}}, true},
		{"stuck", deploys.State{Phase: "stuck"}, false},
		{"done", deploys.State{Phase: "done"}, false},
		{"failed", deploys.State{Phase: "failed"}, false},
	}
	for _, c := range cases {
		if got := followDeploy(c.dep); got != c.want {
			t.Errorf("%s: follow %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAWaitOnYouIsNotPolled: the fixture's merged release waits on the
// viewer: no refresh is pulled in and nothing is followed (D reads it).
func TestAWaitOnYouIsNotPolled(t *testing.T) {
	w, app, _ := fixture(t, true)
	w.AfterApply(app)
	if len(app.Soon) != 0 || w.followsDeploys() {
		t.Errorf("soon %v follows %v", app.Soon, w.followsDeploys())
	}
}

// TestARunningDeployIsFollowedByThePollAlone: a running deploy makes the
// workflow live at deploy_refresh_secs; the poll reads only the merge
// commit's runs, and a phase that moves pulls one refresh in.
func TestARunningDeployIsFollowedByThePollAlone(t *testing.T) {
	w, app, _ := fixture(t, true)
	app.Fake.Runs = map[string][]model.WorkflowRun{mergedSHA: {{ID: 31, Name: "Migrate", Status: "in_progress", HeadSHA: mergedSHA}}}
	s, _, err := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(app, s)
	w.AfterApply(app)
	if len(app.Soon) != 0 {
		t.Errorf("a running deploy pulls no whole refresh in: %v", app.Soon)
	}
	if !w.Live(app) || w.PollEvery() != 30*time.Second {
		t.Fatalf("followed: live %v every %v", w.Live(app), w.PollEvery())
	}
	app.Fake.Runs = map[string][]model.WorkflowRun{mergedSHA: {{ID: 31, Name: "Migrate", Status: "completed", Conclusion: "success", HeadSHA: mergedSHA}}}
	patch, err := w.Poll(context.Background(), app.Fake)
	if err != nil || patch == nil {
		t.Fatalf("poll: %v %v", patch, err)
	}
	w.ApplyPoll(app, patch)
	id := model.Ident{Repo: portal, Number: 133}
	dep, _ := w.Current().Find(id).Get(ExtDeploy).(deploys.State)
	if dep.Phase != "done" {
		t.Errorf("the poll's read lands on the row: %+v", dep)
	}
	if len(app.Soon) != 1 || app.Soon[0] != deployMovedRefresh {
		t.Errorf("the phase moved: one refresh follows: %v", app.Soon)
	}
	if w.followsDeploys() {
		t.Error("a finished deploy is followed no longer")
	}
}
