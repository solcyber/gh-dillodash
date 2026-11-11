package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestFirstLoadShowsTheListFirst: the first refresh sends the list on
// its own before the whole result, and the whole result clears the
// list-only state; later refreshes send no list-only message.
func TestFirstLoadShowsTheListFirst(t *testing.T) {
	h := newHarness(t)
	var kinds []string
	h.m.sendFn = func(msg tea.Msg) {
		switch msg.(type) {
		case refreshListMsg:
			kinds = append(kinds, "list")
		case refreshDoneMsg:
			kinds = append(kinds, "done")
		}
		h.msgs <- msg
	}
	h.load()
	if strings.Join(kinds, ",") != "list,done" {
		t.Fatalf("first load sent %v", kinds)
	}
	if h.m.listOnly {
		t.Error("the whole refresh landed but the list-only state stayed")
	}
	if s := h.screen(); !strings.Contains(s, "widen the deploy role") {
		t.Errorf("list missing:\n%s", s)
	}
	kinds = nil
	h.key('r')
	if strings.Join(kinds, ",") != "done" {
		t.Errorf("a later refresh sent %v", kinds)
	}
}

// TestListOnlyHoldsTheKeysThatAct: while only the first load's list is
// on screen, a key that acts says the workflows are still loading and
// posts nothing; a key that looks still works.
func TestListOnlyHoldsTheKeysThatAct(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	if h.m.view != ViewDetail {
		t.Fatalf("view %s", h.m.view)
	}
	h.m.listOnly = true
	h.key('A')
	if !strings.Contains(h.m.status, "still reading the workflows") {
		t.Errorf("status %q", h.m.status)
	}
	if len(h.gh.Approvals) != 0 || len(h.gh.Calls) != 0 {
		t.Errorf("posted while list-only: %+v", h.gh.Calls)
	}
	h.named("esc")
	if h.m.view != ViewList {
		t.Errorf("Esc (a key that looks) did not go back: %s", h.m.view)
	}
}
