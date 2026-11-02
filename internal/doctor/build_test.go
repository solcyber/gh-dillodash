package doctor

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
)

func TestBuildReport(t *testing.T) {
	at := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	old := clock.Now
	clock.Now = func() time.Time { return at }
	t.Cleanup(func() { clock.Now = old })
	ext := selfupdate.Install{Kind: selfupdate.KindExtension, Path: "/home/alice/.local/share/gh/extensions/gh-dillodash/gh-dillodash", Version: "v1.5.2", Running: true}
	installs := []selfupdate.Install{
		ext,
		{Kind: selfupdate.KindLauncher, Path: "/home/alice/.local/bin/dillodash", Runs: "gh dillodash"},
		{Kind: selfupdate.KindBinary, Path: "/opt/bin/dillodash", Version: "v1.4.1"},
	}
	var out bytes.Buffer
	Build(&out, selfupdate.Status{Running: "v1.5.2", Latest: "v1.5.4", CheckedAt: at.Add(-3 * time.Minute), Self: ext}, installs)
	s := out.String()
	for _, want := range []string{
		"version: STALE — v1.5.4 is out (checked 3m ago); gh extension upgrade dillodash",
		"installs:",
		"gh extension  /home/alice/.local/share/gh/extensions/gh-dillodash/gh-dillodash  v1.5.2  (running)",
		"launcher      /home/alice/.local/bin/dillodash  runs gh dillodash",
		"binary        /opt/bin/dillodash  v1.4.1  — STALE: /opt/bin/dillodash update",
		"note: installs of different versions (v1.5.2, v1.4.1)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	cases := []struct {
		st   selfupdate.Status
		want string
	}{
		{selfupdate.Status{Running: "v1.5.4", Latest: "v1.5.4", CheckedAt: at}, "version: current (latest v1.5.4, checked"},
		{selfupdate.Status{Running: "v1.5.4", Err: "network error: timed out"}, "version: latest release unknown (the check failed: network error: timed out)"},
		{selfupdate.Status{Running: "dev+abc1234"}, "version: dev+abc1234 is not a release build"},
	}
	for _, tc := range cases {
		out.Reset()
		Build(&out, tc.st, nil)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%+v: %q", tc.st, out.String())
		}
	}
}

func TestAutoUpdateLine(t *testing.T) {
	for _, tc := range []struct {
		auto bool
		env  string
		want string
	}{
		{true, "", "auto-update: on"},
		{false, "0", "auto-update: off (DILLODASH_AUTO_UPDATE=0)"},
		{false, "", "auto-update: off ([update] auto = false)"},
	} {
		var out strings.Builder
		AutoUpdate(&out, tc.auto, tc.env)
		if !strings.HasPrefix(out.String(), tc.want) {
			t.Errorf("auto=%v env=%q: %q", tc.auto, tc.env, out.String())
		}
	}
}
