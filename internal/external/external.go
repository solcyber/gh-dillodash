// Package external holds the pieces for leaving the terminal: the editor
// and pager commands from the environment, temp files for them, the
// clipboard and the browser. The app itself suspends the TUI around the
// programs these run; this package only runs them.
package external

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"
)

// Hooks tests replace: how a command is run to completion (stdin fed from
// a string), how one is started and left running, where the OSC 52
// escape goes, and which OS this is.
var (
	runCommand   = run
	startCommand = start
	writeTTY     = writeDevTTY
	goos         = runtime.GOOS
)

// SplitWords splits a command line into words the way a POSIX shell
// would, enough for $EDITOR and $PAGER values: single quotes keep
// everything literal, double quotes allow \" and \\, a backslash outside
// quotes escapes the next character. An unterminated quote is an error.
func SplitWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			i++
			cur.WriteByte(s[i])
			have = true
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			have = true
			i += end + 1
		case '"':
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
					cur.WriteByte(s[i])
					continue
				}
				if s[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
			have = true
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}

// EditorCommand is the editor to run: $VISUAL, else $EDITOR, else vi,
// split into words.
func EditorCommand() ([]string, error) {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	argv, err := SplitWords(ed)
	if err != nil {
		return nil, fmt.Errorf("editor %q: %w", ed, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("editor %q: empty command", ed)
	}
	return argv, nil
}

// PagerCommand is the pager to run: $PAGER split into words, else less.
func PagerCommand() []string {
	if p := os.Getenv("PAGER"); p != "" {
		if argv, err := SplitWords(p); err == nil && len(argv) > 0 {
			return argv
		}
	}
	return []string{"less"}
}

// exitStatus is what a non-zero exit looks like (exec.ExitError, or a
// test's stand-in).
type exitStatus interface{ ExitCode() int }

// Run runs an interactive program (editor, pager) on the real terminal
// and returns its exit status as the error (an *exec.ExitError for a
// non-zero exit). Ctrl-C meanwhile is swallowed here: it reaches the
// child as well, and used to kill the dashboard too. A handler, not
// SIG_IGN, which the child would inherit across exec.
func Run(argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	defer signal.Stop(ch)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// EditInTemp opens the editor on a temp file seeded with seed and returns
// the saved text, stripped. ok is false when the editor exited non-zero
// (vim's :cq, the git-commit abort convention); err is set when it could
// not be started. run runs the editor (nil means Run). The file is
// removed in every case.
func EditInTemp(seed string, run func(argv []string) error) (text string, ok bool, err error) {
	argv, err := EditorCommand()
	if err != nil {
		return "", false, err
	}
	if run == nil {
		run = Run
	}
	f, err := os.CreateTemp("", "dillodash-*.md")
	if err != nil {
		return "", false, err
	}
	path := f.Name()
	defer os.Remove(path)
	if err := os.Chmod(path, 0o600); err != nil {
		f.Close()
		return "", false, err
	}
	if _, err := f.WriteString(seed); err != nil {
		f.Close()
		return "", false, err
	}
	if err := f.Close(); err != nil {
		return "", false, err
	}
	if err := run(append(argv, path)); err != nil {
		var es exitStatus
		if errors.As(err, &es) {
			return "", false, nil
		}
		return "", false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(raw), "�")), true, nil
}

// WriteTempDiff writes a PR's diff to a temp file for the pager and
// returns its path and a cleanup that removes it.
func WriteTempDiff(number int, diff string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", fmt.Sprintf("dillodash-%d-*.diff", number))
	if err != nil {
		return "", nil, err
	}
	path = f.Name()
	cleanup = func() { os.Remove(path) }
	if _, err := f.WriteString(strings.ToValidUTF8(diff, "�")); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// CopyToClipboard puts s on the clipboard from inside a terminal app,
// over SSH too. Inside tmux, `tmux load-buffer -w` (tmux forwards it to
// the outer terminal's clipboard itself; its default set-clipboard=
// external drops OSC 52 written by applications); otherwise an OSC 52
// escape straight to the tty; failing that, the local clipboard tools.
// Returns a word for the status line, "" when nothing worked.
func CopyToClipboard(s string) (where string) {
	if os.Getenv("TMUX") != "" {
		if runCommand([]string{"tmux", "load-buffer", "-w", "-"}, s) == nil {
			return "tmux buffer + clipboard"
		}
		if runCommand([]string{"tmux", "load-buffer", "-"}, s) == nil {
			return "tmux buffer"
		}
	}
	osc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07"
	if writeTTY(osc) == nil {
		return "clipboard (OSC 52)"
	}
	switch goos {
	case "darwin":
		if runCommand([]string{"pbcopy"}, s) == nil {
			return "pbcopy"
		}
	case "windows":
		if runCommand([]string{"clip"}, s) == nil {
			return "clip"
		}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" && runCommand([]string{"wl-copy"}, s) == nil {
			return "wl-copy"
		}
		if os.Getenv("DISPLAY") != "" && runCommand([]string{"xclip", "-selection", "clipboard"}, s) == nil {
			return "xclip"
		}
	}
	return ""
}

// OpenURL opens url in a browser when one is reachable (macOS, Windows,
// or an X11 / Wayland session) and reports whether it was started. Not a
// generic opener: on a headless host that would happily run lynx or w3m
// INSIDE this terminal.
func OpenURL(url string) bool {
	var argv []string
	switch {
	case goos == "darwin":
		argv = []string{"open", url}
	case goos == "windows":
		argv = []string{"cmd", "/c", "start", "", url}
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		argv = []string{"xdg-open", url}
	default:
		return false
	}
	return startCommand(argv) == nil
}

// run runs argv to completion with stdin fed from input and its output
// discarded, giving up after three seconds.
func run(argv []string, stdin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.Run()
}

// start launches argv detached from the terminal and does not wait.
func start(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// writeDevTTY writes s to the controlling terminal.
func writeDevTTY(s string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	_, err = tty.WriteString(s)
	return err
}
