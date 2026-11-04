package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/releasepr"
)

// fakeUpdater answers every check with latest.
type fakeUpdater struct {
	latest     string
	calls      int
	installs   int
	installErr error
}

func (f *fakeUpdater) Check(_ context.Context, _ bool) selfupdate.Status {
	f.calls++
	return selfupdate.Status{Running: "v1.5.2", Latest: f.latest, Self: selfupdate.Install{Kind: selfupdate.KindExtension}}
}

func (f *fakeUpdater) Install(_ context.Context, st selfupdate.Status) selfupdate.Result {
	f.installs++
	if f.installErr != nil {
		return selfupdate.Result{Version: st.Latest, Err: f.installErr, Notes: []string{"/home/alice/bin/dillodash: a launcher — never replaced"}}
	}
	return selfupdate.Result{Version: st.Latest, Updated: []string{"/home/alice/.local/share/gh/extensions/gh-dillodash/gh-dillodash"}}
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
	h.m.cfg.Update.Auto = false // banner only
	h.load()
	if u.installs != 0 {
		t.Errorf("auto = false installed")
	}
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
	h.m.cfg.Update.Auto = false
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
	if len(menu) != 3 || !strings.HasPrefix(menu[0], "release NOW") {
		t.Errorf("after the gate: %q", menu)
	}
}

// TestAutoUpdateInstallsAndRestartKeepsThePlace (#25): a session on
// v1.5.2 with v1.5.4 out installs it in the background, says so with R,
// and R quits with a restart request naming the open PR and the row.
func TestAutoUpdateInstallsAndRestartKeepsThePlace(t *testing.T) {
	h, u := staleHarness(t, "v1.5.4")
	h.load()
	if u.installs != 1 {
		t.Fatalf("installs: %d", u.installs)
	}
	s := h.screen()
	if !strings.Contains(s, "installed dillodash v1.5.4") || !strings.Contains(s, "R  restart") {
		t.Errorf("after the install:\n%s", s)
	}
	if line, _ := h.m.persistentLine(); line != "dillodash v1.5.4 installed — R: restart now (keeps your place)" {
		t.Errorf("standing line %q", line)
	}
	// a later check does not install the same release twice
	h.m.mu.Lock()
	h.m.checkBuild(true)
	h.m.mu.Unlock()
	h.pump()
	if u.installs != 1 {
		t.Errorf("installed again: %d", u.installs)
	}
	h.named("enter")
	// a release action first offers the restart
	var asked []string
	h.m.tp.Confirm = func(q string) bool { asked = append(asked, q); return false }
	h.key('a')
	if len(asked) == 0 || !strings.Contains(asked[0], "dillodash v1.5.4 is installed — restart into v1.5.4 first?") {
		t.Errorf("gate: %q", asked)
	}
	if len(h.gh.Approvals) != 0 {
		t.Error("approved with every confirm declined")
	}
	h.m.mu.Lock()
	cmd := h.m.handleKey(widgets.R('R'))
	r := h.m.restartTo
	h.m.mu.Unlock()
	if cmd == nil || r == nil {
		t.Fatal("R did not quit with a restart")
	}
	if r.Detail == nil || r.Detail.Repo != "acme/infra" || r.Detail.Number != 101 || r.Detail.Kind != "pr" {
		t.Errorf("restore detail %+v", r.Detail)
	}
	if r.Selected == nil || r.Selected.Number != 101 {
		t.Errorf("restore selected %+v", r.Selected)
	}
	if got := DecodeRestore(r.Encode()); got == nil || got.Detail.Number != 101 {
		t.Errorf("round trip %+v", got)
	}
}

// TestReleaseActionRestartsFirst: yes to the restart-first question
// restarts into the installed release once the action settles, keeping
// the open PR, and posts nothing.
func TestReleaseActionRestartsFirst(t *testing.T) {
	h, _ := staleHarness(t, "v1.5.4")
	h.load()
	h.named("enter")
	h.m.tp.Confirm = func(string) bool { return true }
	h.m.tp.Pick = func(string, []string) int { t.Error("no menu after yes to the restart"); return -1 }
	h.key('a')
	h.pump()
	h.m.Update(tickMsg{h.m.tickGen})
	if h.m.restartTo == nil || h.m.restartTo.Detail == nil || h.m.restartTo.Detail.Number != 101 {
		t.Fatalf("restart: %+v status %q", h.m.restartTo, h.m.status)
	}
	if len(h.gh.Approvals) != 0 || len(h.gh.CommentsPosted) != 0 {
		t.Error("posted on the way to a restart")
	}
}

// TestRestartWaitsForAnActionInFlight: R while a post is in flight is
// queued and runs once it settles.
func TestRestartWaitsForAnActionInFlight(t *testing.T) {
	h, _ := staleHarness(t, "v1.5.4")
	h.load()
	h.m.mu.Lock()
	h.m.action = true
	cmd := h.m.handleKey(widgets.R('R'))
	queued := h.m.restartQueued && h.m.restartTo == nil
	h.m.mu.Unlock()
	if !queued || !strings.Contains(h.m.status, "waits for the action in flight") {
		t.Fatalf("R during an action: queued=%v status=%q cmd=%v", queued, h.m.status, cmd)
	}
	h.m.Update(actionDoneMsg{})
	h.m.Update(tickMsg{h.m.tickGen})
	if h.m.restartTo == nil {
		t.Error("the queued restart never ran")
	}
}

// TestInstallRefusalStaysUp: a refused install is an error on the
// status line, with the notes behind e, and nothing pending.
func TestInstallRefusalStaysUp(t *testing.T) {
	h, u := staleHarness(t, "v1.5.4")
	u.installErr = errors.New("checksum mismatch: linux-amd64's sha256 is 5e684770a24e, the release says e5fd0a6527f1 — nothing installed")
	h.load()
	if h.m.upd.Pending() {
		t.Error("pending after a refusal")
	}
	line, refused := h.m.persistentLine()
	if !refused || !strings.HasPrefix(line, "dillodash v1.5.4 NOT installed: checksum mismatch") || !strings.HasSuffix(line, "nothing installed; gh extension upgrade dillodash") {
		t.Errorf("standing line %q", line)
	}
	if !strings.Contains(h.m.lastMsg, "a launcher — never replaced") {
		t.Errorf("last %q", h.m.lastMsg)
	}
}

// TestRestoreAfterRestart: a session started with a Restore reopens the
// PR and selects the row once the list is in.
func TestRestoreAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.m.restore = &Restore{Selected: &RestoreRef{Repo: "acme/infra", Number: 102}, Detail: &RestoreRef{Repo: "acme/infra", Number: 101, Kind: "pr"}}
	h.load()
	if h.m.view != ViewDetail {
		t.Fatalf("view %s", h.m.view)
	}
	if p, ok := h.m.detail.(*model.PR); !ok || p.Number != 101 {
		t.Errorf("detail %+v", h.m.detail)
	}
	row, ok := h.m.Selected()
	if !ok || row.Ident().Number != 102 {
		t.Errorf("selected %+v", row)
	}
}
