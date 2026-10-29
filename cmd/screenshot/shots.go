package main

import (
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ui"
)

// A shot is one picture of the gallery: a key script on the demo fixture,
// drawn at a size, in a glyph set, with or without motion. The docs embed
// them from docs/shots/<name>.png with ui.ShotCaptions as the alt text;
// shots_test.go keeps the three in step.
type shot struct {
	Name       string
	Cols, Rows int    // 0: 128×24
	Icons      string // "" = nerd | unicode | ascii
	Steps      []string
	Opts       ui.SnapshotOptions
}

// The fixture's list, top to bottom (the cursor starts on the first
// row): infra#44 (draft), web-portal#92, infra#41 (plan digests),
// infra#42, the releases web-portal#91 and #90 (in flight), the stage
// line, the window night with its manifest infra#20 and the queued
// infra#43, then the issues web-portal#19, infra#17, infra#18.
var shots = []shot{
	{Name: "list", Steps: []string{"j"}},
	{Name: "list-narrow", Cols: 80, Steps: []string{"j"}},
	{Name: "narrow-50x12", Cols: 50, Rows: 12, Steps: []string{"j"}},
	{Name: "pr-detail", Steps: []string{"j", "enter"}},
	{Name: "pr-files", Steps: []string{"j", "enter", "f"}},
	{Name: "pr-diff", Steps: []string{"j", "enter", "f", "enter"}},
	{Name: "issue-detail", Steps: []string{"G", "enter"}},
	{Name: "help", Steps: []string{"?"}},
	{Name: "filter", Steps: []string{"/", "infra"}},
	{Name: "goto", Steps: []string{":", "91"}},
	{Name: "terraform-approve", Steps: []string{"j", "j", "enter"}},
	{Name: "terraform-approve-menu", Steps: []string{"j", "j", "enter", "a"}},
	{Name: "approve-menu", Steps: []string{"j", "j", "j", "j", "enter", "a"}},
	{Name: "release-now-prompt", Steps: []string{"j", "j", "j", "j", "enter", "a", "2", "the referral fix is holding up signups"}},
	{Name: "release-rail", Steps: []string{"j", "j", "j", "j", "j"}},
	{Name: "release-card", Steps: []string{"j", "j", "j", "j", "j", "enter"}},
	{Name: "release-launch", Steps: []string{"j", "j", "j", "j", "j"},
		Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Fx: []ui.FxStart{{Name: "launch", Secs: fx.LaunchSecs, Elapsed: 2.3}}}},
	{Name: "window-card", Steps: []string{"G", "k", "k", "k", "enter"}},
	{Name: "fortress", Steps: []string{"d"}},
	{Name: "splash", Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 0.9, KeepSplash: true}},
	{Name: "amos", Steps: []string{"/", "nothing-here"}, Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 2.0}},
	{Name: "icons-nerd", Steps: []string{"j"}, Icons: "nerd"},
	{Name: "icons-unicode", Steps: []string{"j"}, Icons: "unicode"},
	{Name: "icons-ascii", Steps: []string{"j"}, Icons: "ascii"},
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
