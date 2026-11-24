package waitingruns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// gated is an environment that needs a reviewer; open is one that does not.
func gated(name string) model.Environment {
	var e model.Environment
	e.Name = name
	e.ProtectionRules = append(e.ProtectionRules, struct {
		Type      string                     `json:"type"`
		Reviewers []model.DeploymentReviewer `json:"reviewers"`
	}{Type: "required_reviewers", Reviewers: []model.DeploymentReviewer{{Type: "Team"}}})
	return e
}

func open(name string) model.Environment { return model.Environment{Name: name} }

// TestDiscoveryFindsGatedReposAndKeepsThem: repos = "auto" reads the
// org's repos, keeps the ones with an environment that needs a reviewer
// (not archived), reads only those, and keeps the answer: a second
// refresh asks GitHub nothing about environments, and a new dashboard
// finds it on disk.
func TestDiscoveryFindsGatedReposAndKeepsThem(t *testing.T) {
	w, app := newWF(t)
	w.cfg.Auto, w.cfg.Repos, w.cfg.Orgs = true, nil, []string{"acme"}
	w.cfg.Cache = filepath.Join(t.TempDir(), "waiting.json")
	ship := model.Repo{Owner: "acme", Name: "ship"}
	docs := model.Repo{Owner: "acme", Name: "docs"}
	old := model.Repo{Owner: "acme", Name: "old"}
	app.Fake.Orgs = map[string][]model.RepoListing{"acme": {{Name: "ship"}, {Name: "docs"}, {Name: "old", Archived: true}}}
	app.Fake.Envs = map[model.Repo][]model.Environment{ship: {open("dev"), gated("prod")}, docs: {open("preview")}, old: {gated("prod")}}
	run := model.WorkflowRun{ID: 9, Name: "deploy", Status: "waiting", CreatedAt: clock.Now().Add(-time.Minute)}
	app.Fake.RepoRuns = map[model.Repo][]model.WorkflowRun{ship: {run}, docs: {{ID: 10, Status: "waiting"}}}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	st := app.State(Name).(*State)
	if len(st.Items) != 1 || st.Items[0].Repo != ship {
		t.Fatalf("only the gated repo is read: %+v", st.Items)
	}
	reads := app.Fake.EnvReads
	if reads != 2 {
		t.Errorf("environments read for the two live repos, not the archived one: %d", reads)
	}
	if _, err := refresh(t, w, app); err != nil {
		t.Fatal(err)
	}
	if app.Fake.EnvReads != reads {
		t.Errorf("a kept discovery asks nothing: %d reads", app.Fake.EnvReads)
	}
	fresh := New(w.cfg)
	got, note := fresh.repos(context.Background(), app.Fake, clock.Now())
	if note != "" || len(got) != 1 || got[0] != ship || app.Fake.EnvReads != reads {
		t.Errorf("a new dashboard reads the cache: %v %q (%d reads)", got, note, app.Fake.EnvReads)
	}
}

// TestDiscoveryNeedsAnOrg: auto with nowhere to look says so.
func TestDiscoveryNeedsAnOrg(t *testing.T) {
	w, app := newWF(t)
	w.cfg.Auto, w.cfg.Repos, w.cfg.Orgs, w.cfg.Cache = true, nil, nil, ""
	warns, err := refresh(t, w, app)
	if err != nil || len(warns) == 0 {
		t.Fatalf("warns %v err %v", warns, err)
	}
}

// TestADiscoveryThatCouldNotReadFallsBack: a token that cannot read any
// repo's environments finds nothing; the review repos are read instead,
// and nothing is kept on disk.
func TestADiscoveryThatCouldNotReadFallsBack(t *testing.T) {
	w, app := newWF(t)
	w.cfg.Auto, w.cfg.Repos, w.cfg.Orgs = true, nil, []string{"acme"}
	w.cfg.Cache = filepath.Join(t.TempDir(), "waiting.json")
	w.fallback = []model.Repo{infra}
	app.Fake.Orgs = map[string][]model.RepoListing{"acme": {{Name: "ship"}}}
	app.Fake.Fail = map[string]error{"environments": errors.New("403 Resource not accessible")}
	got, note := w.repos(context.Background(), app.Fake, clock.Now())
	if len(got) != 1 || got[0] != infra || !strings.Contains(note, "review repos instead") {
		t.Errorf("fallback: %v %q", got, note)
	}
	if _, err := os.Stat(w.cfg.Cache); err == nil {
		t.Error("a discovery that could not read was kept on disk")
	}
}
