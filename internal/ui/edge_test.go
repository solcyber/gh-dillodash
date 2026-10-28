package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// TestTinySizesDoNotPanic renders every view, the help overlay and a
// modal at absurd terminal sizes: nothing may panic.
func TestTinySizesDoNotPanic(t *testing.T) {
	h := newHarness(t)
	h.load()
	sizes := [][2]int{{1, 1}, {2, 2}, {10, 3}, {3, 10}, {20, 5}, {40, 8}}
	views := []struct {
		name string
		prep func()
	}{
		{"list", func() {}},
		{"empty", func() {
			h.m.mu.Lock()
			h.m.filter = "zzz-no-such-thing"
			h.m.refilter()
			h.m.mu.Unlock()
		}},
		{"help", func() {
			h.m.mu.Lock()
			h.m.filter = ""
			h.m.refilter()
			h.m.mu.Unlock()
			h.key('?')
		}},
		{"detail", func() {
			h.named("esc") // close the help
			h.named("enter")
		}},
		{"files", func() { h.key('f') }},
		{"patch", func() { h.named("enter") }},
		{"fortress", func() {
			h.named("esc")
			h.named("esc")
			h.named("esc")
			h.key('d')
		}},
		{"loading", func() {
			h.key('d')
			h.m.mu.Lock()
			h.m.loaded = false
			h.m.mu.Unlock()
		}},
		{"load-failed", func() {
			h.m.mu.Lock()
			h.m.errText = "boom"
			h.m.mu.Unlock()
		}},
	}
	for _, v := range views {
		v.prep()
		for _, sz := range sizes {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s at %dx%d panicked: %v", v.name, sz[0], sz[1], r)
					}
				}()
				h.m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
				_ = h.m.View()
			}()
		}
		h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	}
}

// TestGotoEdgeCases: repo#N for a repo that is not configured, a
// non-number, and a number that is neither a PR nor an issue.
func TestGotoEdgeCases(t *testing.T) {
	h := newHarness(t, infra, portal)
	h.load()
	for _, tc := range []struct{ typed, want string }{
		{"nowhere#7", `no repo named "nowhere"`},
		{"abc", "is not a number"},
		{"999", "infra#999"}, // the first repo is the default
	} {
		h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return tc.typed, true }
		h.key(':')
		if h.m.view != ViewList {
			t.Fatalf("%q opened %s", tc.typed, h.m.view)
		}
		if got := h.m.status + h.m.errText; !strings.Contains(got, tc.want) {
			t.Errorf("%q: status %q lacks %q", tc.typed, got, tc.want)
		}
	}
	// repo#N names the second repo
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "web-portal#101", true }
	h.key(':')
	if h.m.view != ViewDetail || h.m.detailRef() != "web-portal#101" {
		t.Fatalf("view=%s ref=%s", h.m.view, h.m.detailRef())
	}
}

// TestGotoWithNoRepos never indexes an empty repo list.
func TestGotoWithNoRepos(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.repos = nil
	h.m.mu.Unlock()
	h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return "7", true }
	h.key(':')
	if h.m.view != ViewList || !strings.Contains(h.m.status, "no repos") {
		t.Fatalf("view=%s status=%q", h.m.view, h.m.status)
	}
}

// TestDetailSurvivesVanishingPR: the open PR drops out of the list and
// its detail can no longer be fetched; the refresh still lands, the
// detail stays readable with the error on the status line, and q goes
// back to a list with a valid cursor.
func TestDetailSurvivesVanishingPR(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view = %s", h.m.view)
	}
	id := model.Ident{Repo: infra, Number: 101}
	h.gh.Open[infra] = ghapi.ListResult{PRs: h.gh.Open[infra].PRs[1:], Issues: h.gh.Open[infra].Issues}
	delete(h.gh.PRs, id)
	h.key('r')
	if h.m.view != ViewDetail || h.m.detail == nil {
		t.Fatalf("view=%s detail=%v", h.m.view, h.m.detail)
	}
	if !strings.Contains(h.m.errText, "couldn't refresh #101") {
		t.Errorf("errText = %q", h.m.errText)
	}
	if !strings.Contains(h.screen(), "A change by bob") {
		t.Errorf("detail no longer readable:\n%s", h.screen())
	}
	h.key('q')
	if h.m.view != ViewList {
		t.Fatalf("view = %s", h.m.view)
	}
	if _, ok := h.m.Selected(); !ok {
		t.Errorf("cursor off the rows: selected=%d of %d", h.m.selected, len(h.m.rows))
	}
	for _, r := range h.m.rows {
		if p, ok := r.Item.(*model.PR); ok && p.Number == 101 {
			t.Errorf("the vanished PR is still listed:\n%s", h.screen())
		}
	}
}

// TestDetailListedFollowsRefresh: the listed PR behind an open detail is
// re-pointed at the refreshed list's object, so workflow cards and the
// header icon read fresh data rather than the pre-refresh copy.
func TestDetailListedFollowsRefresh(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	was := h.m.detailListed
	if was == nil {
		t.Fatal("no listed PR")
	}
	fresh := *was
	fresh.Title = "widen the deploy role (v2)"
	h.gh.Open[infra] = ghapi.ListResult{PRs: []*model.PR{&fresh, h.gh.Open[infra].PRs[1]}, Issues: h.gh.Open[infra].Issues}
	h.key('r')
	if h.m.detailListed != &fresh {
		t.Errorf("detailListed still points at the old object (%p vs %p)", h.m.detailListed, &fresh)
	}
}

// TestEmptyListAfterFilterKeys: with nothing matching, the movement and
// open keys are no-ops and the note says how to clear it.
func TestEmptyListAfterFilterKeys(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.mu.Lock()
	h.m.filter = "nothing-matches-this"
	h.m.refilter()
	h.m.mu.Unlock()
	if len(h.m.rows) != 0 {
		t.Fatalf("rows = %d", len(h.m.rows))
	}
	for _, k := range []rune{'j', 'k', 'g', 'G', ' ', 'y', 'o', 'D'} {
		h.key(k)
	}
	h.named("enter")
	if h.m.view != ViewList || h.m.selected != 0 {
		t.Fatalf("view=%s selected=%d", h.m.view, h.m.selected)
	}
	if !strings.Contains(h.screen(), "nothing matches the filter") {
		t.Errorf("screen:\n%s", h.screen())
	}
}

// TestZeroReposRenders: an empty repo list (demo, tests) draws without
// a panic and with no repos text.
func TestZeroReposRenders(t *testing.T) {
	h := newHarness(t)
	h.m.mu.Lock()
	h.m.repos = nil
	h.m.multi = false
	h.m.cfg.Reviews.Repos = nil
	h.m.mu.Unlock()
	h.load()
	s := h.screen()
	if !h.m.loaded || len(h.m.rows) != 0 {
		t.Fatalf("loaded=%v rows=%d err=%q", h.m.loaded, len(h.m.rows), h.m.errText)
	}
	if !strings.Contains(s, "all clear") {
		t.Errorf("screen:\n%s", s)
	}
}

// TestErrorMessagesNameTheThing: failed fetches say what failed and for
// which item.
func TestErrorMessagesNameTheThing(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	boom := &ghapi.Error{Method: "GET", URL: "x", Status: 403, Message: "forbidden"}
	for _, tc := range []struct {
		op   string
		key  rune
		want string
	}{
		{"files", 'f', "couldn't load the files of #101"},
		{"diff", 'v', "couldn't fetch the diff of #101"},
		{"users", 't', "couldn't list who is assignable in acme/infra"},
	} {
		h.gh.Fail = map[string]error{tc.op: boom}
		h.key(tc.key)
		if !strings.Contains(h.m.errText, tc.want) {
			t.Errorf("%s: errText %q lacks %q", tc.op, h.m.errText, tc.want)
		}
		if h.m.view != ViewDetail {
			t.Fatalf("%s: view = %s", tc.op, h.m.view)
		}
	}
	h.gh.Fail = map[string]error{"approve": boom}
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('a')
	if !strings.Contains(h.m.errText, "review approval of #101 failed") || !strings.Contains(h.m.errText, "Pull requests") {
		t.Errorf("approve errText = %q", h.m.errText)
	}
}

// TestGotoOwnerQualified: ":" accepts owner/repo#N, and tells two repos
// with the same name under different owners apart.
func TestGotoOwnerQualified(t *testing.T) {
	beta := model.Repo{Owner: "beta", Name: "infra"}
	h := newHarness(t, infra, beta)
	h.load()
	if !h.m.multiOwner {
		t.Fatal("two owners should set multiOwner")
	}
	for _, tc := range []struct{ typed, ref string }{
		{"acme/infra#101", "acme/infra#101"},
		{"beta/infra#101", "beta/infra#101"},
		{"#101", "acme/infra#101"}, // the first repo is the default
	} {
		h.m.tp.Prompt = func(string, workflow.PromptOpts) (string, bool) { return tc.typed, true }
		h.key(':')
		if h.m.view != ViewDetail || h.m.detailRef() != tc.ref {
			t.Fatalf("%q: view=%s ref=%s", tc.typed, h.m.view, h.m.detailRef())
		}
		h.named("esc")
	}
	// the list rows carry the owner too
	if s := h.screen(); !strings.Contains(s, "acme/infra#101") || !strings.Contains(s, "beta/infra#101") {
		t.Errorf("rows lack owner-qualified refs:\n%s", s)
	}
}

// TestWhereQualifiesForeignRepo: with one review repo, a PR from any
// other repo (a release repo) still gets its repo in the heading.
func TestWhereQualifiesForeignRepo(t *testing.T) {
	h := newHarness(t, infra)
	h.load()
	if got := h.m.where(infra, 5); got != "#5" {
		t.Errorf("own repo: %q", got)
	}
	if got := h.m.where(portal, 5); got != "web-portal#5" {
		t.Errorf("foreign repo: %q", got)
	}
	h.m.mu.Lock()
	h.m.multi = true
	h.m.mu.Unlock()
	if got := h.m.where(infra, 5); got != "infra#5" {
		t.Errorf("multi: %q", got)
	}
	h.m.mu.Lock()
	h.m.multi, h.m.repos = false, nil
	h.m.mu.Unlock()
	if got := h.m.where(portal, 5); got != "web-portal#5" {
		t.Errorf("no repos: %q", got)
	}
	// and the detail heading of a foreign PR shows it
	h.m.mu.Lock()
	h.m.repos = []model.Repo{infra}
	foreign := pr(portal, 7, "release 2026.10", "bob", false)
	h.m.showDetail(foreign, "pr", 0, nil)
	h.m.mu.Unlock()
	if s := h.screen(); !strings.Contains(s, "web-portal#7") {
		t.Errorf("heading lacks the repo:\n%s", s)
	}
	if ref := h.m.detailRef(); ref != "web-portal#7" {
		t.Errorf("detailRef = %q", ref)
	}
}

// TestNarrowLayouts: at 50 and 40 columns the list keeps its titles and
// the bar keeps its help chip; files keep their base name; the help
// scrolls when it overflows.
func TestNarrowLayouts(t *testing.T) {
	h := newHarness(t)
	p := h.gh.Open[infra].PRs[0]
	h.gh.Files = map[model.Ident][]model.PRFile{p.Ident(): {{Filename: "terraform/environments/production/eu-west-1/modules/iam/deploy-role/main.tf", Status: "modified", Additions: 3, Deletions: 1}}}
	h.load()
	for _, sz := range [][2]int{{50, 12}, {40, 10}} {
		h.m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		s := h.screen()
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		if !strings.Contains(lines[0], "dillodash") {
			t.Errorf("%dx%d: header lost the name:\n%s", sz[0], sz[1], s)
		}
		if !strings.Contains(s, "#101") || !strings.Contains(s, "widen") {
			t.Errorf("%dx%d: the title vanished:\n%s", sz[0], sz[1], s)
		}
		bar := strings.TrimRight(lines[len(lines)-1], " ")
		if !strings.HasSuffix(bar, "?  help") {
			t.Errorf("%dx%d: bar %q", sz[0], sz[1], bar)
		}
	}
	h.m.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	h.named("enter")
	if s := h.screen(); !strings.Contains(strings.Split(s, "\n")[0], "#101") {
		t.Errorf("detail header at 50 cols:\n%s", s)
	}
	h.key('f')
	if s := h.screen(); !strings.Contains(s, "main.tf") {
		t.Errorf("files at 50 cols lose the base name:\n%s", s)
	}
	h.named("esc")
	h.named("esc")
	// the help at 80x12 overflows and scrolls
	h.m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	h.key('?')
	if h.m.overlay != "help" {
		t.Fatal("help not open")
	}
	if s := h.screen(); !strings.Contains(s, "j/k scroll") {
		t.Errorf("overflowing help shows the scroll hint:\n%s", s)
	}
	h.key('j')
	h.key('j')
	h.key('j')
	if h.m.helpTop != 3 || h.m.overlay != "help" {
		t.Errorf("j scrolls the help: top=%d overlay=%q", h.m.helpTop, h.m.overlay)
	}
	h.key('x')
	if h.m.overlay != "" {
		t.Error("any other key closes it")
	}
	h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
}

func TestFilterChipSurvivesNarrow(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	h.m.mu.Lock()
	h.m.filter = "widen"
	h.m.refilter()
	h.m.mu.Unlock()
	lines := strings.Split(h.screen(), "\n")
	if !strings.Contains(lines[1], "widen") {
		t.Errorf("summary row at 60 cols lost the filter chip: %q", lines[1])
	}
	h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
}

func TestStatusEllipsisAndMessageOverlay(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	long := "approval failed: " + strings.Repeat("the token lacks a permission ", 10) + "END"
	h.m.mu.Lock()
	h.m.fail(long)
	h.m.mu.Unlock()
	lines := strings.Split(h.screen(), "\n")
	status := strings.TrimRight(lines[len(lines)-2], " ")
	if !strings.HasSuffix(status, text.Ellipsis) || strings.Contains(status, "END") {
		t.Errorf("a long error ends in an ellipsis: %q", status)
	}
	h.key('e')
	if h.m.overlay != "message" {
		t.Fatalf("e opens the message overlay, got %q", h.m.overlay)
	}
	if s := h.screen(); !strings.Contains(s, "END") || !strings.Contains(s, "last message") {
		t.Errorf("the overlay shows the whole message:\n%s", s)
	}
	h.named("esc")
	if h.m.overlay != "" {
		t.Error("esc closes it")
	}
	h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
}
