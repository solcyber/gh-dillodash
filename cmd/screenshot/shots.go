package main

import (
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ui"
)

// A shot is one picture of the gallery: a key script on the demo fixture,
// drawn at a size, in a glyph set, with or without motion. The docs embed
// them from docs/shots/<name>.png; shots_test.go keeps the two in step.
type shot struct {
	Name       string
	Cols, Rows int    // 0: 128×24
	Icons      string // "" = nerd | unicode | ascii
	Steps      []string
	Caption    string // the alt text
	Opts       ui.SnapshotOptions
}

// The fixture's list, top to bottom (the cursor starts on the first
// row): infra#44 (draft), web-portal#92, infra#41 (plan digests),
// infra#42, the releases web-portal#91 and #90 (in flight), the stage
// line, the window night with its manifest infra#20 and the queued
// infra#43, then the issues web-portal#19, infra#17, infra#18.
var shots = []shot{
	{Name: "list", Steps: []string{"j"},
		Caption: "The list: pull requests with their review chips, CI glyphs, diffstat, author and age; the releases with a release in flight; the night's queue; the issues"},
	{Name: "list-narrow", Cols: 80, Steps: []string{"j"},
		Caption: "The list at eighty columns: the diffstat and author go first, the titles stay"},
	{Name: "narrow-50x12", Cols: 50, Rows: 12, Steps: []string{"j"},
		Caption: "Fifty columns by twelve rows: number, chip and title, and a key bar down to ? help"},
	{Name: "pr-detail", Steps: []string{"j", "enter"},
		Caption: "A pull request's detail: the header, the release or plan cards, the description rendered from markdown, then comments and reviews in order"},
	{Name: "pr-files", Steps: []string{"j", "enter", "f"},
		Caption: "The changed files of a pull request, with their status and diffstat; enter opens a file's diff"},
	{Name: "pr-diff", Steps: []string{"j", "enter", "f", "enter"},
		Caption: "One file's diff in-app, hunks colored; / searches it"},
	{Name: "issue-detail", Steps: []string{"G", "enter"},
		Caption: "An issue's detail: labels, assignees, the body and the comments"},
	{Name: "help", Steps: []string{"?"},
		Caption: "The help overlay: every key of the current view by group, the workflows' keys, and the legend of chips"},
	{Name: "filter", Steps: []string{"/", "infra"},
		Caption: "The filter, live as you type: the rows narrow to the words"},
	{Name: "goto", Steps: []string{":", "91"},
		Caption: "Go to a number: #N, or repo#N when the repos share numbers"},
	{Name: "terraform-approve", Steps: []string{"j", "j", "enter"},
		Caption: "A terraform PR's detail with its plan cards: the plan id, what changes, the destroy count and the window verdict"},
	{Name: "terraform-approve-menu", Steps: []string{"j", "j", "enter", "a"},
		Caption: "a on a terraform PR: the choice between the window and an emergency, with what will be posted"},
	{Name: "approve-menu", Steps: []string{"j", "j", "j", "j", "enter", "a"},
		Caption: "a on a release: queue it for tonight's window, or release NOW with a reason"},
	{Name: "release-now-prompt", Steps: []string{"j", "j", "j", "j", "enter", "a", "2", "the referral fix is holding up signups"},
		Caption: "The release-now prompt: the reason, its length meter, and the exact comment that will be posted"},
	{Name: "release-rail", Steps: []string{"j", "j", "j", "j", "j"},
		Caption: "A release in flight on the list: its rail fills phase by phase, with the phase and its clock"},
	{Name: "release-card", Steps: []string{"j", "j", "j", "j", "j", "enter"},
		Caption: "Mission control for a release in flight: the T+ clock, the rail with Amos on it, one row per phase with its clock and its technical tail, the run"},
	{Name: "release-launch", Steps: []string{"j", "j", "j", "j", "j"},
		Opts:    ui.SnapshotOptions{Motion: true, Clock: 100, Fx: []ui.FxStart{{Name: "launch", Secs: fx.LaunchSecs, Elapsed: 2.3}}},
		Caption: "When a release lands: the rocket, the fireworks and RELEASED! across the screen"},
	{Name: "window-card", Steps: []string{"G", "k", "k", "k", "enter"},
		Caption: "A PR queued for the maintenance window: its night, its slot, what w does"},
	{Name: "fortress", Steps: []string{"d"},
		Caption: "Fortress mode: every repo as a lane of branches and pull requests, the oldest work furthest out"},
	{Name: "splash", Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 0.9, KeepSplash: true},
		Caption: "The splash while the first refresh runs: the logotype cascades in and Amos walks"},
	{Name: "amos", Steps: []string{"/", "nothing-here"}, Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 2.0},
		Caption: "Nothing to show: Amos walks the empty list under the stars"},
	{Name: "icons-nerd", Steps: []string{"j"}, Icons: "nerd",
		Caption: "The list with the bundled Nerd Font's icons"},
	{Name: "icons-unicode", Steps: []string{"j"}, Icons: "unicode",
		Caption: "The same list with icons = \"unicode\": plain Unicode symbols, no font needed"},
	{Name: "icons-ascii", Steps: []string{"j"}, Icons: "ascii",
		Caption: "The same list with icons = \"ascii\": every glyph from the seven-bit set"},
}

func (s shot) size() (int, int) {
	c, r := s.Cols, s.Rows
	if c == 0 {
		c = 128
	}
	if r == 0 {
		r = 24
	}
	return c, r
}

func (s shot) glyphSet() string {
	if s.Icons == "" {
		return "nerd"
	}
	return s.Icons
}
