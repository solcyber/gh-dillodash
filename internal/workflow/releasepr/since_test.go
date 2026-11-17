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

// cutRelease is an open release pinned on a staging merge commit: the
// commit is stamped two seconds before the staging PR's merge time, as
// GitHub does, and the merge time is the cut.
func cutRelease(number int, cut time.Time) *model.PR {
	pr := makeRelease(relOpts{number: number})
	pr.Set(ExtRel, nil)
	c := model.Commit{Oid: pr.HeadRefOid, CommittedDate: cut.Add(-2 * time.Second)}
	c.Landed.Nodes = []model.LandedPR{
		{Number: 500, BaseRefName: "main", MergedAt: &cut, MergeCommit: &model.MergeCommit{Oid: "other"}},
		{Number: 610, BaseRefName: "stage", MergedAt: &cut, MergeCommit: &model.MergeCommit{Oid: pr.HeadRefOid}},
	}
	pr.Commits = model.NewCommits(c)
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
		t.Errorf("asked from the merge time of the staging PR behind the release head: %v", asked)
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
	want := "status: web-portal: dev and stage differ — since release #44 was cut (3h ago): merged into dev #602 #603; into stage #612"
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
	if app.Last() != "status: web-portal: dev and stage hold the same content — since release #44 was cut (60m ago): nothing merged" {
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
	s, warns, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	if len(st.Stages) != 1 || st.Stages[0].Units == nil || st.Stages[0].Since != nil {
		t.Errorf("the stage row stands without a tail: %+v", st.Stages)
	}
	said := false
	for _, wn := range warns {
		said = said || strings.Contains(wn, "web-portal since the cut:")
	}
	if !said {
		t.Errorf("the failed read is a warning: %v", warns)
	}
}

// TestSinceIsNotResurrected: a good refresh, then the stage read fails.
// The last good units come back; what merged since the cut does not, so
// a count that stopped moving is never shown as current.
func TestSinceIsNotResurrected(t *testing.T) {
	w, app, st := sinceWF(t, []*model.PR{cutRelease(44, utc(1, 0))}, [2]string{"a", "b"})
	if st.Stages[0].Since == nil {
		t.Fatal("the first refresh has a tail")
	}
	app.Fake.Fail = map[string]error{"tags": context.DeadlineExceeded}
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	again := s.(*State)
	if len(again.Stages) != 1 || again.Stages[0].Units == nil || again.Stages[0].Since != nil {
		t.Errorf("last good units, no tail: %+v", again.Stages[0])
	}
	if st.Stages[0].Since == nil {
		t.Error("the earlier snapshot was changed under its reader")
	}
}

// TestCutMoment: the cut is the merge time of the staging PR whose merge
// commit is the release head; a head that is no staging merge falls back
// to its commit time; a last commit that is not the head, to the PR's
// creation.
func TestCutMoment(t *testing.T) {
	w, _, _ := sinceWF(t, nil, [2]string{"a", "b"})
	pr := cutRelease(44, utc(1, 0))
	if _, at := w.cutOf([]*model.PR{pr}); !at.Equal(utc(1, 0)) {
		t.Errorf("the staging PR's merge time: %v", at)
	}
	moved := cutRelease(44, utc(1, 0))
	c := moved.Commits.Last()
	c.Landed.Nodes = nil
	moved.Commits = model.NewCommits(*c)
	if _, at := w.cutOf([]*model.PR{moved}); !at.Equal(utc(1, 0).Add(-2 * time.Second)) {
		t.Errorf("no staging merge behind the head: its commit time: %v", at)
	}
	long := cutRelease(44, utc(1, 0))
	c = long.Commits.Last()
	c.Oid = "not-the-head"
	long.Commits = model.NewCommits(*c)
	if _, at := w.cutOf([]*model.PR{long}); !at.Equal(long.CreatedAt) {
		t.Errorf("the last commit read is not the head: the PR's creation: %v", at)
	}
	// two open releases: the newer one is the cut
	older := cutRelease(43, utc(0, 10))
	older.CreatedAt = utc(0, 0).Add(-time.Hour)
	if rel, _ := w.cutOf([]*model.PR{older, pr}); rel == nil || rel.Number != 44 {
		t.Errorf("the newest release: %+v", rel)
	}
}
