package ui

import (
	"fmt"
	"os"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// A config pulled from a repo (`config pull`) carries its source and
// blob sha in a header. The dashboard compares that sha with the repo's
// now and then: when the repo has a newer file, the status line says so
// and P pulls it and restarts into it, keeping the place. An approver
// running yesterday's window times is how a night goes wrong quietly.

// configCheckEverySecs is how often the source is compared.
const configCheckEverySecs = 3600

type configCheckMsg struct {
	sha string
	err error
}

// configStale gates P in the bar.
func (m *Model) configStale() bool { return m.cfgNewer != "" }

// checkConfig compares the installed config with its source in the
// background (lock held).
func (m *Model) checkConfig() {
	m.nextCfgCheckAt = fx.Now() + configCheckEverySecs
	if m.cfg == nil || m.cfg.Path == "" || m.cfgChecking {
		return
	}
	raw, err := os.ReadFile(m.cfg.Path)
	if err != nil {
		return
	}
	src, pulled := config.ParseSource(raw)
	if !pulled {
		return
	}
	m.cfgSource, m.cfgChecking = src, true
	ctx, gh := m.ctx, m.gh
	go func() {
		msg := configCheckMsg{}
		defer func() {
			_ = recover()
			m.send(msg)
		}()
		_, msg.sha, msg.err = gh.FileContents(ctx, src.Repo, src.Path, "")
	}()
}

// applyConfigCheck takes the answer (lock held): a failed read changes
// nothing; a different sha is a newer config.
func (m *Model) applyConfigCheck(msg configCheckMsg) {
	m.cfgChecking = false
	if msg.err != nil || msg.sha == "" {
		return
	}
	if msg.sha != m.cfgSource.SHA {
		m.cfgNewer = msg.sha
	} else {
		m.cfgNewer = ""
	}
}

// configBanner is the status line's standing note while the config is
// behind its source.
func (m *Model) configBanner() string {
	if m.cfgNewer == "" {
		return ""
	}
	return fmt.Sprintf("config outdated: %s has a newer %s (P pulls it and restarts)", m.cfgSource.Repo, m.cfgSource.Path)
}

// doConfigPull is the P key: fetch the source again, refuse one that
// does not validate, install it stamped, and restart into it.
func (m *Model) doConfigPull(app *lockedApp) {
	m.mu.Lock()
	src, newer, path := m.cfgSource, m.cfgNewer, ""
	if m.cfg != nil {
		path = m.cfg.Path
	}
	m.mu.Unlock()
	if newer == "" || path == "" {
		app.Notice("the config is current (or was not pulled from a repo): nothing to pull")
		return
	}
	if !app.Confirm(fmt.Sprintf("pull %s @ %s into %s and restart into it? (keeps your place)", src.Spec(), model.ShortSHA(newer), path)) {
		app.Notice("cancelled — gh dillodash config pull does it by hand")
		return
	}
	app.Flash(fmt.Sprintf("reading %s...", src.Spec()))
	data, sha, err := m.gh.FileContents(m.ctx, src.Repo, src.Path, "")
	if err != nil {
		app.Fail(fmt.Sprintf("could not read %s: %s", src.Spec(), err))
		return
	}
	data = config.StripHeader(data)
	if _, err := config.Parse(data, src.Spec(), config.Overrides{}); err != nil {
		app.Fail(fmt.Sprintf("not installed: %s does not validate: %s", src.Spec(), err))
		return
	}
	src.SHA, src.At = sha, app.Now()
	if err := config.WriteFileAtomic(path, config.Stamp(src, data), 0o600); err != nil {
		app.Fail("could not write the config: " + err.Error())
		return
	}
	m.mu.Lock()
	m.cfgSource, m.cfgNewer = src, ""
	m.mu.Unlock()
	app.OK(fmt.Sprintf("installed %s @ %s — restarting into it", src.Spec(), model.ShortSHA(sha)))
	app.RestartAfterAction()
}
