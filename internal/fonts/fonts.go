// Package fonts embeds Hack Nerd Font Mono (Regular and Bold, Nerd Fonts
// v3.5.1) and installs it per user, so the nerd icon set has a font to
// draw with. It is `gh dillodash font install|check`.
//
// Where the files go: macOS ~/Library/Fonts; Linux and the BSDs
// $XDG_DATA_HOME/fonts/dillodash (default ~/.local/share/fonts/dillodash),
// then `fc-cache -f`; Windows %LOCALAPPDATA%\Microsoft\Windows\Fonts plus
// one HKCU Fonts registry value per file, which is what Explorer's
// "Install" does for a non-admin; WSL both the Linux directory (WSLg
// terminals) and the Windows one (Windows Terminal draws WSL), found
// through cmd.exe and wslpath. A file is rewritten only when the copy on
// disk does not hash to the embedded one (or Force is set), and nothing
// is ever deleted. Everything that leaves the process (fc-cache, cmd.exe,
// wslpath, reg.exe) goes through Options.Run, so tests stub it.
//
// Fonts render where the terminal runs: over SSH the font must also be
// installed on the client machine, and SSHHint says how.
package fonts

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed HackNerdFontMono-Regular.ttf
var regular []byte

//go:embed HackNerdFontMono-Bold.ttf
var bold []byte

// The pinned font: the family name terminals list it under, the Nerd
// Fonts release it comes from, and the two files with their SHA256.
const (
	Family  = "Hack Nerd Font Mono"
	Version = "v3.5.1"

	RegularName   = "HackNerdFontMono-Regular.ttf"
	RegularSHA256 = "e5aeb58577a8acf2b42083e4a07ce48bd53116fdd868636403cadb5b547351be"
	BoldName      = "HackNerdFontMono-Bold.ttf"
	BoldSHA256    = "7c2c76c26ff80fb546798b204ae519335573417c969eb3142d202298722d3832"

	// Subdir is the directory under $XDG_DATA_HOME/fonts on Linux.
	Subdir = "dillodash"

	winFontsKey = `HKCU\Software\Microsoft\Windows NT\CurrentVersion\Fonts`
)

// File is one embedded font file.
type File struct {
	Name   string
	SHA256 string
	Data   []byte
}

// Files lists the embedded fonts, Regular then Bold. The slices alias the
// embedded data: do not modify them.
func Files() []File {
	return []File{
		{Name: RegularName, SHA256: RegularSHA256, Data: regular},
		{Name: BoldName, SHA256: BoldSHA256, Data: bold},
	}
}

// Options controls Install, Check, Paths and SSHHint. The zero value
// means the real machine; every field exists so tests can stand in for
// the OS, the environment and the external commands.
type Options struct {
	// OS is "darwin", "linux" or "windows"; "" = runtime.GOOS. Any other
	// value (the BSDs) takes the Linux branch.
	OS string
	// Env looks a variable up; nil = os.Getenv. Read: XDG_DATA_HOME,
	// LOCALAPPDATA, WSL_DISTRO_NAME, SSH_CONNECTION, SSH_TTY, USER.
	Env func(string) string
	// Home is the user's home directory; "" = os.UserHomeDir().
	Home string
	// ProcVersion returns the contents of /proc/version, which says
	// "microsoft" on WSL; nil = read the file. WSL_DISTRO_NAME in Env
	// counts too (a custom kernel).
	ProcVersion func() string
	// Hostname is what the SSH hint names this machine as; "" =
	// os.Hostname().
	Hostname string
	// Run executes an external command (fc-cache, cmd.exe, wslpath,
	// reg.exe) and returns its stdout; nil = os/exec. A command that is
	// not on PATH is an error.
	Run func(argv []string) (string, error)
	// Out receives progress lines (one per directory, like the "font:"
	// lines of an installer); nil = discard. Notes are NOT written to
	// Out: the caller prints Result.Notes.
	Out io.Writer
	// Force rewrites the files even when the installed copies match.
	Force bool
}

// Result says what Install or Check found.
type Result struct {
	// Installed: paths Install wrote; for Check, paths that exist and
	// hash to the embedded file.
	Installed []string
	// Skipped: paths Install left alone because they were already
	// current. Check leaves it empty.
	Skipped []string
	// Missing: Check only: paths that are absent or differ, which
	// Install would write.
	Missing []string
	// Notes: hints for the user, in order: the terminal setting to
	// change, a restart when Windows changed, the SSH scp lines.
	Notes []string
}

// target is one directory the fonts go in.
type target struct {
	dir     string // where the files are written, as a path this process opens
	label   string // how messages name it
	winDir  string // the same directory as Windows sees it; "" = not Windows
	fcCache bool   // refresh fontconfig after a change
}

func (o *Options) defaults() error {
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Env == nil {
		o.Env = os.Getenv
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home directory: %w", err)
		}
		o.Home = h
	}
	if o.ProcVersion == nil {
		o.ProcVersion = func() string {
			b, _ := os.ReadFile("/proc/version")
			return string(b)
		}
	}
	if o.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			o.Hostname = h
		} else {
			o.Hostname = "localhost"
		}
	}
	if o.Run == nil {
		o.Run = run
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	return nil
}

// run is the default Options.Run: argv[0] with the rest as arguments,
// stdout returned, stderr folded into the error.
func run(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			err = fmt.Errorf("%w: %s", err, s)
		}
		return string(out), fmt.Errorf("%s: %w", argv[0], err)
	}
	return string(out), nil
}

// isWSL: Linux whose kernel says microsoft, or a WSL distro name in the
// environment (a custom kernel).
func (o Options) isWSL() bool {
	if o.OS != "linux" {
		return false
	}
	return strings.Contains(strings.ToLower(o.ProcVersion()), "microsoft") || o.Env("WSL_DISTRO_NAME") != ""
}

// winJoin joins Windows path elements with backslashes, whatever the host.
func winJoin(elem ...string) string {
	out := strings.TrimRight(elem[0], `\`)
	for _, e := range elem[1:] {
		out += `\` + e
	}
	return out
}

// windowsTarget is the per-user Windows fonts directory: winLocal is
// %LOCALAPPDATA% as Windows writes it, dir the same directory as a path
// this process can open.
func windowsTarget(winLocal, dir string) target {
	winDir := winJoin(winLocal, "Microsoft", "Windows", "Fonts")
	return target{
		dir:    filepath.Join(dir, "Microsoft", "Windows", "Fonts"),
		label:  "Windows (" + winDir + ")",
		winDir: winDir,
	}
}

// targets lists the directories for this OS, Windows first on WSL (a
// WSL install that cannot reach Windows is useless, so it fails before
// the Linux copy).
func targets(o Options) ([]target, error) {
	switch o.OS {
	case "darwin":
		return []target{{dir: filepath.Join(o.Home, "Library", "Fonts"), label: filepath.Join(o.Home, "Library", "Fonts")}}, nil
	case "windows":
		local := o.Env("LOCALAPPDATA")
		if local == "" {
			return nil, errors.New("LOCALAPPDATA is unset, so the Windows fonts directory is unknown")
		}
		return []target{windowsTarget(local, local)}, nil
	}
	data := o.Env("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(o.Home, ".local", "share")
	}
	dir := filepath.Join(data, "fonts", Subdir)
	linux := target{dir: dir, label: dir, fcCache: true}
	if !o.isWSL() {
		return []target{linux}, nil
	}
	out, err := o.Run([]string{"cmd.exe", "/d", "/c", "echo %LOCALAPPDATA%"})
	if err != nil {
		return nil, fmt.Errorf("this is WSL, but cmd.exe isn't usable (Windows interop off?), so the font can't reach Windows, where your terminal draws it: %w. "+
			"Copy %s and %s to Windows, right-click, Install.", err, RegularName, BoldName)
	}
	winLocal := strings.TrimSpace(out)
	if winLocal == "" || strings.Contains(winLocal, "%") {
		return nil, errors.New("couldn't read %LOCALAPPDATA% from Windows through cmd.exe")
	}
	out, err = o.Run([]string{"wslpath", "-u", winLocal})
	if err != nil {
		return nil, fmt.Errorf("wslpath couldn't translate %s: %w", winLocal, err)
	}
	posix := strings.TrimSpace(out)
	if posix == "" {
		return nil, fmt.Errorf("wslpath gave no path for %s", winLocal)
	}
	return []target{windowsTarget(winLocal, posix), linux}, nil
}

// Paths lists where the font files go on this OS: two per directory,
// Windows first on WSL. Best effort: a Windows directory that cannot be
// found is left out; Install and Check report the error.
func Paths(o Options) []string {
	if err := o.defaults(); err != nil {
		return nil
	}
	ts, err := targets(o)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range ts {
		for _, f := range Files() {
			out = append(out, filepath.Join(t.dir, f.Name))
		}
	}
	return out
}

// verifyEmbedded refuses to install a font that is not the checksummed
// one: a broken font fails silently in the terminal.
func verifyEmbedded() error {
	for _, f := range Files() {
		if got := sum(f.Data); got != f.SHA256 {
			return fmt.Errorf("embedded %s doesn't match its checksum (%s, want %s): not installing it", f.Name, got, f.SHA256)
		}
	}
	return nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// state of a file on disk against the embedded one.
func state(path, want string) (exists, current bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, sum(b) == want, nil
}

// register adds (or re-asserts) the HKCU Fonts value for each file: cheap,
// and it repairs a value someone deleted.
func register(o Options, t target) error {
	for _, f := range Files() {
		argv := []string{"reg.exe", "add", winFontsKey,
			"/v", valueName(f.Name), "/t", "REG_SZ",
			"/d", winJoin(t.winDir, f.Name), "/f"}
		if _, err := o.Run(argv); err != nil {
			return fmt.Errorf("reg.exe couldn't register %s under %s: %w", f.Name, winFontsKey, err)
		}
	}
	return nil
}

// valueName is the registry value for a file: "HackNerdFontMono-Regular
// (TrueType)".
func valueName(file string) string {
	return strings.TrimSuffix(file, ".ttf") + " (TrueType)"
}

// Install writes the fonts where this OS looks for per-user fonts,
// registers them with Windows where that applies, refreshes fontconfig on
// Linux, and returns what it did. A directory it cannot write is an
// error; what was done before the error stays done.
func Install(o Options) (Result, error) {
	var res Result
	if err := o.defaults(); err != nil {
		return res, err
	}
	if err := verifyEmbedded(); err != nil {
		return res, err
	}
	ts, err := targets(o)
	if err != nil {
		return res, err
	}
	for _, t := range ts {
		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			return res, fmt.Errorf("can't create %s: %w", t.dir, err)
		}
		changed, existed := false, false
		for _, f := range Files() {
			p := filepath.Join(t.dir, f.Name)
			exists, current, err := state(p, f.SHA256)
			if err != nil {
				return res, fmt.Errorf("can't read %s: %w", p, err)
			}
			existed = existed || exists
			if current && !o.Force {
				res.Skipped = append(res.Skipped, p)
				continue
			}
			if err := os.WriteFile(p, f.Data, 0o644); err != nil {
				if t.winDir != "" {
					return res, fmt.Errorf("can't write %s: %w (Windows locks a font in use: close the terminal using it, or sign out and back in, then re-run)", p, err)
				}
				return res, fmt.Errorf("can't write %s: %w", p, err)
			}
			res.Installed = append(res.Installed, p)
			changed = true
		}
		switch {
		case !changed:
			fmt.Fprintf(o.Out, "font: no change: %s already in %s\n", Family, t.label)
		case existed:
			fmt.Fprintf(o.Out, "font: upgraded %s in %s\n", Family, t.label)
		default:
			fmt.Fprintf(o.Out, "font: installed %s to %s\n", Family, t.label)
		}
		if t.winDir != "" {
			if err := register(o, t); err != nil {
				return res, err
			}
			if changed {
				res.Notes = append(res.Notes, "restart Windows Terminal to see it (sign out and back in if it still isn't listed)")
			}
		}
		if t.fcCache && changed {
			if _, err := o.Run([]string{"fc-cache", "-f", t.dir}); err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("fc-cache -f %s failed (%v): terminals may not list the font until the fontconfig cache is refreshed", t.dir, err))
			}
		}
	}
	res.Notes = append(res.Notes, terminalNote)
	res.Notes = append(res.Notes, SSHHint(o)...)
	return res, nil
}

const terminalNote = `set your terminal's font to "` + Family + `", then check: gh dillodash glyph-test`

// Check reports which installed copies exist and match the embedded
// files, and (on Windows and WSL) whether they are registered, without
// writing anything. Notes say what Install would do.
func Check(o Options) (Result, error) {
	var res Result
	if err := o.defaults(); err != nil {
		return res, err
	}
	ts, err := targets(o)
	if err != nil {
		return res, err
	}
	for _, t := range ts {
		for _, f := range Files() {
			p := filepath.Join(t.dir, f.Name)
			exists, current, err := state(p, f.SHA256)
			if err != nil {
				return res, fmt.Errorf("can't read %s: %w", p, err)
			}
			switch {
			case current:
				res.Installed = append(res.Installed, p)
				fmt.Fprintf(o.Out, "font: ok       %s\n", p)
			case exists:
				res.Missing = append(res.Missing, p)
				fmt.Fprintf(o.Out, "font: differs  %s\n", p)
				res.Notes = append(res.Notes, "install would replace "+p+" (it doesn't match the bundled "+Family+" "+Version+")")
			default:
				res.Missing = append(res.Missing, p)
				fmt.Fprintf(o.Out, "font: missing  %s\n", p)
				res.Notes = append(res.Notes, "install would write "+p)
			}
		}
		if t.winDir != "" {
			for _, f := range Files() {
				want := winJoin(t.winDir, f.Name)
				out, err := o.Run([]string{"reg.exe", "query", winFontsKey, "/v", valueName(f.Name)})
				if err != nil || !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
					res.Notes = append(res.Notes, fmt.Sprintf("install would register %q with Windows as %s", valueName(f.Name), want))
				}
			}
		}
	}
	if len(res.Missing) == 0 {
		res.Notes = append(res.Notes, terminalNote)
	}
	res.Notes = append(res.Notes, SSHHint(o)...)
	return res, nil
}

// SSHHint is the note for an SSH session, where the font has to be
// installed on the machine the terminal runs on: it names the installed
// copies on this host so they can be copied with scp. Nil when
// $SSH_CONNECTION and $SSH_TTY are both unset.
func SSHHint(o Options) []string {
	if err := o.defaults(); err != nil {
		return nil
	}
	if o.Env("SSH_CONNECTION") == "" && o.Env("SSH_TTY") == "" {
		return nil
	}
	user := o.Env("USER")
	if user == "" {
		user = "USER"
	}
	ts, err := targets(o)
	if err != nil || len(ts) == 0 {
		return []string{sshIntro}
	}
	// The Linux (or macOS) copy, not the Windows one: that is the path
	// scp sees.
	src := filepath.ToSlash(filepath.Join(ts[len(ts)-1].dir, "HackNerdFontMono-*.ttf"))
	from := fmt.Sprintf("'%s@%s:%s'", user, o.Hostname, src)
	return []string{
		sshIntro,
		"  macOS:   scp " + from + " ~/Library/Fonts/",
		"  Linux:   scp " + from + " ~/.local/share/fonts/ && fc-cache -f",
		"  Windows: copy the two .ttf files there, right-click, Install.",
		`until it's there, run with --icons unicode (or icons = "unicode" in the config).`,
	}
}

const sshIntro = "this is an SSH session: fonts render on the machine your TERMINAL runs on, not here, " +
	"so install the font there too: run `gh dillodash font install` there (macOS, Linux, WSL, Windows), or copy it:"
