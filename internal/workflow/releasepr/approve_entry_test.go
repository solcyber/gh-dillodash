package releasepr

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/terraformapprove"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

const secondReview = "a second review changes nothing"

// entry is one way a release reaches a: which copies carry its info, and
// whether the release state is loaded.
type entry struct {
	name string
	// setup returns the detail PR and the listed row a sees
	setup func(w *Workflow, app *wftest.App, rel *model.PR) (pr, listed *model.PR)
}

// bare is a copy of rel as a detail fetch returns it: no release info.
func bare(rel *model.PR) *model.PR {
	cp := *rel
	cp.Ext = nil
	return &cp
}

// loaded puts rel in an applied release state.
func loaded(w *Workflow, app *wftest.App, rel *model.PR) {
	st := &State{w: w, Releases: []*model.PR{rel}, Repos: []model.Repo{rel.Repo}}
	st.index()
	w.Apply(app, st)
}

var entries = []entry{
	{"releases row (row and detail carry the info)", func(w *Workflow, app *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		loaded(w, app, rel)
		return rel, rel
	}},
	{"night row, state loaded (the listed row carries it)", func(w *Workflow, app *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		loaded(w, app, rel)
		return bare(rel), rel
	}},
	{"night row, state not loaded", func(_ *Workflow, _ *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		return bare(rel), rel
	}},
	{"detail, state loaded (only the state has it)", func(w *Workflow, app *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		loaded(w, app, rel)
		return bare(rel), nil
	}},
	{"goto or a manifest link, state not loaded", func(_ *Workflow, _ *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		return bare(rel), nil
	}},
	{"state dropped by a rate-limited refresh", func(w *Workflow, app *wftest.App, rel *model.PR) (*model.PR, *model.PR) {
		loaded(w, app, rel)
		w.Apply(app, (*State)(nil)) // the refresh stopped before this workflow
		return bare(rel), nil
	}},
}

// TestApproveOffersNowFromEveryEntry (#23): a on a release offers
// release NOW from every place a release can be opened, state loaded or
// not; an approved one offers exactly NOW, an unapproved one queue and
// NOW, and the core's "second review" notice never appears.
func TestApproveOffersNowFromEveryEntry(t *testing.T) {
	for _, e := range entries {
		for _, mine := range []bool{true, false} {
			name := fmt.Sprintf("%s, approved by me %v", e.name, mine)
			patchNow(t, utc(14, 0))
			w, app := newWF(t, true)
			rel := makeRelease(relOpts{number: 743})
			if mine {
				approved(rel, "alice")
			}
			pr, listed := e.setup(w, app, rel)
			if !w.Approve(app, pr, listed) {
				t.Errorf("%s: a release is always handled", name)
				continue
			}
			if len(app.Menus) != 1 {
				t.Errorf("%s: one menu, got %v (log %q)", name, app.Menus, app.Log)
				continue
			}
			labels := app.Menus[0][1:]
			switch {
			case mine && (len(labels) != 3 || !strings.HasPrefix(labels[0], "release NOW (/release-now)") ||
				!strings.HasPrefix(labels[1], "dismiss my approval") || !strings.HasPrefix(labels[2], "request changes")):
				t.Errorf("%s: release NOW, then dismiss or request changes (never a second review), got %q", name, labels)
			case !mine && (len(labels) != 3 || !strings.HasPrefix(labels[0], "queue it") || !strings.HasPrefix(labels[1], "release NOW") ||
				!strings.HasPrefix(labels[2], "request changes")):
				t.Errorf("%s: queue and NOW, got %q", name, labels)
			}
			for _, l := range app.Log {
				if strings.Contains(l, secondReview) {
					t.Errorf("%s: %q", name, l)
				}
			}
		}
	}
}

// TestApproveWithReleaseReposUnknown: with discovery on and nothing
// discovered yet, a release-shaped PR is neither approved nor sent to
// the core: a says the state is not read, and posts nothing.
func TestApproveWithReleaseReposUnknown(t *testing.T) {
	patchNow(t, utc(14, 0))
	w, app := newWF(t, true)
	w.cfg.Auto, w.cfg.Cache = true, ""
	rel := approved(makeRelease(relOpts{number: 743}), "alice")
	if !w.Approve(app, bare(rel), nil) {
		t.Fatal("a release-shaped PR is handled")
	}
	if len(app.Menus) != 0 || len(comments(app)) != 0 || len(app.Fake.Approvals) != 0 {
		t.Errorf("nothing offered or posted: %v %q", app.Menus, comments(app))
	}
	if !logHas(app, "notice", "couldn't read release state for") || !logHas(app, "notice", "press r") {
		t.Errorf("says why: %q", app.Log)
	}
	if !w.ReleaseShaped(bare(rel)) {
		t.Error("the core is told it is a release")
	}

	// Once the refresh names the repo, the same PR gets NOW.
	w.mu.Lock()
	w.enrolled = []model.Repo{portal}
	w.mu.Unlock()
	app.Log, app.Menus = nil, nil
	w.Approve(app, bare(rel), nil)
	if len(app.Menus) != 1 || !strings.HasPrefix(app.Menus[0][1], "release NOW") {
		t.Errorf("enrolled: NOW, got %v %q", app.Menus, app.Log)
	}

	// A repo discovery did not list is not a release.
	other := bare(rel)
	other.Repo = model.Repo{Owner: "acme", Name: "website"}
	if _, sure := w.classify(other, nil, nil); sure != NotRelease || w.ReleaseShaped(other) {
		t.Errorf("an unenrolled repo is not a release: %v", sure)
	}
}

// TestClassifyByBranches: no info anywhere, the config's repos: a release
// or hotfix head into production is a release; any other base or head
// is not.
func TestClassifyByBranches(t *testing.T) {
	w, _ := newWF(t, true)
	cases := []struct {
		head, base string
		want       Release
		kind       string
	}{
		{"release/2026-11-01", "main", IsRelease, KindRelease},
		{"hotfix/login", "main", IsRelease, KindHotfix},
		{"release/2026-11-01", "", IsRelease, KindRelease},
		{"release/2026-11-01", "stage", NotRelease, ""},
		{"feat/x", "main", NotRelease, ""},
	}
	for _, c := range cases {
		pr := &model.PR{Repo: portal, Number: 1, State: "OPEN", HeadRefName: c.head, BaseRefName: c.base}
		info, got := w.classify(pr, nil, nil)
		if got != c.want || (info != nil && info.Kind != c.kind) {
			t.Errorf("%s -> %q: %v %+v, want %v %s", c.head, c.base, got, info, c.want, c.kind)
		}
	}
}

// TestReleaseBeatsAPlanDigest: a release PR whose thread quotes a plan
// digest (a release that changed infrastructure, say) goes to the
// release workflow, not to the plan workflow ahead of it in order, which
// would answer "a second review changes nothing".
func TestReleaseBeatsAPlanDigest(t *testing.T) {
	patchNow(t, utc(14, 0))
	cfg := newCfg(true)
	cfg.TerraformApprove = config.DefaultTerraformApprove()
	ctx := &workflow.Context{Config: cfg, Repos: cfg.Reviews.Repos}
	tf := terraformapprove.New(cfg.TerraformApprove, cfg.MaintenanceWindow)
	rel := New(ctx)
	set, err := workflow.NewSet(ctx, tf, winStub{}, rel)
	if err != nil {
		t.Fatal(err)
	}
	app := wftest.New(cfg, "alice")
	pr := approved(makeRelease(relOpts{number: 743}), "alice")
	at, _ := time.Parse(time.RFC3339, "2026-10-28T13:00:00Z")
	pr.Comments.Nodes = []model.Comment{{Author: model.Actor{Login: "infra-bot"}, CreatedAt: at,
		Body: "### plan `prod/07-portal`\n\nPlan-id: `" + strings.Repeat("a", 64) + "`\nPlan: 0 to add, 1 to change, 0 to destroy.\nhttps://github.com/acme/infra/actions/runs/100\n"}}
	if !set.Approve(app, bare(pr), nil) {
		t.Fatal("handled")
	}
	if len(app.Menus) != 1 || !strings.HasPrefix(app.Menus[0][1], "release NOW") {
		t.Errorf("the release workflow answers: %v %q", app.Menus, app.Log)
	}
	for _, l := range app.Log {
		if strings.Contains(l, secondReview) {
			t.Errorf("%q", l)
		}
	}
}

// TestApplyKeepsStateOnNil: a refresh that stopped at a rate limit
// before this workflow hands it nil; the releases stay.
func TestApplyKeepsStateOnNil(t *testing.T) {
	w, app := newWF(t, true)
	rel := makeRelease(relOpts{})
	loaded(w, app, rel)
	w.Apply(app, (*State)(nil))
	if st := w.stateOf(app); st == nil || st.Find(rel.Ident()) == nil {
		t.Errorf("the last state stands: %+v", st)
	}
}

// TestBumpedChipOffersNow: a release the freeze bumped (the manifest's
// flag) that I approved says so on its chip: bumped — a: release NOW.
func TestBumpedChipOffersNow(t *testing.T) {
	patchNow(t, utc(1, 30))
	w, app := newWF(t, true)
	pr := approved(makeRelease(relOpts{number: 743}), "alice")
	p := Progress{}
	if ph, ok := w.fromItem(&p, &workflow.ManifestItem{ID: "rel:portal#743", Flag: "bumped", Status: "bumped"}, utc(1, 30)); !ok || ph != PhaseBumped || !p.Terminal {
		t.Fatalf("the bumped flag is a terminal phase: %q %v", ph, p.Terminal)
	}
	got := plain(w.progressChipSegs(app, pr, p, 0, false))
	if !strings.Contains(got, "bumped — a: release NOW") {
		t.Errorf("chip: %q", got)
	}
	if got := plain(w.progressChipSegs(app, pr, p, 0, true)); !strings.Contains(got, "bumped · a: NOW") {
		t.Errorf("compact chip: %q", got)
	}
	notMine := approved(makeRelease(relOpts{number: 744}), "carol")
	if got := plain(w.progressChipSegs(app, notMine, p, 0, false)); strings.Contains(got, "a: release NOW") {
		t.Errorf("approved by someone else: no hint: %q", got)
	}
}

func plain(segs []text.Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}
