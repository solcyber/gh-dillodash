package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/releasepr"
)

// fakeUpdater answers every check with latest.
type fakeUpdater struct {
	latest string
	calls  int
}

func (f *fakeUpdater) Check(_ context.Context, _ bool) selfupdate.Status {
	f.calls++
	return selfupdate.Status{Running: "v1.5.2", Latest: f.latest, Self: selfupdate.Install{Kind: selfupdate.KindExtension}}
}

// staleHarness runs the harness as a v1.5.2 build with v1.5.4 out.
func staleHarness(t *testing.T, latest string) (*harness, *fakeUpdater) {
	h := newHarness(t)
	u := &fakeUpdater{latest: latest}
	h.m.ver, h.m.updater, h.m.upd = "v1.5.2", u, selfupdate.Status{Running: "v1.5.2"}
	return h, u
}

// TestStaleBuildBanner (#22): a build behind the latest release says so
// in gold on every screen, with the upgrade command.
func TestStaleBuildBanner(t *testing.T) {
	h, u := staleHarness(t, "v1.5.4")
	h.load()
	if u.calls != 1 {
		t.Fatalf("checks at startup: %d", u.calls)
	}
	s := h.screen()
	for _, want := range []string{"v1.5.2 → v1.5.4", "dillodash v1.5.2 — v1.5.4 is out: gh extension upgrade dillodash"} {
		if !strings.Contains(s, want) {
			t.Errorf("list lacks %q:\n%s", want, s)
		}
	}
	h.named("enter")
	if s := h.screen(); !strings.Contains(s, "v1.5.2 → v1.5.4") || !strings.Contains(s, "v1.5.4 is out") {
		t.Errorf("detail lacks the banner:\n%s", s)
	}
}

// TestCurrentBuildNoBanner: current, or offline (no answer), is quiet.
func TestCurrentBuildNoBanner(t *testing.T) {
	for _, latest := range []string{"v1.5.2", ""} {
		h, _ := staleHarness(t, latest)
		h.load()
		if s := h.screen(); strings.Contains(s, "is out") || strings.Contains(s, "→") {
			t.Errorf("latest %q: banner on a current build:\n%s", latest, s)
		}
	}
}

// TestStaleBuildGatesApproveOnARelease (#22): a on an approved release
// from a build without the release-NOW fix names the version it needs,
// never "a second review changes nothing", and posts nothing when the
// operator stops there.
func TestStaleBuildGatesApproveOnARelease(t *testing.T) {
	h, _ := staleHarness(t, "v1.5.4")
	p := withReleasePR(t, h)
	p.ReviewDecision = "APPROVED"
	p.Opinions.Nodes = []model.Review{{Author: model.Actor{Login: "alice"}, State: "APPROVED"}}
	h.load()
	st, _ := h.m.wfState["release_pr"].(*releasepr.State)
	if st == nil || len(st.Releases) != 1 {
		t.Fatalf("release state: %+v", st)
	}
	rel := st.Releases[0]
	h.m.mu.Lock()
	h.m.showDetail(rel, "pr", 0, rel)
	h.m.mu.Unlock()
	var asked []string
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return false }
	h.m.tp.Pick = func(string, []string) int { t.Error("no menu before the gate is passed"); return -1 }
	h.key('a')
	want := "release NOW on an approved release needs ≥ v1.5.3 — you run v1.5.2: gh extension upgrade dillodash"
	if len(asked) != 1 || !strings.HasPrefix(asked[0], want) {
		t.Errorf("gate question: %q", asked)
	}
	if !strings.Contains(h.m.status, want) || strings.Contains(h.m.status, "second review") {
		t.Errorf("status %q", h.m.status)
	}
	if len(h.gh.CommentsPosted) != 0 || len(h.gh.Approvals) != 0 {
		t.Errorf("posted on a refused gate: %+v %v", h.gh.CommentsPosted, h.gh.Approvals)
	}
	// going on anyway reaches the workflow
	h.m.tp.Confirm = func(string) bool { return true }
	var menu []string
	h.m.tp.Pick = func(_ string, labels []string) int { menu = labels; return -1 }
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "", false }
	h.key('a')
	if len(menu) != 1 || !strings.HasPrefix(menu[0], "release NOW") {
		t.Errorf("after the gate: %q", menu)
	}
}
