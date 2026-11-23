package config

import (
	"strings"
	"testing"
)

// TestReleaseNowDispatchModes: release_now_dispatch takes true, false or
// "comment" (the strings "true" and "false" too); comment mode keeps the
// dispatch's refire and M gate on and leaves the firing to the app repo.
func TestReleaseNowDispatchModes(t *testing.T) {
	const head = minimal + "[workflows.maintenance_window]\nenabled = true\n[workflows.release_pr]\nenabled = true\n"
	for _, c := range []struct {
		value             string // "" = absent
		dispatch, comment bool
	}{
		{"", true, false},
		{"true", true, false},
		{"false", false, false},
		{`"comment"`, true, true},
		{`"Comment"`, true, true},
		{`"true"`, true, false},
		{`"false"`, false, false},
	} {
		src := head
		if c.value != "" {
			src += "release_now_dispatch = " + c.value + "\n"
		}
		cfg, err := parse(t, src)
		if err != nil {
			t.Fatalf("%s: %v", c.value, err)
		}
		r := cfg.ReleasePR
		if r.ReleaseNowDispatch != c.dispatch || r.ReleaseNowCommentOnly != c.comment {
			t.Errorf("release_now_dispatch = %s: dispatch %v comment %v, want %v %v", c.value, r.ReleaseNowDispatch, r.ReleaseNowCommentOnly, c.dispatch, c.comment)
		}
	}
	for _, bad := range []string{`"sometimes"`, "1", "[true]"} {
		_, err := parse(t, head+"release_now_dispatch = "+bad+"\n")
		if err == nil || !strings.Contains(err.Error(), "release_now_dispatch") || !strings.Contains(err.Error(), `"comment"`) {
			t.Errorf("release_now_dispatch = %s: want an error naming the choices, got %v", bad, err)
		}
	}
}

// TestTagByHand: [workflows.promote] tag_by_hand is off by default.
func TestTagByHand(t *testing.T) {
	cfg, err := parse(t, minimal+"[workflows.promote]\nenabled = true\n")
	if err != nil || cfg.Promote == nil || cfg.Promote.TagByHand {
		t.Fatalf("default: %v %+v", err, cfg.Promote)
	}
	cfg, err = parse(t, minimal+"[workflows.promote]\nenabled = true\ntag_by_hand = true\n")
	if err != nil || !cfg.Promote.TagByHand {
		t.Fatalf("tag_by_hand = true: %v %+v", err, cfg.Promote)
	}
}
