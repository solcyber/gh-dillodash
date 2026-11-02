package selfupdate

import (
	"fmt"
	"time"
)

// Status is what the check knows: the running build, the latest
// release, when that was last read, and how the running install is
// upgraded.
type Status struct {
	Running   string
	Latest    string // "" when no check has ever answered
	Notes     string // the latest release's notes
	URL       string // the latest release's page
	CheckedAt time.Time
	Err       string // the last check's failure ("" when it answered)
	Self      Install
	// Installed is a newer release installed by this session, which a
	// restart runs; "" when none.
	Installed string
}

// Pending reports whether a newer release is installed and waits for a
// restart.
func (s Status) Pending() bool { return Newer(s.Installed, s.Running) }

// Stale reports whether a newer release than the running build is out.
func (s Status) Stale() bool { return Newer(s.Latest, s.Running) }

// Hint is the one command that upgrades the running install.
func (s Status) Hint() string { return s.Self.UpgradeHint(s.Latest) }

// Unchanged copies what this session knows of itself (the install
// pending a restart) onto a fresh check, which knows only the release.
func (s Status) Unchanged(prev Status) Status {
	if s.Installed == "" {
		s.Installed = prev.Installed
	}
	return s
}

// Banner is the stale build's line on every screen ("" when current).
func (s Status) Banner() string {
	if s.Pending() {
		return fmt.Sprintf("dillodash %s installed — R: restart now (keeps your place)", s.Installed)
	}
	if !s.Stale() {
		return ""
	}
	return fmt.Sprintf("dillodash %s — %s is out: %s", s.Running, s.Latest, s.Hint())
}

// Need is the release that a release action's current behaviour landed
// in, and how a person would name the action.
type Need struct {
	Min  string
	What string
}

// Needs maps the release actions (the keys a, M, D and w) to the
// release that last fixed them. A build older than an entry lacks that
// fix, and the action says which version it needs instead of falling
// back to a generic answer that hides the gap.
var Needs = map[string]Need{
	"approve": {"v1.5.3", "release NOW on an approved release"},
	"merge":   {"v1.5.3", "merging a release by hand"},
	"deploy":  {"v1.5.4", "a release deploy held at an environment"},
	"window":  {"v1.5.4", "w on a release"},
}

// Gate is what a release action says before it runs on this build, or
// "" when the build is current. An action whose fix this build lacks
// names the version it needs; any other action on a stale build names
// the gap, since the newer release may carry release fixes.
func (s Status) Gate(action string) string {
	if s.Pending() {
		return fmt.Sprintf("dillodash %s is installed and this session still runs %s — R restarts into it (keeps your place)", s.Installed, s.Running)
	}
	if n, ok := Needs[action]; ok && Comparable(s.Running) && Compare(s.Running, n.Min) < 0 {
		return fmt.Sprintf("%s needs ≥ %s — you run %s: %s", n.What, n.Min, s.Running, s.Self.UpgradeHint(n.Min))
	}
	if s.Stale() {
		return fmt.Sprintf("you run dillodash %s and %s is out — it may carry release fixes this build lacks: %s", s.Running, s.Latest, s.Hint())
	}
	return ""
}
