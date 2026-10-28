package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fortress"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// fortressHarness is the harness with the branches/commits layer on the
// fake, the clock a week past the fixture, and the coin pinned.
func fortressHarness(t *testing.T, city string, repos ...model.Repo) *harness {
	t.Helper()
	h := newHarness(t, repos...)
	was := clock.Now
	now := t0.Add(7 * 24 * time.Hour)
	clock.Now = func() time.Time { return now }
	t.Cleanup(func() { clock.Now = was })
	h.gh.FortressData = map[model.Repo]ghapi.FortressData{}
	for _, r := range h.m.repos {
		h.gh.FortressData[r] = ghapi.FortressData{
			Default: "main", BranchTotal: 3,
			Branches: []ghapi.FortressBranch{
				{Name: "main", CommittedDate: now},
				{Name: "feature-bob", CommittedDate: t0},                               // has PR #101: not a ruin
				{Name: "old-experiment", CommittedDate: now.Add(-40 * 24 * time.Hour)}, // a ruin
			},
			Commits: []ghapi.FortressCommit{
				{Oid: strings.Repeat("1", 40), MessageHeadline: "tidy the deploy role", CommittedDate: now.Add(-6 * time.Hour)},
				{Oid: strings.Repeat("2", 40), MessageHeadline: "bump the theme", CommittedDate: now.Add(-3 * 24 * time.Hour)},
			},
		}
	}
	h.m.fortress = &fortressState{coin: func() string { return city }}
	h.load()
	return h
}

func (h *harness) tab() {
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	h.pump()
}

func TestFortressOpensAndRenders(t *testing.T) {
	h := fortressHarness(t, "manhattan")
	h.key('d')
	if h.m.view != ViewFortress {
		t.Fatalf("view = %s", h.m.view)
	}
	if !h.m.fortress.wanted || h.m.fortress.data == nil {
		t.Fatalf("the layer was not fetched: wanted=%v data=%v", h.m.fortress.wanted, h.m.fortress.data)
	}
	s := h.screen()
	for _, want := range []string{"Fortress of Manhattan", "dwarves", "goblins", "ruins", "statues",
		"legend", "PR awaiting review", "street = days open", "lanes W to E: 1 infra",
		"look", "infra#101", "awaiting review", "by bob", "widen the deploy role", "7 days old",
		"Strike the earth", "Scouts sent out", "14th", "zoom", "city"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	v := h.m.fortress.view
	ids := map[string]*fortress.Item{}
	for _, it := range v.Items {
		ids[it.ID] = it
	}
	if _, ok := ids["branch:infra:old-experiment"]; !ok {
		t.Error("the old branch is not a ruin")
	}
	if _, ok := ids["branch:infra:feature-bob"]; ok {
		t.Error("a branch with a PR is a ruin")
	}
	if it := ids["commit:infra:"+strings.Repeat("1", 40)]; it == nil || it.Color != "white" {
		t.Errorf("fresh commit: %+v", it)
	}
	// an item glyph stands on its cell: the draft (o in ascii), the statue (^)
	lines := strings.Split(s, "\n")
	cellGlyph := func(it *fortress.Item) rune {
		y, x := 2+it.Cell.R-v.Top, fortress.Margin+it.Cell.C-v.Left
		return []rune(lines[y])[x]
	}
	if g := cellGlyph(ids["pr:infra#102"]); g != 'o' {
		t.Errorf("draft glyph = %q", g)
	}
	if g := cellGlyph(ids["commit:infra:"+strings.Repeat("2", 40)]); g != '^' {
		t.Errorf("statue glyph = %q", g)
	}
	if g := cellGlyph(ids["pr:infra#101"]); g != 'X' {
		t.Errorf("the cursor is not on #101: %q", g)
	}
	if u := h.m.fortressURL(); u != "https://github.com/acme/infra/pull/101" {
		t.Errorf("url = %q", u)
	}
}

func TestFortressCursorEnterAndBack(t *testing.T) {
	h := fortressHarness(t, "chicago")
	h.key('d')
	if !strings.Contains(h.screen(), "Fortress of Chicago") {
		t.Fatalf("screen:\n%s", h.screen())
	}
	v := h.m.fortress.view
	cur := v.Cur
	h.key('l')
	if v.Cur != (fortress.Cell{R: cur.R, C: cur.C + 1}) {
		t.Errorf("l moved to %v from %v", v.Cur, cur)
	}
	h.key('h')
	if v.Cur != cur {
		t.Errorf("h went to %v", v.Cur)
	}
	h.tab()
	if v.Cur == cur {
		t.Error("tab stayed")
	}
	if !strings.Contains(h.screen(), "look") {
		t.Error("the look panel is gone")
	}
	h.key('j')
	h.key('k')
	// back on the PR, Enter opens it
	v.Cur = cur
	h.named("enter")
	if h.m.view != ViewDetail || h.m.detailKind != "pr" {
		t.Fatalf("view=%s kind=%s err=%q", h.m.view, h.m.detailKind, h.m.errText)
	}
	if !strings.Contains(h.screen(), "A change by bob") {
		t.Errorf("detail:\n%s", h.screen())
	}
	h.key('q')
	if h.m.view != ViewFortress {
		t.Fatalf("q went to %s", h.m.view)
	}
	h.key('d')
	if h.m.view != ViewList {
		t.Fatalf("d went to %s", h.m.view)
	}
	if !strings.Contains(h.screen(), "pull requests") {
		t.Error("the list is not back")
	}
	// d again lands in the fortress without another scouting trip
	reads := h.gh.Reads["fortress"]
	h.key('d')
	if h.m.view != ViewFortress || h.gh.Reads["fortress"] != reads {
		t.Errorf("view=%s fortress reads %d -> %d", h.m.view, reads, h.gh.Reads["fortress"])
	}
	h.named("esc")
	if h.m.view != ViewList {
		t.Errorf("esc went to %s", h.m.view)
	}
}

func TestFortressZoomCityAndNothingToOpen(t *testing.T) {
	h := fortressHarness(t, "manhattan")
	h.key('d')
	h.key('+')
	if !strings.Contains(h.screen(), "1 row = 1 day") {
		t.Errorf("+ did not zoom:\n%s", h.screen())
	}
	if u := h.m.fortress.view.Under(); len(u) == 0 || u[0].ID != "pr:infra#101" {
		t.Errorf("zoom lost the cursor: %+v", u)
	}
	h.key('-')
	if !strings.Contains(h.screen(), "not to scale") {
		t.Errorf("- did not fit:\n%s", h.screen())
	}
	h.key('c')
	if !strings.Contains(h.screen(), "Fortress of Chicago") || h.m.view != ViewFortress {
		t.Errorf("c did not swap:\n%s", h.screen())
	}
	h.key('c')
	if !strings.Contains(h.screen(), "Fortress of Manhattan") {
		t.Error("c did not swap back")
	}
	// a ruin opens nothing (o would); empty ground says so too
	v := h.m.fortress.view
	for _, it := range v.Items {
		if it.Kind == fortress.KindBranch {
			v.Cur = it.Cell
		}
	}
	h.named("enter")
	if h.m.view != ViewFortress || !strings.Contains(h.m.status, "nothing to open here") {
		t.Errorf("view=%s status=%q", h.m.view, h.m.status)
	}
	if u := h.m.fortressURL(); !strings.HasSuffix(u, "/tree/old-experiment") {
		t.Errorf("ruin url = %q", u)
	}
	v.Cur = fortress.Cell{R: 0, C: 0}
	h.named("enter")
	if !strings.Contains(h.m.status, "tab jumps to the next thing") {
		t.Errorf("status = %q", h.m.status)
	}
	if h.m.fortressURL() != "" {
		t.Error("empty ground has a url")
	}
	// a small terminal says what it needs
	h.m.Update(tea.WindowSizeMsg{Width: 50, Height: 18})
	if !strings.Contains(h.screen(), "at least 60x20") {
		t.Errorf("small screen:\n%s", h.screen())
	}
}

func TestFortressLanesPerRepo(t *testing.T) {
	h := fortressHarness(t, "manhattan", infra, portal)
	h.key('d')
	s := h.screen()
	if !strings.Contains(s, "1 infra") || !strings.Contains(s, "2 web-portal") {
		t.Errorf("lanes:\n%s", s)
	}
	v := h.m.fortress.view
	lanes := map[int]bool{}
	for _, it := range v.Items {
		lanes[it.Lane] = true
		if it.Repo == portal && it.Lane != 1 {
			t.Errorf("%s in lane %d", it.ID, it.Lane)
		}
	}
	if !lanes[0] || !lanes[1] {
		t.Errorf("lanes used: %v", lanes)
	}
	if d, _, r, st, _ := v.Counts(); d != 4 || r != 2 || st != 4 {
		t.Errorf("counts: dwarves %d ruins %d statues %d", d, r, st)
	}
}
