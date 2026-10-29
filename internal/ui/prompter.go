package ui

import (
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/external"
	"github.com/solcyber/gh-dillodash/internal/widgets"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

// The action goroutine asks the UI loop for things through messages and
// waits on channels: a modal (confirm / prompt / pick), a redraw, or
// running an external program with the terminal released.

type showModalMsg struct {
	modal widgets.Modal
	done  chan struct{}
}

type redrawMsg struct{}

type execMsg struct {
	cmd  *exec.Cmd
	done chan error
}

type execDoneMsg struct {
	done chan error
	err  error
}

type actionDoneMsg struct{}

// runAction starts fn on the action goroutine; one at a time.
func (m *Model) runAction(fn func()) {
	if m.action {
		return
	}
	m.action = true
	go func() {
		defer m.send(actionDoneMsg{})
		fn()
	}()
}

// show blocks the action goroutine until the modal finishes.
func (m *Model) show(modal widgets.Modal) {
	done := make(chan struct{})
	if m.prog == nil && m.sendFn == nil {
		// no program and no test harness: nobody can answer
		close(done)
		return
	}
	m.send(showModalMsg{modal, done})
	<-done
}

// Confirm asks a y/N question (blocks the action goroutine).
func (m *Model) Confirm(question string) bool {
	if m.tp != nil && m.tp.Confirm != nil {
		return m.tp.Confirm(question)
	}
	c := widgets.NewConfirm(question)
	m.show(c)
	return c.Result()
}

// Prompt reads a line; ok is false on Esc. OnChange runs on the UI
// goroutine with the lock held (a live filter re-filters the rows).
func (m *Model) Prompt(prompt string, opts workflow.PromptOpts) (string, bool) {
	if m.tp != nil && m.tp.Prompt != nil {
		return m.tp.Prompt(prompt, opts)
	}
	p := widgets.NewPrompt(prompt, opts.Initial)
	p.OnChange = opts.OnChange
	p.MinLen = opts.MinLen
	p.Forbid = opts.Forbid
	p.Hazard = opts.MinLen > 0
	m.show(p)
	return p.Result()
}

// Pick shows a menu; -1 on Esc.
func (m *Model) Pick(title string, labels []string) int {
	if m.tp != nil && m.tp.Pick != nil {
		return m.tp.Pick(title, labels)
	}
	p := widgets.NewPick(title, labels, "menu")
	m.show(p)
	return p.Result()
}

// redraw asks for a frame now (ahead of a blocking fetch).
func (m *Model) redraw() { m.send(redrawMsg{}) }

// send delivers a message to the UI loop: the program, or a test's hook.
func (m *Model) send(msg tea.Msg) {
	if m.sendFn != nil {
		m.sendFn(msg)
		return
	}
	if m.prog != nil {
		m.prog.Send(msg)
	}
}

// RunExternal runs a program on the terminal, the TUI suspended, and
// returns its error (blocks the action goroutine).
func (m *Model) RunExternal(argv []string) error {
	if len(argv) == 0 {
		return exec.ErrNotFound
	}
	if m.tp != nil && m.tp.RunExternal != nil {
		return m.tp.RunExternal(argv)
	}
	done := make(chan error, 1)
	cmd := exec.Command(argv[0], argv[1:]...)
	if m.prog == nil && m.sendFn == nil {
		return cmd.Run()
	}
	m.send(execMsg{cmd, done})
	return <-done
}

// handleExec returns the Bubble Tea command that runs the process.
func (m *Model) handleExec(msg execMsg) tea.Cmd {
	return tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
		return execDoneMsg{msg.done, err}
	})
}

// openURL starts a browser on url and reports whether one started.
// Tests script it; nothing in the ui tests may reach the real browser.
func (m *Model) openURL(url string) bool {
	if m.tp != nil && m.tp.OpenURL != nil {
		return m.tp.OpenURL(url)
	}
	return external.OpenURL(url)
}

// copy puts s on the clipboard and returns where it went ("" when nowhere).
func (m *Model) copy(s string) string {
	if m.tp != nil && m.tp.Copy != nil {
		return m.tp.Copy(s)
	}
	return external.CopyToClipboard(s)
}

// testPrompter scripts the answers in tests, and stands in for the
// programs a key would otherwise start on the developer's machine (the
// browser, the clipboard, $PAGER): a test harness fills every field.
type testPrompter struct {
	Confirm     func(q string) bool
	Prompt      func(p string, o workflow.PromptOpts) (string, bool)
	Pick        func(title string, labels []string) int
	RunExternal func(argv []string) error
	OpenURL     func(url string) bool
	Copy        func(s string) (where string)
}
