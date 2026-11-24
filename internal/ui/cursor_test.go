package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The cursor's one rule (cursor.go): it stays in "your turn" while
// there is work there, keeps its row (or its row's place) across a
// rebuild, and never lands on whatever row took over its index.

// cursorAt is the selected row as "pin#N" or "home#N" ("" on no row).
func cursorAt(h *harness) string {
	h.t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	r, ok := h.m.Selected()
	if !ok {
		return ""
	}
	where := "home"
	if pinned(r) {
		where = "pin"
	}
	return fmt.Sprintf("%s#%d", where, r.Ident().Number)
}

// put places the cursor on a row by hand, as keys would.
func put(h *harness, kind string, n int, wantPinned bool) {
	h.t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	for i, r := range h.m.rows {
		if r.Kind == kind && r.Ident().Number == n && pinned(r) == wantPinned {
			h.m.selected = i
			h.m.cursorPlaced = true
			return
		}
	}
	h.t.Fatalf("no %s row #%d (pinned %v)", kind, n, wantPinned)
}

// setPRs replaces the fixture repo's open PRs (the next refresh reads them).
func setPRs(h *harness, prs ...*model.PR) {
	r := h.gh.Open[infra]
	r.PRs = prs
	h.gh.Open[infra] = r
}

func reviews(ns ...int) []*model.PR {
	var out []*model.PR
	for _, n := range ns {
		out = append(out, pr(infra, n, fmt.Sprintf("change %d", n), "bob", false))
	}
	return out
}

// TestADoneToDoGoesToTheNextToDo: a row of "your turn" that is done
// leaves the cursor on the next to-do (the one before, when it was the
// last), and only an emptied section sends it to the item's own row.
func TestADoneToDoGoesToTheNextToDo(t *testing.T) {
	h := newHarness(t)
	prs := reviews(101, 103, 104)
	setPRs(h, prs...)
	h.load()
	if got := cursorAt(h); got != "pin#101" {
		t.Fatalf("start on %s, want pin#101", got)
	}
	h.key('n') // placed: pin#103
	if got := cursorAt(h); got != "pin#103" {
		t.Fatalf("n went to %s", got)
	}
	prs[1].IsDraft = true // #103 is no longer a review on me
	h.key('r')
	if got := cursorAt(h); got != "pin#104" {
		t.Errorf("after #103 was done: %s, want the next to-do pin#104", got)
	}
	prs[2].IsDraft = true // #104, the last of the section
	h.key('r')
	if got := cursorAt(h); got != "pin#101" {
		t.Errorf("after the last was done: %s, want the one before, pin#101", got)
	}
	prs[0].IsDraft = true // the section empties
	h.key('r')
	if got := cursorAt(h); got != "home#101" {
		t.Errorf("after the section emptied: %s, want #101's own row", got)
	}
}

// TestAVanishedRowLandsOnItsNeighbor: a row that leaves the list leaves
// the cursor on its neighbor in the same section, even while rows above
// it come and go (an index would land elsewhere).
func TestAVanishedRowLandsOnItsNeighbor(t *testing.T) {
	h := newHarness(t)
	drafts := []*model.PR{}
	for _, n := range []int{201, 202, 203} {
		drafts = append(drafts, pr(infra, n, fmt.Sprintf("draft %d", n), "carol", true))
	}
	setPRs(h, append(reviews(101), drafts...)...)
	h.load()
	put(h, "pr", 202, false)
	// #202 goes; two new reviews arrive above it (each a pinned copy and
	// a home row)
	setPRs(h, append(reviews(101, 105, 106), drafts[0], drafts[2])...)
	h.key('r')
	if got := cursorAt(h); got != "home#203" {
		t.Errorf("cursor on %s, want #202's neighbor home#203", got)
	}
	setPRs(h, append(reviews(101, 105, 106), drafts[0])...) // the last row goes
	h.key('r')
	if got := cursorAt(h); got != "home#201" {
		t.Errorf("cursor on %s, want the neighbor above, home#201", got)
	}
}

// TestTheRowKeepsItsLine: rows arriving above the cursor leave its row
// on the same screen line.
func TestTheRowKeepsItsLine(t *testing.T) {
	h := newHarness(t)
	h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 14})
	var drafts []*model.PR
	for n := 201; n <= 215; n++ {
		drafts = append(drafts, pr(infra, n, fmt.Sprintf("draft %d", n), "carol", true))
	}
	setPRs(h, append(reviews(101), drafts...)...)
	h.load()
	put(h, "pr", 212, false)
	_ = h.screen() // the frame scrolls the cursor into view, at the bottom
	h.m.mu.Lock()
	h.m.listTop = h.m.selected - 3 // mid-screen: a slide would show
	h.m.mu.Unlock()
	_ = h.screen()
	h.m.mu.Lock()
	line := h.m.selected - h.m.listTop
	top := h.m.listTop
	h.m.mu.Unlock()
	if top == 0 {
		t.Fatal("the fixture must scroll")
	}
	setPRs(h, append(reviews(101, 105), drafts...)...)
	h.key('r')
	_ = h.screen()
	if got := cursorAt(h); got != "home#212" {
		t.Fatalf("cursor on %s", got)
	}
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if got := h.m.selected - h.m.listTop; got != line {
		t.Errorf("the row moved from line %d to %d (top %d -> %d)", line, got, top, h.m.listTop)
	}
}

// TestTheCursorFollowsYourTurnUntilAKey: until a key places it, the
// cursor follows the head of "your turn" as it changes; a key ends that.
func TestTheCursorFollowsYourTurnUntilAKey(t *testing.T) {
	h := newHarness(t)
	setPRs(h, reviews(101)...)
	h.load()
	setPRs(h, reviews(99, 101)...) // a new head
	h.key('r')                     // r refreshes; it does not place the cursor
	if got := cursorAt(h); got != "pin#99" {
		t.Errorf("an untouched cursor is on %s, want the new head pin#99", got)
	}
	h.key('j')
	was := cursorAt(h)
	setPRs(h, reviews(98, 99, 101)...)
	h.key('r')
	if got := cursorAt(h); got != was {
		t.Errorf("a placed cursor moved from %s to %s", was, got)
	}
}

// TestIdleGoesBackToYourTurn: idle_after_secs without a key, a cursor
// outside "your turn" goes back to its head; one on a to-do, in the
// detail or under a filter stays.
func TestIdleGoesBackToYourTurn(t *testing.T) {
	h := newHarness(t)
	setPRs(h, reviews(101, 103)...)
	h.load()
	tick := func() {
		h.m.mu.Lock()
		h.m.cfg.Display.IdleAfterSecs = 300
		h.m.lastInput = fx.Now() - 400
		gen := h.m.tickGen
		h.m.mu.Unlock()
		h.m.Update(tickMsg{gen})
		h.pump()
	}
	put(h, "issue", 7, false)
	tick()
	if got := cursorAt(h); got != "pin#101" {
		t.Errorf("idle off the section: %s, want the head pin#101", got)
	}
	put(h, "pr", 103, true)
	tick()
	if got := cursorAt(h); got != "pin#103" {
		t.Errorf("idle on a to-do moved it to %s", got)
	}
	put(h, "issue", 7, false)
	h.m.mu.Lock()
	h.m.filter = "rotate"
	h.m.refilter()
	h.m.mu.Unlock()
	put(h, "issue", 7, false)
	tick()
	if got := cursorAt(h); got != "home#7" {
		t.Errorf("idle under a filter moved it to %s", got)
	}
	h.m.mu.Lock()
	h.m.filter = ""
	h.m.refilter()
	h.m.mu.Unlock()
	put(h, "issue", 7, false)
	h.m.mu.Lock()
	h.m.view = ViewDetail
	h.m.mu.Unlock()
	tick()
	if got := cursorAt(h); got != "home#7" {
		t.Errorf("idle in the detail moved it to %s", got)
	}
}

// TestFilterEscGoesBackToTheRow: typing moves the cursor to the first
// match when its row stops matching; Esc puts it back where it was.
func TestFilterEscGoesBackToTheRow(t *testing.T) {
	h := newHarness(t)
	r := h.gh.Open[infra]
	r.Issues = append(r.Issues, issue(infra, 8, "wip notes", "carol"))
	h.gh.Open[infra] = r
	h.load()
	put(h, "issue", 7, false)
	var during string
	h.m.tp.Prompt = func(_ string, o workflow.PromptOpts) (string, bool) {
		h.m.mu.Lock()
		o.OnChange("wip")
		h.m.mu.Unlock()
		during = cursorAt(h)
		h.m.mu.Lock()
		o.OnChange("nothing matches this")
		h.m.mu.Unlock()
		return "", false
	}
	h.key('/')
	if during != "home#102" {
		t.Errorf("typing: cursor on %s, want the first match home#102 (not the old neighbor #8)", during)
	}
	if got := cursorAt(h); got != "home#7" {
		t.Errorf("after Esc: %s, want home#7", got)
	}
}

// TestRestoreKeepsTheHomeRow: a restart restores the copy the cursor was
// on, the home row of a to-do included.
func TestRestoreKeepsTheHomeRow(t *testing.T) {
	h := newHarness(t)
	h.m.restore = &Restore{Selected: &RestoreRef{Repo: "acme/infra", Number: 101}}
	h.load()
	if got := cursorAt(h); got != "home#101" {
		t.Errorf("restored onto %s, want home#101", got)
	}
	h.m.mu.Lock()
	r := h.m.captureRestore()
	h.m.mu.Unlock()
	if r.Selected == nil || r.Selected.Pinned {
		t.Errorf("snapshot %+v", r.Selected)
	}
}
