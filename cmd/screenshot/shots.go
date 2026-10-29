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
	Expect     []string // text the frame must show: the script landed where it meant to
	Opts       ui.SnapshotOptions
}

// The fixture's list, top to bottom (the cursor starts on the first
// row): infra#44 (draft), web-portal#92, infra#41 (plan digests),
// infra#42, the waiting runs web-portal 7102 (waits on the dba team) and
// infra 7101 (waits on you), the releases web-portal#91 and #90 (in
// flight), the stage line, the window night with its manifest infra#20
// and the queued infra#43, then the issues web-portal#19, infra#17,
// infra#18. Expect pins each script to what it means to show, so a new
// row in the fixture fails the test instead of the picture.
var shots = []shot{
	{Name: "list", Steps: []string{"j"}},
	{Name: "list-narrow", Cols: 80, Steps: []string{"j"}},
	{Name: "narrow-50x12", Cols: 50, Rows: 12, Steps: []string{"j"}},
	{Name: "pr-detail", Steps: []string{"j", "enter"}, Expect: []string{"web-portal#92", "description"}},
	{Name: "pr-files", Steps: []string{"j", "enter", "f"}},
	{Name: "pr-diff", Steps: []string{"j", "enter", "f", "enter"}},
	{Name: "issue-detail", Steps: []string{"G", "enter"}, Expect: []string{"infra#18"}},
	{Name: "help", Steps: []string{"?"}},
	{Name: "filter", Steps: []string{"/", "infra"}},
	{Name: "goto", Steps: []string{":", "91"}},
	{Name: "terraform-approve", Steps: []string{"j", "j", "enter"}, Expect: []string{"infra#41", "plans"}},
	{Name: "terraform-approve-menu", Steps: []string{"j", "j", "enter", "a"}, Expect: []string{"for which plan"}},
	{Name: "approve-menu", Steps: []string{"j", "j", "j", "j", "j", "j", "enter", "a"}, Expect: []string{"web-portal#91", "release NOW"}},
	{Name: "release-now-prompt", Steps: []string{"j", "j", "j", "j", "j", "j", "enter", "a", "2", "the referral fix is holding up signups"}, Expect: []string{"web-portal#91", "/release-now"}},
	{Name: "release-rail", Steps: []string{"j", "j", "j", "j", "j", "j", "j"}, Expect: []string{"deploying 2/3"}},
	{Name: "release-card", Steps: []string{"j", "j", "j", "j", "j", "j", "j", "enter"}, Expect: []string{"web-portal#90", "T+"}},
	{Name: "release-launch", Steps: []string{"j", "j", "j", "j", "j", "j", "j"},
		Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Fx: []ui.FxStart{{Name: "launch", Secs: fx.LaunchSecs, Elapsed: 2.3}}}},
	{Name: "waiting-runs", Steps: []string{"j", "j", "j", "j", "j"}, Expect: []string{"waiting approvals", "approve: prod"}},
	{Name: "waiting-runs-approve", Steps: []string{"j", "j", "j", "j", "j", "D"}, Expect: []string{"approve 1 deployment of infra deploy run 7101"}},
	{Name: "window-card", Steps: []string{"G", "k", "k", "k", "enter"}, Expect: []string{"infra#43", "nonprod/15-window-canary"}},
	{Name: "fortress", Steps: []string{"d"}, Expect: []string{"Manhattan"}},
	{Name: "fortress-chicago", Steps: []string{"d"}, Opts: ui.SnapshotOptions{City: "chicago"}, Expect: []string{"Chicago"}},
	{Name: "splash", Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 0.9, KeepSplash: true}},
	{Name: "amos", Steps: []string{"/", "nothing-here"}, Opts: ui.SnapshotOptions{Motion: true, Clock: 100, Advance: 2.0}},
	{Name: "icons-nerd", Steps: []string{"j"}, Icons: "nerd"},
	{Name: "icons-unicode", Steps: []string{"j"}, Icons: "unicode"},
	{Name: "icons-ascii", Steps: []string{"j"}, Icons: "ascii"},
}

// heroScripts are the README's picture: the list with the cursor on the
// second row, then (the keys continue from there) the release
// web-portal#91's detail. heroExpect is what the second frame must show.
var (
	heroScripts = [][]string{
		{"j"},
		{"j", "j", "j", "j", "j", "enter"},
	}
	heroExpect = "web-portal#91"
)

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
