package ui

import (
	"regexp"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// Where the cursor goes when the rows change under it. Every refresh,
// write, filter keystroke and toggle rebuilds the list (refilter), and
// one rule places the cursor afterwards:
//
//  1. the row it was on, when that row is still listed;
//  2. a row of "your turn" that is done (it left the section): the next
//     to-do, else the one before it; only an emptied section lets the
//     cursor follow the item to its own section, where what the move set
//     off shows;
//  3. the same item elsewhere (it changed section or kind);
//  4. a row that is gone: its nearest neighbor in the same section, then
//     the head of that section; never whatever row now has its index;
//  5. else the head of "your turn", else the first row.
//
// Until a key places it, the cursor follows the head of "your turn"; it
// goes back there after idle_after_secs without a key when it sits
// outside the section and the section has rows. The row under the
// cursor keeps its line on the screen while rows come and go above it.

// rowKey names a listed row: a pinned copy and its home row differ.
type rowKey struct {
	kind   string
	id     model.Ident
	pinned bool
}

func keyOf(r workflow.Row) rowKey { return rowKey{r.Kind, r.Ident(), pinned(r)} }

// cursorAnchor is the cursor's place before the rows are rebuilt.
type cursorAnchor struct {
	key     rowKey
	section string   // the header above it, without its count
	after   []rowKey // the rest of its section below it, nearest first
	before  []rowKey // and above it, nearest first
	offset  int      // its line in the list's viewport
}

var headerCount = regexp.MustCompile(`\s*\(\d+\)$`)

// sectionName is a header's text without its count, so a section that
// grew is still the same section.
func sectionName(r workflow.Row) string {
	s, _ := r.Item.(string)
	return headerCount.ReplaceAllString(s, "")
}

// anchor captures the cursor's place (lock held); false when it is on
// no row.
func (m *Model) anchor() (cursorAnchor, bool) {
	if m.selected < 0 || m.selected >= len(m.rows) || m.rows[m.selected].Kind == "header" {
		return cursorAnchor{}, false
	}
	a := cursorAnchor{key: keyOf(m.rows[m.selected]), offset: m.selected - m.listTop}
	for i := m.selected - 1; i >= 0; i-- {
		r := m.rows[i]
		if r.Kind == "header" {
			a.section = sectionName(r)
			break
		}
		a.before = append(a.before, keyOf(r))
	}
	for i := m.selected + 1; i < len(m.rows) && m.rows[i].Kind != "header"; i++ {
		a.after = append(a.after, keyOf(m.rows[i]))
	}
	return a, true
}

// resolve is where the anchor lands in the rows now listed (rules 1-4);
// -1 when nothing of it is left. exact keeps to rules 1 and 3 (the
// filter: a row that stopped matching goes to the first match instead).
func (m *Model) resolve(a cursorAnchor, exact bool) int {
	at := map[rowKey]int{}
	for i, r := range m.rows {
		if r.Kind == "header" {
			continue
		}
		if _, ok := at[keyOf(r)]; !ok {
			at[keyOf(r)] = i
		}
	}
	if i, ok := at[a.key]; ok {
		return i
	}
	if a.key.pinned && !exact {
		// a to-do that is done: the next one, else the one before
		for _, k := range a.after {
			if i, ok := at[k]; ok {
				return i
			}
		}
		for _, k := range a.before {
			if i, ok := at[k]; ok {
				return i
			}
		}
		if i := m.firstTurn(); i >= 0 {
			return i // a section of new to-dos only
		}
	}
	// the same item in another section, or as another kind (PR -> queued)
	for _, sameKind := range []bool{true, false} {
		if !sameKind && a.key.id.Number == 0 {
			break // a numberless row (a branch) is only itself
		}
		for i, r := range m.rows {
			if r.Kind != "header" && (!sameKind || r.Kind == a.key.kind) && r.Ident() == a.key.id {
				return i
			}
		}
	}
	if exact {
		return -1
	}
	for _, near := range [][]rowKey{a.after, a.before} {
		for _, k := range near {
			if i, ok := at[k]; ok {
				return i
			}
		}
	}
	for i, r := range m.rows {
		if r.Kind == "header" && a.section != "" && sectionName(r) == a.section &&
			i+1 < len(m.rows) && m.rows[i+1].Kind != "header" {
			return i + 1
		}
	}
	return -1
}

// firstTurn is the head of "your turn"; -1 when it has no rows.
func (m *Model) firstTurn() int {
	for i, r := range m.rows {
		if pinned(r) {
			return i
		}
	}
	return -1
}

// idleSinceKey: no key for idle_after_secs (the timer, not the focus: a
// glance at a browser is no reason to move the cursor).
func (m *Model) idleSinceKey() bool {
	secs := m.cfg.Display.IdleAfterSecs
	return secs > 0 && fx.Now()-m.lastInput > float64(secs)
}

// wantsHome: the cursor belongs on the head of "your turn" rather than
// where the rule put it at i.
func (m *Model) wantsHome(i int) bool {
	if !m.cursorPlaced {
		return true
	}
	if !m.idleSinceKey() || m.view != ViewList || m.modal != nil || m.filter != "" || m.filtering {
		return false
	}
	return i < 0 || i >= len(m.rows) || !pinned(m.rows[i])
}

// placeCursor settles the cursor after the rows were rebuilt (lock held).
func (m *Model) placeCursor(a cursorAnchor, had bool) {
	at := -1
	if had {
		at = m.resolve(a, m.filtering)
	}
	if at < 0 || m.wantsHome(at) {
		if i := m.firstTurn(); i >= 0 {
			at = i
		}
	}
	m.selected = max(0, min(at, len(m.rows)-1))
	m.parkCursor()
	if had {
		m.holdViewport(a)
	}
}

// goHome is the idle timer's half of the rule: the frame clock checks it,
// so the cursor is back on the head before anyone looks again.
func (m *Model) goHome() {
	if !m.loaded || !m.wantsHome(m.selected) {
		return
	}
	if i := m.firstTurn(); i >= 0 && i != m.selected {
		m.selected = i
	}
}

// holdViewport keeps the cursor's row on the line it was on, so rows
// arriving or leaving above it do not slide it down the screen; a list
// scrolled to the top stays at the top while the row still shows there
// (the "your turn" header stays in view as it grows).
func (m *Model) holdViewport(a cursorAnchor) {
	if m.listVisible <= 0 || (m.listTop == 0 && m.selected < m.listVisible) {
		return
	}
	top := max(0, m.selected-a.offset)
	if d := top - m.listTop; d != 0 {
		m.listTop = top
		if g, ok := m.glide["list"]; ok {
			// a scroll easing in moves with the rows, not across them
			m.glide["list"] = glideEntry{g.from + d, g.to + d, g.start}
		}
	}
}
