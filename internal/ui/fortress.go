package ui

import (
	"fmt"
	"math/rand/v2"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fortress"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// fortressState is the fortress map's state: whether the refresh should
// fetch the branches/commits layer (once d was pressed), that layer per
// repo, and the view itself (the city, the cursor, the log).
type fortressState struct {
	wanted bool
	data   map[model.Repo]ghapi.FortressData
	view   *fortress.View
	// coin picks the city; nil flips a coin (tests pin it).
	coin func() string
}

// openFortress is the d key: a coin flip between the two cities, the look
// cursor on the thing that most wants a look.
func (m *Model) openFortress() { m.openFortressCity("") }

func (m *Model) openFortressCity(city string) {
	if m.fortress == nil {
		m.fortress = &fortressState{}
	}
	f := m.fortress
	if city == "" {
		if f.coin != nil {
			city = f.coin()
		} else {
			city = fortress.CityKeys[rand.IntN(len(fortress.CityKeys))]
		}
	}
	if f.view == nil {
		f.view = fortress.NewView(city)
		f.view.Build = m.fortressBuild
	}
	f.view.Open(city)
	m.view = ViewFortress
	if !f.wanted {
		// the branches and commits come with the refresh this starts
		f.wanted = true
		f.view.Announce("Scouts sent out for branches and commits")
		m.refresh(true)
	}
}

// fortressBuild is what the map draws: the core's PRs, issues, branches
// and commits, plus whatever the workflows place (queued PRs in the
// barracks, releases at the depot, stage wagons), as *fortress.Item.
func (m *Model) fortressBuild() ([]*fortress.Item, []fortress.Lane) {
	var extra []*fortress.Item
	m.wf.FortressItems(m, func(item any) {
		switch v := item.(type) {
		case *fortress.Item:
			if v != nil {
				extra = append(extra, v)
			}
		case fortress.Item:
			extra = append(extra, &v)
		}
	})
	in := fortress.Input{
		Repos: m.repos, Me: m.me, Host: m.host, Now: clock.Now(),
		PRs: m.allPRs, Issues: m.issues, Data: m.fortress.data, Extra: extra,
		PRState: func(pr *model.PR) fortress.PRState {
			label, _, needsYou, _ := m.decision(pr, nil)
			ci, _ := m.ciOf(pr)
			return fortress.PRState{Label: label, NeedsYou: needsYou, CI: ci}
		},
		SkipIssue: func(i *model.Issue) bool {
			return m.wf.Claims(workflow.Row{Kind: "issue", Item: i})
		},
	}
	items := fortress.Items(in)
	lanes := make([]fortress.Lane, 0, len(m.repos))
	for _, r := range m.repos {
		lanes = append(lanes, fortress.Lane{Repo: r, Label: m.wf.FortressLaneLabel(r)})
	}
	return items, lanes
}

// fortressPrepare brings the view up to date with the last refresh.
func (m *Model) fortressPrepare() *fortress.View {
	v := m.fortress.view
	gen := fmt.Sprintf("%s|%d|%d", m.lastRefreshWall, len(m.allPRs), len(m.issues))
	v.Prepare(m.h, m.w, gen, m.fortress.data != nil)
	return v
}

// fortressKey handles a key in fortress mode; false leaves for the list.
func (m *Model) fortressKey(k widgets.Key) bool {
	if m.fortress == nil || m.fortress.view == nil {
		return false
	}
	v := m.fortressPrepare()
	switch {
	case k.Rune == 'q' || k.Rune == 'd' || isEsc(k):
		return false
	case k.Rune == 'h' || k.Name == "left":
		v.Move(0, -1)
	case k.Rune == 'l' || k.Name == "right":
		v.Move(0, 1)
	case k.Rune == 'k' || k.Name == "up":
		v.Move(-1, 0)
	case k.Rune == 'j' || k.Name == "down":
		v.Move(1, 0)
	case k.Rune == 'H':
		v.Move(0, -8)
	case k.Rune == 'L':
		v.Move(0, 8)
	case k.Rune == 'K':
		v.Move(-8, 0)
	case k.Rune == 'J':
		v.Move(8, 0)
	case k.Name == "tab":
		v.Next(+1)
	case k.Name == "shift+tab" || k.Name == "backtab":
		v.Next(-1)
	case k.Rune == '+' || k.Rune == '=':
		v.SetZoom(fortress.ZoomStreet)
	case k.Rune == '-':
		v.SetZoom(fortress.ZoomFit)
	case k.Rune == 'c':
		m.openFortressCity(fortress.OtherCity(v.City))
	case isEnter(k):
		under := v.Under()
		switch {
		case len(under) > 0 && under[0].Open != nil:
			op := *under[0].Open
			m.detailFrom = ViewFortress
			app := &lockedApp{m}
			m.runAction(func() { m.openDetail(app, op.Repo, op.Number, op.Kind, nil) })
		case len(under) > 0:
			m.notice("nothing to open here: o opens it on GitHub")
		default:
			m.notice("nothing here: tab jumps to the next thing")
		}
	case k.Rune == 'y':
		m.copyURL()
	case k.Rune == 'o':
		m.openInBrowser()
	case k.Rune == 'r':
		m.manualRefresh()
	}
	return true
}

// fortressTickMS: fortress mode animates on its own clock: 4 fps, 1 fps
// once nobody has pressed a key for AmbientIdleSecs (a wall dashboard
// stays alive, cheaply), and not at all with --motion off.
func (m *Model) fortressTickMS() int {
	base := 400
	switch {
	case m.fx.Active():
		base = fx.FrameMS
	case m.animating:
		base = TickMS
	case m.ambient:
		base = fx.AmbientMS
	}
	if fx.Motion != "full" {
		return base
	}
	if fx.Now()-m.lastKey >= fx.AmbientIdleSecs {
		return min(base, 1000)
	}
	return min(base, 250)
}

// fortressURL is the URL of the thing under the look cursor.
func (m *Model) fortressURL() string {
	if m.fortress == nil || m.fortress.view == nil {
		return ""
	}
	if under := m.fortress.view.Under(); len(under) > 0 {
		return under[0].URL
	}
	return ""
}

// drawFortress paints the map with the last refresh's data.
func (m *Model) drawFortress(c *canvas.Canvas, h, w int) {
	if m.fortress == nil || m.fortress.view == nil {
		return
	}
	v := m.fortressPrepare()
	still := fx.Motion != "full" || theme.Current() == theme.Mono
	v.Draw(c, h, w, fortress.Frame{
		Still:  still,
		T:      fx.Now(),
		Tier:   theme.Current(),
		Season: fortress.Season(clock.Now().Local()),
	})
}
