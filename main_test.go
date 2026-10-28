package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/config"
)

func TestResolveVersion(t *testing.T) {
	bi := func(mv string, settings ...[2]string) *debug.BuildInfo {
		b := &debug.BuildInfo{}
		b.Main.Version = mv
		for _, s := range settings {
			b.Settings = append(b.Settings, debug.BuildSetting{Key: s[0], Value: s[1]})
		}
		return b
	}
	rev := "0123456789abcdef"
	for _, tc := range []struct {
		name string
		v    string
		bi   *debug.BuildInfo
		ok   bool
		want string
	}{
		{"injected wins", "v1.2.3", bi("(devel)", [2]string{"vcs.revision", rev}), true, "v1.2.3"},
		{"no build info", "dev", nil, false, "dev"},
		{"module version", "dev", bi("v0.9.0"), true, "v0.9.0"},
		{"devel with commit", "dev", bi("(devel)", [2]string{"vcs.revision", rev}, [2]string{"vcs.modified", "false"}), true, "dev+0123456"},
		{"dirty tree", "dev", bi("(devel)", [2]string{"vcs.revision", rev}, [2]string{"vcs.modified", "true"}), true, "dev+0123456-dirty"},
		{"short revision", "dev", bi("", [2]string{"vcs.revision", "abc"}), true, "dev+abc"},
		{"no vcs at all", "dev", bi("(devel)"), true, "dev"},
	} {
		if got := resolveVersion(tc.v, tc.bi, tc.ok); got != tc.want {
			t.Errorf("%s: resolveVersion = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseFlags(t *testing.T) {
	f, err := parseFlags([]string{"--demo", "--mono", "--refresh", "0", "--repo", "a/b", "--repo=c/d"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.demo || !f.mono || !f.refreshSet || f.refresh != 0 || f.repos.String() != "a/b,c/d" {
		t.Errorf("flags = %+v", f)
	}
	if ov := f.overrides(); ov.RefreshSecs == nil || *ov.RefreshSecs != 0 || !ov.Mono {
		t.Errorf("overrides = %+v", ov)
	}
	f, err = parseFlags(nil)
	if err != nil || f.refreshSet || f.overrides().RefreshSecs != nil {
		t.Errorf("no flags: f=%+v err=%v", f, err)
	}
	_, err = parseFlags([]string{"--bogus"})
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "-bogus") || !strings.Contains(err.Error(), "gh dillodash help") {
		t.Errorf("unknown flag: code=%d err=%v", exitCode(err), err)
	}
	_, err = parseFlags([]string{"frobnicate"})
	if exitCode(err) != 2 || !strings.Contains(err.Error(), `unknown command "frobnicate"`) {
		t.Errorf("stray arg: code=%d err=%v", exitCode(err), err)
	}
	out := capture(t, func() {
		_, err = parseFlags([]string{"--help"})
	})
	if !errors.Is(err, errHelp) || !strings.Contains(out, "usage:") || !strings.Contains(out, "--demo") {
		t.Errorf("--help: err=%v out=%q", err, out)
	}
}

func TestVersionAndHelp(t *testing.T) {
	was := version
	version = "v9.9.9"
	t.Cleanup(func() { version = was })
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		out := capture(t, func() {
			if err := run(args); err != nil {
				t.Error(err)
			}
		})
		if out != "dillodash v9.9.9\n" {
			t.Errorf("%v printed %q", args, out)
		}
	}
	out := capture(t, func() {
		if err := run([]string{"help"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.HasPrefix(out, "dillodash v9.9.9 — ") {
		t.Errorf("help header: %q", firstLine(out))
	}
	// every flag parseFlags knows is in the help text, and --help is exit 0
	for _, fl := range []string{"--config", "--repo", "--refresh", "--icons", "--theme", "--motion", "--mono", "--demo"} {
		if !strings.Contains(out, fl) {
			t.Errorf("help lacks %s", fl)
		}
	}
	out = capture(t, func() {
		if err := run([]string{"--demo", "--help"}); err != nil {
			t.Errorf("--help: %v", err)
		}
	})
	if !strings.Contains(out, "usage:") {
		t.Errorf("--help printed %q", out)
	}
}

func TestSubcommandExitCodes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DILLODASH_CONFIG", "")
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"config"}, 2, "check, pull, init or migrate"},
		{[]string{"config", "bogus"}, 2, `unknown subcommand "bogus"`},
		{[]string{"font"}, 2, "install or check"},
		{[]string{"font", "bogus"}, 2, `unknown subcommand "bogus"`},
		{[]string{"config", "check"}, 2, "no config found"},
		{[]string{"config", "migrate"}, 2, "no pr-dash config.ini found"},
		{[]string{"--repo", "x"}, 2, "no config found"},
	} {
		err := run(tc.args)
		if exitCode(err) != tc.code || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: code=%d err=%v (want %d, %q)", tc.args, exitCode(err), err, tc.code, tc.want)
		}
	}
	// init writes the starter once; a second init refuses
	out := capture(t, func() {
		if err := run([]string{"config", "init"}); err != nil {
			t.Fatal(err)
		}
	})
	path := filepath.Join(home, ".config", "dillodash", "config.toml")
	if !strings.Contains(out, "wrote "+path) {
		t.Errorf("init printed %q", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"config", "init"}); exitCode(err) != 2 || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}
	// a bad --repo fails before any network, naming the flag
	if err := os.WriteFile(path, []byte("[reviews]\nrepos = [\"acme/infra\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--repo", "not-a-repo"})
	if exitCode(err) != 2 || !strings.Contains(err.Error(), `--repo: "not-a-repo" is not "owner/name"`) {
		t.Errorf("bad --repo: code=%d err=%v", exitCode(err), err)
	}
	err = run([]string{"config", "check", "--repo", "not-a-repo"})
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "--repo") {
		t.Errorf("config check with a bad --repo: code=%d err=%v", exitCode(err), err)
	}
}

// capture returns what fn wrote to stdout.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	was := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stdout = was
	w.Close()
	return <-done
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestRefuseBot(t *testing.T) {
	cfg := config.Default()
	cfg.Bots.Logins = append(cfg.Bots.Logins, "infra-bot")
	if err := refuseBot(cfg, "alice", "keyring"); err != nil {
		t.Errorf("a human runs: %v", err)
	}
	err := refuseBot(cfg, "dependabot[bot]", "keyring")
	if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "bot login") || !strings.Contains(err.Error(), "gh auth switch") {
		t.Errorf("[bot] suffix: %v", err)
	}
	err = refuseBot(cfg, "infra-bot", "GH_TOKEN")
	if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "unset GH_TOKEN") {
		t.Errorf("configured bot from the environment: %v", err)
	}
}
