package workflow

import (
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// TurnKind is whose move a row is. One answer per row, read by every
// part of the screen that says what to do: the "your turn" section, the
// summary, the gold chips, the key bar's offers, the hint line and the
// terminal title. A row is the viewer's turn only when nothing moves
// until they act: a release the next window will take, a deploy that
// runs, a wait on somebody else are not.
type TurnKind int

const (
	TurnNone    TurnKind = iota // a row with no move to make (a header, a stage line)
	TurnYou                     // nothing moves until the viewer acts
	TurnRunning                 // it moves on its own: watch it
	TurnOthers                  // it waits on somebody else, or on automation
	TurnDone                    // finished well
	TurnFailed                  // finished badly: look at it
)

// Turn is one row's answer.
type Turn struct {
	Kind TurnKind
	// Key is the key that moves a TurnYou row on ("D", "A", "M", "P"), or
	// "↵" for a PR that wants reading first (a review).
	Key string
	// Verb is what Key does here, as the hint says it after the key:
	// "approve prod", "approve the release", "review it".
	Verb string
	// Noun is what the row is, for the summary's counts: "deploy",
	// "release", "review", "promotion", "fix".
	Noun string
	// What is what happens, or who it waits on: "deploying 2/3", "team
	// db-admins", "automation (deploy-bot)", "the next window".
	What string
	// Since is when it started waiting on the viewer, or running.
	Since time.Time
	// ETA is when a running row should land (zero: unknown).
	ETA time.Time
	// Prio orders the "your turn" section (lower first).
	Prio int
}

// The your-turn order: what holds a deploy first, then a release, then
// a review.
const (
	PrioDeploy    = 10
	PrioMergeHand = 15
	PrioRelease   = 20
	PrioReview    = 30
	PrioPromotion = 40
	PrioOwnFix    = 50
)

// You is a TurnYou.
func You(key, verb, noun string, prio int) Turn {
	return Turn{Kind: TurnYou, Key: key, Verb: verb, Noun: noun, Prio: prio}
}

// Others is a TurnOthers waiting on who.
func Others(who string) Turn { return Turn{Kind: TurnOthers, What: who} }

// Running is a TurnRunning doing what.
func Running(what string, eta time.Time) Turn { return Turn{Kind: TurnRunning, What: what, ETA: eta} }

// TurnOf is the row's turn by its kind's Turn, TurnNone when the kind
// says nothing.
func (s *Set) TurnOf(app App, row Row) Turn {
	if s == nil {
		return Turn{}
	}
	if rk, ok := s.RowKinds()[row.Kind]; ok && rk.Turn != nil {
		return rk.Turn(app, row)
	}
	return Turn{}
}

// RunWaits is a state that knows the environments a run waits on (the
// waiting runs workflow's): another workflow's row reads it to tell a
// wait on the viewer from one on somebody else.
type RunWaits interface {
	// RunWait is the environments of the run that are the viewer's to
	// approve now; known is false when the state never read the run.
	RunWait(repo model.Repo, runID int64) (mine []string, known bool)
}

// ShownRuns is a state whose rows show (and approve with D) these
// waiting runs: the waiting runs workflow leaves them out of its own
// section, so a deploy is listed once. Only a run the row itself shows
// as waiting is named: one the row's last read saw differently stays
// with the waiting runs, so a wait is never hidden from both.
type ShownRuns interface {
	ShownRuns() []RepoRun
}

// RepoRun is a run in a repo.
type RepoRun struct {
	Repo  model.Repo
	RunID int64
}
