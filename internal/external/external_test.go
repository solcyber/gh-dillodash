package external

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeExit stands in for exec.ExitError: a non-zero exit.
type fakeExit struct{ code int }

func (e fakeExit) Error() string { return "exit status" }
func (e fakeExit) ExitCode() int { return e.code }

// call is one command a fake runner saw.
type call struct {
	argv  []string
	stdin string
}

// fakeRunners installs fakes for the command runners and the tty, and
// returns the calls they saw. ok names the commands that succeed (by
// argv[0]); tty says whether /dev/tty is writable.
func fakeRunners(t *testing.T, ok map[string]bool, tty bool) (*[]call, *[]string) {
	t.Helper()
	var calls []call
	var written []string
	oldRun, oldStart, oldTTY, oldOS := runCommand, startCommand, writeTTY, goos
	runCommand = func(argv []string, stdin string) error {
		calls = append(calls, call{argv, stdin})
		if ok[argv[0]] {
			return nil
		}
		return errors.New("not found")
	}
	startCommand = func(argv []string) error { return runCommand(argv, "") }
	writeTTY = func(s string) error {
		if !tty {
			return errors.New("no tty")
		}
		written = append(written, s)
		return nil
	}
	t.Cleanup(func() { runCommand, startCommand, writeTTY, goos = oldRun, oldStart, oldTTY, oldOS })
	return &calls, &written
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, v := range []string{"GH_EDITOR", "VISUAL", "EDITOR", "GH_PAGER", "PAGER", "TMUX", "DISPLAY", "WAYLAND_DISPLAY"} {
		t.Setenv(v, "")
	}
	oldOS := goos
	goos = "linux"
	t.Cleanup(func() { goos = oldOS })
}

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"vi", []string{"vi"}, false},
		{"code --wait", []string{"code", "--wait"}, false},
		{"  nvim   -u NONE  ", []string{"nvim", "-u", "NONE"}, false},
		{"'my editor' -x", []string{"my editor", "-x"}, false},
		{`"a b"\ c d`, []string{"a b c", "d"}, false},
		{`"say \"hi\"" x`, []string{`say "hi"`, "x"}, false},
		{`it\'s`, []string{"it's"}, false},
		{`'a "b"'`, []string{`a "b"`}, false},
		{`""`, []string{""}, false},
		{"", nil, false},
		{"'open", nil, true},
		{`"open`, nil, true},
		{`trail\`, nil, true},
	}
	for _, c := range cases {
		got, err := splitWords(c.in)
		if (err != nil) != c.err {
			t.Errorf("splitWords(%q): err %v", c.in, err)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("splitWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEditorCommand(t *testing.T) {
	isolate(t)
	if argv, err := EditorCommand(); err != nil || strings.Join(argv, " ") != "vi" {
		t.Errorf("default: %q %v", argv, err)
	}
	t.Setenv("EDITOR", "nano -w")
	if argv, _ := EditorCommand(); strings.Join(argv, " ") != "nano -w" {
		t.Errorf("EDITOR: %q", argv)
	}
	t.Setenv("VISUAL", "'/Apps/My Editor' --wait")
	if argv, _ := EditorCommand(); strings.Join(argv, "|") != "/Apps/My Editor|--wait" {
		t.Errorf("VISUAL wins: %q", argv)
	}
	t.Setenv("VISUAL", "")
	if argv, _ := EditorCommand(); strings.Join(argv, " ") != "nano -w" {
		t.Errorf("empty VISUAL falls through: %q", argv)
	}
	t.Setenv("GH_EDITOR", "code --wait")
	if argv, _ := EditorCommand(); strings.Join(argv, " ") != "code --wait" {
		t.Errorf("GH_EDITOR wins over VISUAL and EDITOR (the gh convention): %q", argv)
	}
	t.Setenv("GH_EDITOR", "")
	t.Setenv("EDITOR", "'broken")
	if _, err := EditorCommand(); err == nil {
		t.Error("an unterminated quote is an error")
	}
	t.Setenv("EDITOR", "")
	goos = "windows"
	if argv, _ := EditorCommand(); strings.Join(argv, " ") != "notepad" {
		t.Errorf("windows default: %q", argv)
	}
	goos = "linux"
	t.Setenv("EDITOR", "   ")
	if _, err := EditorCommand(); err == nil {
		t.Error("a blank command is an error")
	}
}

func TestPagerCommand(t *testing.T) {
	isolate(t)
	if got := PagerCommand(); strings.Join(got, " ") != "less" {
		t.Errorf("default: %q", got)
	}
	t.Setenv("PAGER", "less -R +G")
	if got := PagerCommand(); strings.Join(got, "|") != "less|-R|+G" {
		t.Errorf("PAGER: %q", got)
	}
	t.Setenv("PAGER", "'bad")
	if got := PagerCommand(); strings.Join(got, " ") != "less" {
		t.Errorf("an unparsable PAGER falls back: %q", got)
	}
	t.Setenv("GH_PAGER", "bat -p")
	if got := PagerCommand(); strings.Join(got, "|") != "bat|-p" {
		t.Errorf("GH_PAGER wins (the gh convention): %q", got)
	}
	t.Setenv("GH_PAGER", "")
	t.Setenv("PAGER", "")
	goos = "windows"
	if got := PagerCommand(); strings.Join(got, " ") != "more" {
		t.Errorf("windows has no less: %q", got)
	}
}

func TestEditInTemp(t *testing.T) {
	isolate(t)
	t.Setenv("EDITOR", "fake-editor --flag")
	var seen []string
	var path string
	editor := func(argv []string) error {
		seen = argv
		path = argv[len(argv)-1]
		if runtime.GOOS != "windows" {
			if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("temp file mode: %v %v", st.Mode(), err)
			}
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("café ✓\n")
		return err
	}
	text, ok, err := EditInTemp("x ", editor)
	if err != nil || !ok || text != "x café ✓" {
		t.Errorf("UTF-8 round trip: %q %v %v", text, ok, err)
	}
	if len(seen) != 3 || seen[0] != "fake-editor" || seen[1] != "--flag" {
		t.Errorf("argv = %q", seen)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "dillodash-") || !strings.HasSuffix(base, ".md") {
		t.Errorf("temp name = %q", base)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the temp file is removed afterwards")
	}
	// a seed that is read back untouched is stripped
	text, ok, err = EditInTemp("  keep  \n", func([]string) error { return nil })
	if err != nil || !ok || text != "keep" {
		t.Errorf("stripped: %q %v %v", text, ok, err)
	}
	// a non-zero exit (vim :cq) means no text, not an error
	path = ""
	text, ok, err = EditInTemp("x", func(argv []string) error {
		path = argv[len(argv)-1]
		return fakeExit{1}
	})
	if err != nil || ok || text != "" {
		t.Errorf("non-zero exit: %q %v %v", text, ok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the temp file is removed after a non-zero exit too")
	}
	// an editor that cannot start is an error
	boom := errors.New("exec: not found")
	if _, ok, err := EditInTemp("x", func([]string) error { return boom }); ok || !errors.Is(err, boom) {
		t.Errorf("start failure: %v %v", ok, err)
	}
	// a broken $EDITOR is reported before any file is made
	t.Setenv("EDITOR", "'oops")
	if _, _, err := EditInTemp("x", func([]string) error { t.Error("ran"); return nil }); err == nil {
		t.Error("bad editor: error")
	}
	// invalid bytes are replaced rather than failing the read
	t.Setenv("EDITOR", "fake-editor")
	text, ok, err = EditInTemp("", func(argv []string) error {
		return os.WriteFile(argv[len(argv)-1], []byte("a\xffb"), 0o600)
	})
	if err != nil || !ok || text != "a�b" {
		t.Errorf("invalid utf-8: %q %v %v", text, ok, err)
	}
}

func TestWriteTempDiff(t *testing.T) {
	path, cleanup, err := WriteTempDiff(42, "--- a\n+++ b\n")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "dillodash-42-") || !strings.HasSuffix(base, ".diff") {
		t.Errorf("name = %q", base)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "--- a\n+++ b\n" {
		t.Errorf("contents = %q %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup removes the file")
	}
}

func TestCopyToClipboard(t *testing.T) {
	isolate(t)
	url := "https://github.com/acme/widgets/pull/7"
	osc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(url)) + "\x07"

	// inside tmux: load-buffer -w first
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	calls, written := fakeRunners(t, map[string]bool{"tmux": true}, true)
	if where := CopyToClipboard(url); where != "tmux buffer + clipboard" {
		t.Errorf("tmux -w: %q", where)
	}
	if len(*calls) != 1 || strings.Join((*calls)[0].argv, " ") != "tmux load-buffer -w -" || (*calls)[0].stdin != url {
		t.Errorf("calls = %+v", *calls)
	}
	if len(*written) != 0 {
		t.Error("tmux handled it: nothing on the tty")
	}

	// an old tmux without -w: the plain buffer
	calls, _ = fakeRunners(t, nil, true)
	runCommand = func(argv []string, stdin string) error {
		*calls = append(*calls, call{argv, stdin})
		if len(argv) == 3 { // "tmux load-buffer -"
			return nil
		}
		return errors.New("unknown option -w")
	}
	if where := CopyToClipboard(url); where != "tmux buffer" {
		t.Errorf("tmux without -w: %q", where)
	}
	if len(*calls) != 2 {
		t.Errorf("calls = %+v", *calls)
	}

	// tmux failing and no local tool: OSC 52 on the tty, last
	calls, written = fakeRunners(t, nil, true)
	goos = "linux"
	if where := CopyToClipboard(url); where != "the terminal via OSC 52 (if it allows it)" {
		t.Errorf("tmux failing: %q", where)
	}
	if len(*written) != 1 || (*written)[0] != osc {
		t.Errorf("tty got %q", *written)
	}

	// outside tmux with no tool: the tty, no commands
	t.Setenv("TMUX", "")
	calls, written = fakeRunners(t, nil, true)
	goos = "linux"
	if where := CopyToClipboard(url); !strings.Contains(where, "OSC 52") || len(*calls) != 0 || len(*written) != 1 {
		t.Errorf("no tmux: %q calls %+v written %d", where, *calls, len(*written))
	}

	// the local tool beats OSC 52 (a terminal may ignore the escape and
	// nothing would say so): pbcopy first on a Mac, even with a tty
	calls, written = fakeRunners(t, map[string]bool{"pbcopy": true}, true)
	goos = "darwin"
	if where := CopyToClipboard(url); where != "pbcopy" || (*calls)[0].stdin != url || len(*written) != 0 {
		t.Errorf("darwin: %q %+v written %d", where, *calls, len(*written))
	}
	calls, _ = fakeRunners(t, map[string]bool{"pbcopy": true}, false)
	goos = "darwin"
	if where := CopyToClipboard(url); where != "pbcopy" || (*calls)[0].stdin != url {
		t.Errorf("darwin, no tty: %q %+v", where, *calls)
	}
	calls, _ = fakeRunners(t, map[string]bool{"wl-copy": true, "xclip": true}, false)
	goos = "linux"
	if where := CopyToClipboard(url); where != "" || len(*calls) != 0 {
		t.Errorf("linux, no display: %q %+v", where, *calls)
	}
	t.Setenv("DISPLAY", ":0")
	if where := CopyToClipboard(url); where != "xclip" || strings.Join((*calls)[0].argv, " ") != "xclip -selection clipboard" {
		t.Errorf("x11: %q %+v", where, *calls)
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	calls, _ = fakeRunners(t, map[string]bool{"wl-copy": true, "xclip": true}, false)
	goos = "linux"
	if where := CopyToClipboard(url); where != "wl-copy" {
		t.Errorf("wayland: %q %+v", where, *calls)
	}
	calls, _ = fakeRunners(t, nil, false)
	goos = "linux"
	if where := CopyToClipboard(url); where != "" {
		t.Errorf("nothing works: %q", where)
	}
	if len(*calls) != 2 {
		t.Errorf("both tools tried: %+v", *calls)
	}
}

func TestOpenURL(t *testing.T) {
	isolate(t)
	url := "https://github.com/acme/widgets/issues/9"
	calls, _ := fakeRunners(t, map[string]bool{"open": true, "xdg-open": true, "cmd": true}, false)
	goos = "darwin"
	if !OpenURL(url) || strings.Join((*calls)[0].argv, " ") != "open "+url {
		t.Errorf("darwin: %+v", *calls)
	}
	calls, _ = fakeRunners(t, map[string]bool{"open": true, "xdg-open": true, "cmd": true}, false)
	goos = "linux"
	if OpenURL(url) || len(*calls) != 0 {
		t.Errorf("headless linux: no browser, nothing run: %+v", *calls)
	}
	t.Setenv("DISPLAY", ":0")
	if !OpenURL(url) || strings.Join((*calls)[0].argv, " ") != "xdg-open "+url {
		t.Errorf("x11: %+v", *calls)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	calls, _ = fakeRunners(t, map[string]bool{"xdg-open": true}, false)
	goos = "linux"
	if !OpenURL(url) || len(*calls) != 1 {
		t.Errorf("wayland: %+v", *calls)
	}
	calls, _ = fakeRunners(t, map[string]bool{"rundll32": true}, false)
	goos = "windows"
	if !OpenURL(url) || strings.Join((*calls)[0].argv, " ") != "rundll32 url.dll,FileProtocolHandler "+url {
		t.Errorf("windows (never cmd /c start, which would interpret & in the URL): %+v", *calls)
	}
	// the opener failing to start is a false
	calls, _ = fakeRunners(t, nil, false)
	goos = "darwin"
	if OpenURL(url) || len(*calls) != 1 {
		t.Errorf("open missing: %+v", *calls)
	}
}

func TestRunReportsExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	if err := runTTY([]string{"sh", "-c", "exit 3"}); err == nil {
		t.Fatal("non-zero exit must be an error")
	} else if es, ok := err.(exitStatus); !ok || es.ExitCode() != 3 {
		t.Errorf("exit status: %v", err)
	}
	if err := runTTY([]string{"true"}); err != nil {
		t.Errorf("zero exit: %v", err)
	}
	if err := runTTY(nil); err == nil {
		t.Error("empty argv")
	}
	// through EditInTemp, a real non-zero exit is ok=false
	t.Setenv("VISUAL", "sh -c 'exit 1'")
	if _, ok, err := EditInTemp("x", nil); ok || err != nil {
		t.Errorf("real :cq: %v %v", ok, err)
	}
	t.Setenv("VISUAL", "sh -c 'printf \" done \" >> \"$0\"'")
	if text, ok, err := EditInTemp("x", nil); !ok || err != nil || text != "x done" {
		t.Errorf("real editor: %q %v %v", text, ok, err)
	}
}
