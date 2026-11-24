package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/deploys"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/keymap"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Whose move a row is (workflow.Turn) is read here for every row, the
// core's PRs included, and drives what the screen says to do: the "your
// turn" section pinned at the top, the summary's three counts, the hint
// line, the key bar's first offer, n/N and the terminal title. One
// answer, so the parts of the screen can no longer disagree.

// turnOf is the row's turn (lock held).
func (m *Model) turnOf(row workflow.Row) workflow.Turn {
	switch row.Kind {
	case "header", "issue":
		return workflow.Turn{}
	case "pr":
		pr, ok := row.Item.(*model.PR)
		if !ok || pr == nil {
			return workflow.Turn{}
		}
		return m.prTurn(pr)
	}
	return m.wf.TurnOf(m, row)
}

// prTurn is a core PR's: a review that waits on the viewer, or their own
// PR stuck on something only they can clear.
func (m *Model) prTurn(pr *model.PR) workflow.Turn {
	if pr.State == "MERGED" {
		// a PR merged from here: its deploy is the viewer's when an
		// environment waits on them; D reads one never read
		if st, ok := pr.Get("deploy").(deploys.State); ok {
			if due := st.Due(); st.Phase == "waiting" && len(due) > 0 {
				return workflow.You(keymap.S(keymap.Deploys), "approve "+strings.Join(due, ", "), "deploy", workflow.PrioDeploy)
			}
			if st.Phase == "running" {
				return workflow.Running("deploying", time.Time{})
			}
		} else if !deploysDone(pr) {
			return workflow.Turn{Kind: workflow.TurnOthers, What: "its deploys (D reads them)", Key: keymap.S(keymap.Deploys), Verb: "read its deploys"}
		}
		return workflow.Turn{Kind: workflow.TurnDone, What: "merged"}
	}
	label, _, needsYou, _ := m.decision(pr, nil)
	mine := m.me != "" && strings.EqualFold(pr.Author.Login, m.me)
	author := text.OrQ(pr.Author.Login)
	switch {
	case pr.IsDraft:
		return workflow.Others(author + " (draft)")
	case mine && needsYou && label == model.LabelStuck:
		why := model.StuckOf(pr, nil, m.me).Reason
		if why == "" {
			why = label
		}
		return workflow.You("↵", "clear it: "+why, "fix", workflow.PrioOwnFix)
	case mine:
		// my own PR waits on its reviewers until something only I can
		// clear stops it
		return workflow.Others("its reviewers")
	case needsYou && label == model.LabelReReview:
		return workflow.You("↵", "re-review it (it changed since you approved)", "review", workflow.PrioReview)
	case needsYou:
		return workflow.You("↵", "review it", "review", workflow.PrioReview)
	case label == model.LabelApprovedMe:
		return workflow.Others(author + " to merge it")
	}
	return workflow.Others(author)
}

// pinned reports a row in the "your turn" section (a copy of a row that
// also stands in its own section).
func pinned(r workflow.Row) bool {
	b, _ := r.Meta["pinned"].(bool)
	return b
}

// turnHeader marks the "your turn" section's header.
const turnHeader = "your turn"

// yourTurnRows is the "your turn" section for rows: its header and a
// copy of every row that is the viewer's move, most pressing first; with
// nothing to do, the header alone says so (and what is moving instead).
// A filter keeps the section to its matches and drops the empty line.
func (m *Model) yourTurnRows(rows []workflow.Row, filtered bool) []workflow.Row {
	type pin struct {
		row  workflow.Row
		turn workflow.Turn
		at   int
	}
	var pins []pin
	seen := map[string]bool{}
	for i, r := range rows {
		if r.Kind == "header" {
			continue
		}
		t := m.turnOf(r)
		if t.Kind != workflow.TurnYou {
			continue
		}
		key := r.Kind + "\x00" + r.Ident().String()
		if seen[key] {
			continue
		}
		seen[key] = true
		meta := map[string]any{"pinned": true}
		for k, v := range r.Meta {
			meta[k] = v
		}
		pins = append(pins, pin{workflow.Row{Kind: r.Kind, Item: r.Item, Meta: meta}, t, i})
	}
	sort.SliceStable(pins, func(i, j int) bool {
		if pins[i].turn.Prio != pins[j].turn.Prio {
			return pins[i].turn.Prio < pins[j].turn.Prio
		}
		return pins[i].at < pins[j].at
	})
	if len(pins) == 0 {
		if filtered || len(rows) == 0 {
			return nil // nothing listed at all: the empty screen says so
		}
		return []workflow.Row{{Kind: "header", Item: turnHeader, Meta: map[string]any{"icon": "shield", "turn": true}}}
	}
	out := []workflow.Row{{Kind: "header", Item: fmt.Sprintf("%s (%d)", turnHeader, len(pins)), Meta: map[string]any{"icon": "shield", "turn": true}}}
	for _, p := range pins {
		out = append(out, p.row)
	}
	return out
}

// turnCounts tallies the home rows (not the pinned copies) by kind, the
// viewer's by noun, and the soonest landing of what runs.
type turnCounts struct {
	you, running, others, failed int
	nouns, nounPrio              map[string]int
	nounOrder                    []string
	eta                          time.Time
}

func (m *Model) countTurns() turnCounts {
	c := turnCounts{nouns: map[string]int{}, nounPrio: map[string]int{}}
	seen := map[string]bool{}
	for _, r := range m.rows {
		if r.Kind == "header" || pinned(r) {
			continue
		}
		key := r.Kind + "\x00" + r.Ident().String()
		if seen[key] {
			continue
		}
		seen[key] = true
		t := m.turnOf(r)
		switch t.Kind {
		case workflow.TurnYou:
			c.you++
			n := t.Noun
			if n == "" {
				n = "thing"
			}
			if c.nouns[n] == 0 {
				c.nounOrder = append(c.nounOrder, n)
				c.nounPrio[n] = t.Prio
			}
			c.nouns[n]++
			c.nounPrio[n] = min(c.nounPrio[n], t.Prio)
		case workflow.TurnRunning:
			c.running++
			if !t.ETA.IsZero() && (c.eta.IsZero() || t.ETA.Before(c.eta)) {
				c.eta = t.ETA
			}
		case workflow.TurnOthers:
			c.others++
		case workflow.TurnFailed:
			c.failed++
		}
	}
	sort.SliceStable(c.nounOrder, func(i, j int) bool { return c.nounPrio[c.nounOrder[i]] < c.nounPrio[c.nounOrder[j]] })
	return c
}

// youText is "2 deploys · 1 release" (the viewer's, by noun).
func (c turnCounts) youText() string {
	var parts []string
	for _, n := range c.nounOrder {
		parts = append(parts, plural(c.nouns[n], n))
	}
	return strings.Join(parts, " "+glyphs.G("sep")+" ")
}

// landsText is " · lands ~23:20" for the soonest running ETA, in the
// header clock's zone ("" when none is known).
func (m *Model) landsText(eta time.Time) string {
	if eta.IsZero() {
		return ""
	}
	at := eta.Local()
	if m.clockLoc != nil {
		at = m.clockLoc.In(eta)
	}
	if eta.Before(clock.Now()) {
		return ", due " + at.Format("15:04")
	}
	return ", lands ~" + at.Format("15:04")
}

// turnEmptyText is the empty "your turn" line: nothing waits on you, and
// what is happening instead.
func (m *Model) turnEmptyText() string {
	c := m.countTurns()
	parts := []string{"nothing waits on you"}
	if c.running > 0 {
		parts = append(parts, fmt.Sprintf("%d running%s", c.running, m.landsText(c.eta)))
	}
	if c.others > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting on others", c.others))
	}
	return strings.Join(parts, "  "+glyphs.G("sep")+"  ")
}

// nextTurn is the next (dir 1) or previous (dir -1) row of the "your
// turn" section from the cursor, wrapping (from a row outside it, the
// first or the last); -1 when the section is empty. n walks the to-do
// list in its own order, most pressing first.
func (m *Model) nextTurn(dir int) int {
	var pins []int
	for i, r := range m.rows {
		if pinned(r) {
			pins = append(pins, i)
		}
	}
	if len(pins) == 0 {
		return -1
	}
	at := -1
	for k, i := range pins {
		if i == m.selected {
			at = k
		}
	}
	switch {
	case at < 0 && dir > 0:
		return pins[0]
	case at < 0:
		return pins[len(pins)-1]
	}
	return pins[((at+dir)%len(pins)+len(pins))%len(pins)]
}

// turnIndex is the selected row's place in the "your turn" section ("2
// of 3"): the pinned copy's, or the copy of the home row selected; 0
// when it is not the viewer's move.
func (m *Model) turnIndex() (int, int) {
	sel, ok := m.Selected()
	if !ok {
		return 0, 0
	}
	selKey := sel.Kind + "\x00" + sel.Ident().String()
	at, total := 0, 0
	for _, r := range m.rows {
		if !pinned(r) {
			continue
		}
		total++
		if r.Kind+"\x00"+r.Ident().String() == selKey {
			at = total
		}
	}
	return at, total
}

// windowTitle is the terminal's title: what waits on the viewer, else
// what runs, else the name, so a tab or a tmux window says it while the
// dashboard is out of sight.
func (m *Model) windowTitle() string {
	if !m.loaded {
		return "dillodash"
	}
	c := m.countTurns()
	switch {
	case c.you > 0:
		return fmt.Sprintf("dillodash %s %d on you", glyphs.G("arrow"), c.you)
	case c.failed > 0:
		return fmt.Sprintf("dillodash %s %d failed", glyphs.G("sep"), c.failed)
	case c.running > 0:
		return fmt.Sprintf("dillodash %s %d running", glyphs.G("sep"), c.running)
	}
	return "dillodash"
}

// hasTurns gates n in the key bar: something is the viewer's move.
func (m *Model) hasTurns() bool { return m.nextTurn(1) >= 0 }

// doNextTurn is n (N: backwards): the next row that is the viewer's move.
func doNextTurn(m *Model, k widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	dir := 1
	if k.Rune == 'N' {
		dir = -1
	}
	i := m.nextTurn(dir)
	if i < 0 {
		m.notice("nothing waits on you")
		return nil
	}
	was := m.selected
	m.selected = i
	m.cursorPlaced = true
	m.leaveTrail(was)
	return nil
}
