// Package keymap names the keys a message may tell someone to press.
// The dashboard's binding table (ui) and the workflows' own keys bind
// these same constants, so a status line, a menu or a card can never
// name a key that has moved: "A on its row: release NOW" is written
// with Approve, not with a letter.
//
// Shift acts, lowercase looks: a capital changes something (on GitHub,
// the install or the config) and asks first.
package keymap

// The keys that act.
const (
	Approve     = 'A' // approve (a workflow may take over)
	Merge       = 'M' // merge the open, approved PR
	Deploys     = 'D' // approve the deployments waiting on a merge or a run
	ActOnRuns   = 'X' // re-run, cancel or reject a run; dispatch a listed workflow
	Comment     = 'C' // comment on the open item
	Tag         = 'T' // tag someone in a comment
	Labels      = 'L' // add or remove a label
	Assign      = 'S' // assign an issue
	CloseReopen = 'Z' // close or reopen an issue
	Handoff     = 'H' // hand the open item to the agent
	Promote     = 'P' // move a change one tier on
	Window      = 'W' // the window's commands (/attend, /release)
	Update      = 'U' // restart into the installed build, pull the newer config
)

// The keys that look.
const (
	Runs         = 'x' // the runs of the selected row, a job's log
	Refresh      = 'r' // refresh now
	ShowApproved = 's' // show / hide PRs others approved
	Room         = 'w' // step into or out of the room
)

// S is a key as text, for a message: S(Approve) is "A".
func S(k rune) string { return string(k) }
