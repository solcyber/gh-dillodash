package ui

import (
	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// fortressState is the fortress map's state. The map itself lands in a
// later phase; this keeps the refresh and the keys wired.
type fortressState struct {
	wanted bool
	data   map[model.Repo]ghapi.FortressData
}

func (m *Model) openFortress() {
	if m.fortress == nil {
		m.fortress = &fortressState{}
	}
	m.fortress.wanted = true
	m.notice("fortress mode is not built yet in this version")
}

func (m *Model) fortressKey(k widgets.Key) bool { return false }

func (m *Model) fortressTickMS() int { return 400 }

func (m *Model) fortressURL() string { return "" }

func (m *Model) drawFortress(c *canvas.Canvas, h, w int) {
	c.PutSegs(3, 2, []text.Seg{text.S("fortress mode is not built yet", "faint")}, 0, false, false)
}
