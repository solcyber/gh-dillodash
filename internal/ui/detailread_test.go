package ui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// gate holds one fake op until opened: the read a test wants in flight.
type gate struct {
	mu   sync.Mutex
	op   string
	open chan struct{}
}

func holdOp(h *harness, op string) *gate {
	g := &gate{op: op, open: make(chan struct{})}
	h.gh.BeforeOp = func(o string) {
		g.mu.Lock()
		ch, want := g.open, g.op
		g.mu.Unlock()
		if o == want {
			<-ch
		}
	}
	return g
}

func (g *gate) release() { close(g.open) }

// press sends a key without waiting for anything in flight.
func (h *harness) press(k tea.KeyPressMsg) {
	h.m.Update(k)
	drain(h.m, h.msgs)
}

var (
	enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}
	escKey   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

func runeKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// TestOpenIsInstant: Enter shows the listed PR on the same frame, keys
// that look work while the full read is out, keys that act wait for it.
func TestOpenIsInstant(t *testing.T) {
	h := newHarness(t)
	h.load()
	g := holdOp(h, "pr_detail")
	h.press(enterKey)
	if h.m.view != ViewDetail || !h.m.detailPartial {
		t.Fatalf("view = %s partial = %v; want the detail at once, partial", h.m.view, h.m.detailPartial)
	}
	s := h.screen()
	for _, want := range []string{"#101", "widen the deploy role", "reading #101"} {
		if !strings.Contains(s, want) {
			t.Errorf("partial detail lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "in 0 files") {
		t.Errorf("the list's copy has no file count; it must not say 0:\n%s", s)
	}
	h.m.tp.Confirm = func(string) bool { t.Error("A asked to approve a partial detail"); return false }
	h.press(runeKey('A'))
	if !strings.Contains(h.m.status, "still reading #101") || len(h.gh.Approvals) != 0 {
		t.Errorf("A on a partial detail: status %q approvals %v", h.m.status, h.gh.Approvals)
	}
	h.press(runeKey('j'))
	if strings.Contains(h.m.status, "busy") || strings.Contains(h.m.errText, "busy") {
		t.Errorf("j while reading said busy: %q", h.m.status)
	}
	g.release()
	h.pump()
	if h.m.detailPartial {
		t.Fatal("the full read landed but the detail is still partial")
	}
	if s := h.screen(); !strings.Contains(s, "looks fine") {
		t.Errorf("full detail lacks the comment:\n%s", s)
	}
}

// TestOpenKeptReadIsComplete: a full read kept from a moment ago opens
// complete with no second read, and acting keys work at once.
func TestOpenKeptReadIsComplete(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.named("esc")
	reads := h.gh.Reads["pr_detail"]
	g := holdOp(h, "pr_detail")
	h.press(enterKey)
	g.release() // the approval's refresh reads the detail again
	if h.m.detailPartial {
		t.Fatal("a kept, fresh read opened partial")
	}
	if !strings.Contains(h.screen(), "looks fine") {
		t.Errorf("the kept read is not on screen:\n%s", h.screen())
	}
	if h.gh.Reads["pr_detail"] != reads {
		t.Errorf("pr_detail reads %d → %d; a fresh kept read is not read again", reads, h.gh.Reads["pr_detail"])
	}
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('A')
	if got := h.gh.Approvals; len(got) != 1 || got[0] != 101 {
		t.Errorf("approvals = %v", got)
	}
}

// TestLateReadIsDropped: a read that lands after Esc (or after another
// item was opened) is kept for later but never shown.
func TestLateReadIsDropped(t *testing.T) {
	h := newHarness(t)
	h.load()
	g := holdOp(h, "pr_detail")
	h.press(enterKey)
	h.press(escKey)
	g.release()
	h.pump()
	if h.m.view != ViewList || h.m.detail != nil {
		t.Fatalf("a late read reopened the detail: view %s", h.m.view)
	}
	if _, ok := h.m.dcache.get(model.Ident{Repo: infra, Number: 101}); !ok {
		t.Error("the late read was not kept")
	}
}

// TestPrefetchOnRest: resting on a row reads its detail ahead, once; a
// stale timer (the cursor moved on) reads nothing.
func TestPrefetchOnRest(t *testing.T) {
	h := newHarness(t)
	h.load()
	id := model.Ident{Repo: infra, Number: 101}
	if h.m.prefetchFor != id {
		t.Fatalf("rest timer armed for %v, want %v", h.m.prefetchFor, id)
	}
	h.m.Update(prefetchMsg{gen: h.m.prefetchGen - 1, id: id})
	h.pump()
	if n := h.gh.Reads["pr_detail"]; n != 0 {
		t.Fatalf("a stale rest timer read %d details", n)
	}
	h.m.Update(prefetchMsg{gen: h.m.prefetchGen, id: id})
	h.pump()
	h.m.Update(prefetchMsg{gen: h.m.prefetchGen, id: id})
	h.pump()
	if n := h.gh.Reads["pr_detail"]; n != 1 {
		t.Fatalf("prefetch reads = %d, want 1", n)
	}
	h.press(enterKey)
	if h.m.detailPartial || !strings.Contains(h.screen(), "looks fine") {
		t.Errorf("the prefetched detail did not open complete:\n%s", h.screen())
	}
}

// TestAnswerIsAcknowledgedAtOnce: the frame after y shows a spinner,
// before the approval's round trip returns; a cancel leaves none.
func TestAnswerIsAcknowledgedAtOnce(t *testing.T) {
	h := newHarness(t)
	h.load()
	h.named("enter")
	h.m.tp.Confirm = nil // the real confirm modal
	g := holdOp(h, "approve")
	h.key('A')
	if h.m.modal == nil {
		t.Fatal("no confirm modal")
	}
	h.press(runeKey('y'))
	if h.m.statusKind != "spin" || h.m.status == "" {
		t.Errorf("after y: status %q (%s); want a spinner on the same frame", h.m.status, h.m.statusKind)
	}
	g.release()
	h.pump()
	if !strings.Contains(h.m.status, "review-approved #101") {
		t.Errorf("status after the approval = %q", h.m.status)
	}
	// a cancel says so and leaves no spinner
	h.m.UnmarkApproved(h.gh.PRs[model.Ident{Repo: infra, Number: 101}])
	h.key('A')
	h.key('n')
	if h.m.statusKind == "spin" || !strings.Contains(h.m.status, "cancelled") {
		t.Errorf("after n: status %q (%s)", h.m.status, h.m.statusKind)
	}
}

// settleBriefly applies whatever has arrived, waiting a moment for more.
func settleBriefly(h *harness) {
	select {
	case msg := <-h.msgs:
		h.m.Update(msg)
	case <-time.After(10 * time.Millisecond):
	}
	drain(h.m, h.msgs)
}

// TestOpenAdoptsThePrefetchInFlight: Enter while the rest's read is out
// shows the partial detail and takes that read's answer: one read, not two.
func TestOpenAdoptsThePrefetchInFlight(t *testing.T) {
	h := newHarness(t)
	h.load()
	id := model.Ident{Repo: infra, Number: 101}
	g := holdOp(h, "pr_detail")
	h.m.Update(prefetchMsg{gen: h.m.prefetchGen, id: id})
	h.press(enterKey)
	if !h.m.detailPartial {
		t.Fatal("the open did not show the partial detail while the prefetch is out")
	}
	g.release()
	h.pump()
	if h.m.detailPartial || !strings.Contains(h.screen(), "looks fine") {
		t.Errorf("the prefetch's answer did not land on the open detail:\n%s", h.screen())
	}
	if n := h.gh.Reads["pr_detail"]; n != 1 {
		t.Errorf("pr_detail reads = %d, want 1 (the open adopts the prefetch)", n)
	}
}

// TestAgingKeptReadHoldsActingKeys: a kept read past
// detailRevalidateSecs shows whole, but keys that act wait for the
// read behind it; a write forgets every kept read.
func TestAgingKeptReadHoldsActingKeys(t *testing.T) {
	h := newHarness(t)
	h.load()
	id := model.Ident{Repo: infra, Number: 101}
	h.named("enter")
	h.named("esc")
	e, ok := h.m.dcache.get(id)
	if !ok {
		t.Fatal("no kept read")
	}
	e.started -= detailRevalidateSecs + 1
	g := holdOp(h, "pr_detail")
	h.press(enterKey)
	if h.m.detailPartial || !strings.Contains(h.screen(), "looks fine") {
		t.Fatalf("an aging kept read must still show whole:\n%s", h.screen())
	}
	h.m.tp.Confirm = func(string) bool { t.Error("A asked to approve on an aging read"); return false }
	h.press(runeKey('A'))
	if !strings.Contains(h.m.status, "still reading") {
		t.Errorf("A on an aging read: status %q", h.m.status)
	}
	g.release()
	h.pump()
	if h.m.heldForRead() != "" {
		t.Error("keys still held after the re-read landed")
	}
	h.m.tp.Confirm = func(string) bool { return true }
	h.key('A')
	if _, ok := h.m.dcache.get(id); ok {
		t.Error("a kept read survived a write")
	}
}
