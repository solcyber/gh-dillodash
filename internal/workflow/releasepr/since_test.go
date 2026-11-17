package releasepr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

// sinceWF is a release workflow that knows the first tier, with one open
// release cut at 01:00 and merges on both sides of the cut.
func sinceWF(t *testing.T, prs []*model.PR, trees [2]string) (*Workflow, *wftest.App, *State) {
	t.Helper()
	patchNow(t, utc(4, 0))
	cfg := newCfg(false)
	pc := config.DefaultPromote()
	pc.Repos = []model.Repo{portal}
	cfg.Promote = pc
	w := New(&workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos})
	app := wftest.New(cfg, "alice")
	merged := func(n int, at time.Time) ghapi.MergedPR { return ghapi.MergedPR{Number: n, MergedAt: at} }
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: prs, ProdOid: "m", StageOid: "s"}}
	app.Fake.Compares = map[string]ghapi.CompareResult{"main...stage": {AheadBy: 4, Commits: stageCommits()}}
	app.Fake.Sinces = map[model.Repo]ghapi.Since{portal: {
		First:     []ghapi.MergedPR{merged(601, utc(0, 30)), merged(602, utc(1, 38)), merged(603, utc(1, 43))},
		Stage:     []ghapi.MergedPR{merged(610, utc(0, 50)), merged(612, utc(2, 5))},
		FirstTree: trees[0], StageTree: trees[1],
	}}
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	w.Apply(app, st)
	return w, app, st
}

func cutRelease(number int, cut time.Time) *model.PR {
	pr := makeRelease(relOpts{number: number})
	pr.Set(ExtRel, nil)
	pr.Commits = model.NewCommits(model.Commit{CommittedDate: cut})
	return pr
}

// TestStageRowSinceTheCut: with a release open, the stage row says what
// merged into the first tier and into staging after the release's head
// commit, and whether the two branches hold the same content; Enter
// names the PRs.
func TestStageRowSinceTheCut(t *testing.T) {
	w, app, st := sinceWF(t, []*model.PR{cutRelease(44, utc(1, 0))}, [2]string{"treeA", "treeB"})
	if len(st.Stages) != 1 || st.Stages[0].Since == nil {
		t.Fatalf("the stage row carries what merged since the cut: %+v", st.Stages)
	}
	c := st.Stages[0].Since
	if c.Release != 44 || !c.At.Equal(utc(1, 0)) || c.FirstCount != 2 || c.StageCount != 1 || c.Same() {
		t.Errorf("since the cut at 01:00: %+v", c)
	}
	if asked := app.Fake.SinceAsked; len(asked) != 1 || !asked[0].Equal(utc(1, 0)) {
		t.Errorf("asked from the release head's commit time: %v", asked)
	}
	row := workflow.Row{Kind: RowStage, Item: st.Stages[0]}
	kinds := w.RowKinds()
	L := kinds[RowStage].Layout(app, []workflow.Row{row}, 140, 4, 0)
	got := text.SegsText(kinds[RowStage].Segs(app, row, L, 140, 0))
	if !strings.Contains(got, "since the cut: +2 dev, +1 stage") || !strings.Contains(got, "dev ≠ stage") {
		t.Errorf("the row's tail: %q", got)
	}
	// too narrow for all of it: the tail goes, the units stay
	narrow := text.SegsText(kinds[RowStage].Segs(app, row, L, 60, 0))
	if strings.Contains(narrow, "since the cut") || !strings.Contains(narrow, "ahead") {
		t.Errorf("a narrow row drops the tail whole: %q", narrow)
	}
	if !kinds[RowStage].Open(app, row) {
		t.Fatal("Enter on the stage row is handled")
	}
	want := "notice: web-portal since release #44 was cut (3h ago): merged into dev #602 #603; into stage #612 — dev and stage differ"
	if app.Last() != want {
		t.Errorf("Enter names the PRs:\n got %q\nwant %q", app.Last(), want)
	}
	if !strings.Contains(w.SearchText(row), "since the cut") {
		t.Errorf("the plain text carries it: %q", w.SearchText(row))
	}
}

// TestStageRowSameContent: nothing merged since the cut and equal trees
// is said in so many words, which is the sweep a release manager wants.
func TestStageRowSameContent(t *testing.T) {
	w, app, st := sinceWF(t, []*model.PR{cutRelease(44, utc(3, 0))}, [2]string{"treeA", "treeA"})
	c := st.Stages[0].Since
	if c == nil || c.FirstCount != 0 || c.StageCount != 0 || !c.Same() {
		t.Fatalf("nothing since 03:00, same trees: %+v", c)
	}
	if got := w.sinceText(st.Stages[0]); got != "dev == stage" {
		t.Errorf("tail %q", got)
	}
	row := workflow.Row{Kind: RowStage, Item: st.Stages[0]}
	w.RowKinds()[RowStage].Open(app, row)
	if !strings.HasSuffix(app.Last(), "nothing merged — dev and stage hold the same content") {
		t.Errorf("notice %q", app.Last())
	}
}

// TestStageRowWithoutACut: no open release, a hotfix alone, or no first
// tier: the row says nothing and nothing is read.
func TestStageRowWithoutACut(t *testing.T) {
	hot := makeRelease(relOpts{kind: KindHotfix, number: 45})
	hot.Set(ExtRel, nil)
	for name, prs := range map[string][]*model.PR{"none": nil, "hotfix": {hot}} {
		_, app, st := sinceWF(t, prs, [2]string{"a", "b"})
		if len(st.Stages) != 1 || st.Stages[0].Since != nil || len(app.Fake.SinceAsked) != 0 {
			t.Errorf("%s: no cut, no read: %+v asked %v", name, st.Stages, app.Fake.SinceAsked)
		}
	}
	// the fixture without a promote workflow has no first tier
	w, app, st := fixture(t, false)
	if st.Stages[0].Since != nil || len(app.Fake.SinceAsked) != 0 || w.sinceText(st.Stages[0]) != "" {
		t.Errorf("no first tier: %+v", st.Stages[0].Since)
	}
	row := workflow.Row{Kind: RowStage, Item: st.Stages[0]}
	if w.RowKinds()[RowStage].Open(app, row); !strings.Contains(app.Last(), "branch, not a PR") {
		t.Errorf("Enter keeps its old answer: %q", app.Last())
	}
}

// TestSinceReadFails: a failed read leaves the row without a tail
// rather than with a stale one, and the stage row itself stands.
func TestSinceReadFails(t *testing.T) {
	patchNow(t, utc(4, 0))
	cfg := newCfg(false)
	pc := config.DefaultPromote()
	pc.Repos = []model.Repo{portal}
	cfg.Promote = pc
	w := New(&workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos})
	app := wftest.New(cfg, "alice")
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{cutRelease(44, utc(1, 0))}, ProdOid: "m", StageOid: "s"}}
	app.Fake.Compares = map[string]ghapi.CompareResult{"main...stage": {AheadBy: 4, Commits: stageCommits()}}
	app.Fake.Fail = map[string]error{"since": context.DeadlineExceeded}
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	if len(st.Stages) != 1 || st.Stages[0].Units == nil || st.Stages[0].Since != nil {
		t.Errorf("the stage row stands without a tail: %+v", st.Stages)
	}
}
