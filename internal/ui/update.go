package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
	"github.com/solcyber/gh-dillodash/internal/widgets"
)

// Updater is the build check behind the stale banner and the release
// actions' gate, and the background install behind R.
type Updater interface {
	Check(ctx context.Context, force bool) selfupdate.Status
	Install(ctx context.Context, st selfupdate.Status) selfupdate.Result
}

// buildCheckEverySecs is how often a running dashboard asks again: a
// conditional request, free when nothing changed.
const buildCheckEverySecs = 30 * 60

// buildCheckMsg carries a finished build check back to the UI loop.
type buildCheckMsg struct{ st selfupdate.Status }

// buildInstallMsg carries a finished background install.
type buildInstallMsg struct{ res selfupdate.Result }

// checkBuild starts a build check in the background (lock held). It is
// quiet: an offline check leaves the last answer, and nothing waits on
// it.
func (m *Model) checkBuild(force bool) {
	m.nextBuildCheckAt = fx.Now() + buildCheckEverySecs
	if m.updater == nil || m.checking {
		return
	}
	m.checking = true
	ctx, u, last := m.ctx, m.updater, m.upd
	go func() {
		st := last
		defer func() {
			_ = recover() // a broken check never takes the dashboard down
			m.send(buildCheckMsg{st})
		}()
		st = u.Check(ctx, force)
	}()
}

// applyBuildCheck takes a check's answer and, when auto-update is on
// and a newer release is out, installs it in the background, once per
// release (lock held).
func (m *Model) applyBuildCheck(st selfupdate.Status) {
	m.checking = false
	m.upd = st.Unchanged(m.upd)
	if !m.cfg.Update.Auto || !m.upd.Stale() || m.upd.Pending() || m.installing || m.installTried == m.upd.Latest {
		return
	}
	m.installing, m.installTried = true, m.upd.Latest
	ctx, u, cur := m.ctx, m.updater, m.upd
	go func() {
		res := selfupdate.Result{Version: cur.Latest, Err: fmt.Errorf("the install stopped")}
		defer func() {
			_ = recover()
			m.send(buildInstallMsg{res})
		}()
		res = u.Install(ctx, cur)
	}()
}

// applyBuildInstall reports a background install (lock held): installed
// waits for R; a refusal is an error that stays up, with the notes
// behind e.
func (m *Model) applyBuildInstall(res selfupdate.Result) {
	m.installing = false
	notes := strings.Join(res.Notes, "; ")
	m.installErr = ""
	switch {
	case res.Err != nil:
		m.installErr = fmt.Sprintf("dillodash %s NOT installed: %s", res.Version, res.Err)
		m.fail(m.installErr)
		if errors.Is(res.Err, selfupdate.ErrDownload) {
			m.installTried = "" // a dropped download is retried at the next check; a refusal is not
		}
		if notes != "" {
			m.lastMsg += " — " + notes
		}
	case len(res.Updated) > 0 && res.NotSelf:
		// R would exec the same old binary: say where it landed instead
		m.notice(fmt.Sprintf("installed dillodash %s at %s — this dashboard runs another copy, which was not replaced; quit and start the new one", res.Version, strings.Join(res.Updated, ", ")))
		if notes != "" {
			m.lastMsg += " — " + notes
		}
	case len(res.Updated) > 0:
		m.upd.Installed = res.Version
		m.ok(fmt.Sprintf("installed dillodash %s (%s) — U restarts into it", res.Version, strings.Join(res.Updated, ", ")))
		if notes != "" {
			m.lastMsg += " — " + notes
		}
	}
}

// Restore is where a restart left off: the view, the selected row, the
// open detail and the list's filter. It travels to the new process in
// DILLODASH_RESTORE.
type Restore struct {
	Detail   *RestoreRef `json:"detail,omitempty"`
	Selected *RestoreRef `json:"selected,omitempty"`
	Filter   string      `json:"filter,omitempty"`
	ShowAll  bool        `json:"show_all,omitempty"`
}

// RestoreRef names an item by identity, not by row index: the list a
// restart loads may be ordered differently.
type RestoreRef struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Kind   string `json:"kind,omitempty"` // a detail: "pr" or "issue"
}

// RestoreEnv is the environment variable that carries a Restore.
const RestoreEnv = "DILLODASH_RESTORE"

// Encode is the Restore as one env-safe word.
func (r Restore) Encode() string {
	b, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeRestore reads Encode's word; nil for anything else.
func DecodeRestore(s string) *Restore {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	var r Restore
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// RestartError is what Run returns for R: the dashboard has quit and
// restored the terminal; the caller runs the new binary with Restore.
type RestartError struct{ Restore Restore }

func (e *RestartError) Error() string { return "restart requested" }

// captureRestore records where the dashboard is (lock held).
func (m *Model) captureRestore() Restore {
	r := Restore{Filter: m.filter, ShowAll: m.showAll}
	if row, ok := m.Selected(); ok {
		id := row.Ident()
		r.Selected = &RestoreRef{Repo: id.Repo.String(), Number: id.Number}
	}
	if m.view != ViewList && m.view != ViewFortress {
		switch d := m.detail.(type) {
		case *model.PR:
			r.Detail = &RestoreRef{Repo: m.repoOf(d).String(), Number: d.Number, Kind: "pr"}
		case *model.Issue:
			r.Detail = &RestoreRef{Repo: m.repoOf(d).String(), Number: d.Number, Kind: "issue"}
		}
	}
	return r
}

// applyRestore puts the dashboard back where a restart left it, after
// the first refresh (lock held).
func (m *Model) applyRestore() {
	r := m.restore
	m.restore = nil
	if r == nil {
		return
	}
	if r.Filter != "" || r.ShowAll {
		m.filter, m.showAll = r.Filter, r.ShowAll
		m.refilter()
	}
	if r.Selected != nil {
		if repo, ok := model.ParseRepo(r.Selected.Repo); ok {
			want := model.Ident{Repo: repo, Number: r.Selected.Number}
			for i, row := range m.rows {
				if row.Kind != "header" && row.Ident() == want {
					m.selected = i
					break
				}
			}
		}
	}
	if d := r.Detail; d != nil {
		repo, ok := model.ParseRepo(d.Repo)
		if !ok {
			return
		}
		var listed *model.PR
		for _, row := range m.rows {
			if p, ok := row.Item.(*model.PR); ok && m.repoOf(p) == repo && p.Number == d.Number {
				listed = p
				break
			}
		}
		kind := d.Kind
		if kind != "issue" {
			kind = "pr"
		}
		app := &lockedApp{m}
		m.detailFrom = ViewList
		m.runAction(func() { m.openDetail(app, repo, d.Number, kind, listed) })
	}
}

// doRestart is R: restart into the release installed in the background,
// keeping the place. A confirm or a post in flight is never cut short:
// the restart waits for it.
func doRestart(m *Model, _ widgets.Key, _ int, _ *lockedApp) tea.Cmd {
	if !m.upd.Pending() {
		m.notice("no newer dillodash is installed — U restarts into one once it is")
		return nil
	}
	if m.modal != nil || m.action {
		m.restartQueued = true
		m.notice("the restart waits for the action in flight; it runs once that settles")
		return nil
	}
	return m.restartNow()
}

// restartNow quits the dashboard with a restart request (lock held).
func (m *Model) restartNow() tea.Cmd {
	m.restartQueued = false
	r := m.captureRestore()
	m.restartTo = &r
	return tea.Quit
}

// persistentLine is the status line's standing message when nothing
// newer is up: an install that refused (with the command that upgrades
// by hand), the stale or installed banner, and the refresh's warning.
func (m *Model) persistentLine() (string, bool) {
	lead := m.upd.Banner()
	if m.installErr != "" && !m.upd.Pending() {
		lead = m.installErr + "; " + m.upd.Hint()
	}
	return joinNonEmpty("; ", lead, m.configBanner(), m.warn), m.installErr != "" && !m.upd.Pending()
}

// pendingRestart reports whether R waits in the bar.
func (m *Model) pendingRestart() bool { return m.upd.Pending() }

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
