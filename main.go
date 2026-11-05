// Command gh-dillodash is dillodash, a terminal dashboard for reviewing,
// approving, merging and deploying GitHub pull requests, run as a GitHub
// CLI extension: `gh dillodash`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/colorprofile"
	ghconfig "github.com/cli/go-gh/v2/pkg/config"
	"golang.org/x/term"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/demo"
	"github.com/solcyber/gh-dillodash/internal/doctor"
	"github.com/solcyber/gh-dillodash/internal/fonts"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/scenes"
	"github.com/solcyber/gh-dillodash/internal/selfupdate"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/ui"
	"github.com/solcyber/gh-dillodash/internal/workflow"

	// the workflows register themselves
	_ "github.com/solcyber/gh-dillodash/internal/workflow/all"
)

// version is set by the release build (-ldflags "-X main.version=v1.2.3").
// A build from source (gh extension install with no release yet) leaves
// it "dev", and resolveVersion fills it in from the build info.
var version = "dev"

// resolveVersion is v unless it is still "dev" and the build info knows
// better: the module version when it is a real one, else the commit
// ("dev+abc1234", "-dirty" when the tree had changes).
func resolveVersion(v string, bi *debug.BuildInfo, ok bool) string {
	if v != "dev" || !ok || bi == nil {
		return v
	}
	if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
		return mv
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return v
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		return "dev+" + rev + "-dirty"
	}
	return "dev+" + rev
}

// usage is the help text; the first line carries the version.
func usage() string { return "dillodash " + version + usageBody }

const usageBody = ` — a terminal dashboard for pull requests awaiting you

usage:
  gh dillodash [flags]            open the dashboard
  gh dillodash config check       validate the config, the gh login and the token's reach
                                  (takes the dashboard flags: --config, --repo, ...)
  gh dillodash config pull [OWNER/REPO[:PATH]] [--config PATH] [--host HOST] [--force]
                                  install (or refresh) the config from a repo you can read;
                                  --force replaces a hand-written config or switches repos
                                  (the old file is kept as config.toml.bak)
  gh dillodash config init [--config PATH]
                                  write a starter config
  gh dillodash config migrate [--from PATH] [--config PATH] [--force]
                                  convert a legacy config.ini to config.toml
  gh dillodash font install|check [--force]
                                  install the bundled Nerd Font (icons = "nerd")
  gh dillodash font license       print the font's licences (also written beside the installed files)
  gh dillodash glyph-test [--icons SET]
                                  print every glyph and the palette (checks the terminal font)
  gh dillodash update [--rollback]
                                  install the latest release now, verified, over every install
                                  it may replace (the old binary is kept as .prev); --rollback
                                  puts the .prev back
  gh dillodash amos               say hello to Amos
  gh dillodash version
  gh dillodash help               this text (every command takes --help too)

flags:
  --config PATH      config file (default: $DILLODASH_CONFIG, else
                     $XDG_CONFIG_HOME/dillodash/config.toml, else ~/.config/dillodash/config.toml)
  --repo OWNER/NAME  review this repo (repeatable; replaces [reviews] repos)
  --refresh SECS     auto-refresh interval (floor 5)
  --icons SET        nerd | unicode | ascii
  --theme NAME       auto | mono
  --motion MODE      full | off
  --mono             no colors, attributes only (also: NO_COLOR)
  --demo             open the dashboard on an offline fixture, no GitHub needed
  --demo-night       replay a manual release night offline: release NOW, the lane, D

environment:
  DILLODASH_CONFIG   the config file, when --config is not given
  DILLODASH_AUTO_UPDATE
                     0 turns installing updates off on this host ([update] auto)
  GH_HOST            the GitHub host when [github] host is unset (gh's default otherwise)
  GH_TOKEN, GITHUB_TOKEN
                     a token that overrides the gh login (config check says when one does)
  GH_PAGER, PAGER    the pager for v (default less)
  GH_EDITOR, VISUAL, EDITOR
                     the editor for C and t (default vi)
  NO_COLOR           no colors, like --mono
`

type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(s string) error { *r = append(*r, s); return nil }

// subFlags parses a subcommand's flags: define declares them on the
// set, --help prints the usage and returns errHelp, anything else wrong
// is an exit-2 error that names the flag, and a stray word is an
// unknown-command error. The remaining positional words come back.
func subFlags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usage())
			return nil, errHelp
		}
		return nil, exitError{2, fmt.Errorf("%s: %s (see gh dillodash help)", name, err)}
	}
	return fs.Args(), nil
}

// isHelp reports whether a word asks for help.
func isHelp(s string) bool { return s == "help" || s == "--help" || s == "-h" }

// startExe is the running binary, read at startup: once an upgrade
// renames a new binary over it, the process's own link names a deleted
// file.
var startExe string

func main() {
	bi, ok := debug.ReadBuildInfo()
	version = resolveVersion(version, bi, ok)
	startExe, _ = os.Executable()
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errInterrupted) {
			fmt.Fprintln(os.Stderr, "dillodash:", err)
		}
		os.Exit(exitCode(err))
	}
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }

func (e exitError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

// errHelp is what parseFlags returns for --help: the usage was printed
// and the command is done.
var errHelp = errors.New("help requested")

// errInterrupted is the exit-130 error: ctrl-c or SIGINT ended the
// command, and main prints nothing for it.
var errInterrupted = exitError{130, errors.New("interrupted")}

// run dispatches the command line; nil means exit 0.
func run(args []string) error {
	err := dispatch(args)
	switch {
	case errors.Is(err, errHelp):
		return nil
	case errors.Is(err, ui.ErrInterrupted), errors.Is(err, context.Canceled):
		return errInterrupted
	}
	return err
}

func dispatch(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("dillodash %s\n", version)
			return nil
		case "help", "--help", "-h":
			fmt.Print(usage())
			return nil
		case "amos":
			for _, l := range amos.Lines(amos.Options{}) {
				fmt.Println(l)
			}
			return nil
		case "glyph-test":
			icons := "nerd"
			if _, err := subFlags("glyph-test", args[1:], func(fs *flag.FlagSet) {
				fs.StringVar(&icons, "icons", icons, "glyph set")
			}); err != nil {
				return err
			}
			glyphs.Use(glyphs.Set(icons))
			theme.Setup(colorprofile.Detect(os.Stdout, os.Environ()), false)
			glyphs.Report(os.Stdout, theme.Palette)
			return nil
		case "config":
			return configCmd(args[1:])
		case "font":
			return fontCmd(args[1:])
		case "update":
			return updateCmd(args[1:], os.Stdout)
		}
	}
	return dashboard(args)
}

type flags struct {
	demo       bool
	demoNight  bool
	config     string
	repos      repoList
	refresh    int
	refreshSet bool // --refresh was given (0 and below still get the floor warning)
	icons      string
	theme      string
	motion     string
	mono       bool
}

// parseFlags reads the dashboard flags. --help prints the usage and
// returns errHelp; anything else wrong is an exit-2 error that names
// the flag (the flag package's own message, plus where to look).
func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("dillodash", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // one message, ours, on stderr
	fs.Usage = func() {}
	fs.StringVar(&f.config, "config", "", "config path")
	fs.Var(&f.repos, "repo", "owner/name (repeatable)")
	fs.IntVar(&f.refresh, "refresh", 0, "refresh seconds")
	fs.StringVar(&f.icons, "icons", "", "glyph set")
	fs.StringVar(&f.theme, "theme", "", "theme")
	fs.StringVar(&f.motion, "motion", "", "motion")
	fs.BoolVar(&f.mono, "mono", false, "no colors")
	fs.BoolVar(&f.demo, "demo", false, "run on an offline fixture (no GitHub)")
	fs.BoolVar(&f.demoNight, "demo-night", false, "replay a manual release night offline")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usage())
			return f, errHelp
		}
		return f, exitError{2, fmt.Errorf("%s (see gh dillodash help)", err)}
	}
	if fs.NArg() > 0 {
		return f, exitError{2, fmt.Errorf("unknown command %q (see gh dillodash help)", fs.Arg(0))}
	}
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "refresh" {
			f.refreshSet = true
		}
	})
	if f.demo && f.demoNight {
		return f, exitError{2, errors.New("--demo and --demo-night are two different replays: pass one")}
	}
	return f, nil
}

func (f flags) overrides() config.Overrides {
	ov := config.Overrides{Repos: f.repos, Icons: f.icons, Theme: f.theme, Motion: f.motion, Mono: f.mono}
	if f.refreshSet {
		r := f.refresh
		ov.RefreshSecs = &r
	}
	return ov
}

// loadConfig finds and loads the config, printing its warnings.
func loadConfig(f flags) (*config.Config, error) {
	path, legacy, err := config.FindPath(f.config, os.Getenv)
	if err != nil {
		return nil, exitError{2, err}
	}
	if path == "" {
		if legacy != "" {
			return nil, exitError{2, fmt.Errorf("no config.toml found; %s is the legacy INI format. Run: gh dillodash config migrate", legacy)}
		}
		return nil, exitError{2, fmt.Errorf("no config found (looked for %s). Run: gh dillodash config init", config.DefaultPath(os.Getenv))}
	}
	cfg, err := config.Load(path, f.overrides())
	if err != nil {
		return nil, exitError{2, err}
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return cfg, nil
}

// setupDisplay applies the glyph set, the color tier and the motion mode.
func setupDisplay(cfg *config.Config) {
	ui.UseGlyphs(glyphs.Set(cfg.Display.Icons))
	mono := cfg.Display.Theme == "mono" || os.Getenv("NO_COLOR") != ""
	theme.Setup(colorprofile.Detect(os.Stdout, os.Environ()), mono)
	fx.Motion = cfg.Display.Motion
	scenes.Mascot = cfg.Display.Mascot
}

// dashboard runs the TUI. Signals are handled in two phases: before the
// TUI a signal context covers the auth check and the login pause, and
// it is stopped before ui.Run, because Bubble Tea handles SIGINT and
// SIGTERM itself while the terminal is in raw mode and masks them while
// $PAGER or $EDITOR has the terminal (a ctrl-c inside less must not kill
// the dashboard behind it).
func dashboard(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.demo || f.demoNight {
		return runDemo(context.Background(), f)
	}
	cfg, err := loadConfig(f)
	if err != nil {
		return err
	}
	autoUpdateEnv(cfg, os.Getenv)
	restore := ui.DecodeRestore(os.Getenv(ui.RestoreEnv))
	os.Unsetenv(ui.RestoreEnv) // the next restart sets its own
	if err := needTerminal(); err != nil {
		return err
	}
	gh, err := newClient(ghapi.Options{Host: cfg.GitHub.Host, Version: version})
	if err != nil {
		return exitError{2, err}
	}
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// fail fast (and loudly, outside the TUI) if the token is bad, and
	// warn if gh's active account is not the one the config expects
	me, err := gh.Whoami(sig)
	if err != nil {
		if sig.Err() != nil {
			return errInterrupted
		}
		return exitError{2, fmt.Errorf("GitHub auth failed on %s: %w (gh auth status shows the account; gh auth login signs in)", gh.Host(), err)}
	}
	if err := refuseBot(cfg, me, gh.TokenSource()); err != nil {
		return err
	}
	if cfg.GitHub.Login != "" && !strings.EqualFold(cfg.GitHub.Login, me) {
		fmt.Fprintf(os.Stderr, "warning: config expects login %q but gh is signed in as %q\n", cfg.GitHub.Login, me)
		fmt.Fprintln(os.Stderr, "actions will be performed as", me, "(gh auth switch changes it); ctrl-c now to abort")
		select {
		case <-time.After(3 * time.Second):
		case <-sig.Done():
			return errInterrupted
		}
	}
	stop() // from here Bubble Tea owns the signals
	setupDisplay(cfg)
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		return exitError{2, err}
	}
	err = ui.Run(context.Background(), ui.Options{
		Config: cfg,
		// [github] allowed_repos: writes anywhere else are refused here,
		// under every key that posts
		GH:        ghapi.WithAllowedRepos(gh, cfg.GitHub.AllowedRepos),
		Workflows: wf,
		Me:        me,
		Host:      gh.Host(),
		Version:   version,
		Updater:   newChecker(gh),
		Restore:   restore,
	})
	var re *ui.RestartError
	if errors.As(err, &re) {
		return restart(re.Restore)
	}
	return err
}

// autoUpdateEnv applies DILLODASH_AUTO_UPDATE over [update] auto: a
// pulled org config replaces the file, so one host pins itself here.
func autoUpdateEnv(cfg *config.Config, getenv func(string) string) {
	switch strings.ToLower(strings.TrimSpace(getenv("DILLODASH_AUTO_UPDATE"))) {
	case "0", "false", "off", "no":
		cfg.Update.Auto = false
	case "1", "true", "on", "yes":
		cfg.Update.Auto = true
	}
}

// restart runs the binary this process started from (now the new
// release) with the same arguments and the place to restore. The
// dashboard has already quit and given the terminal back.
func restart(r ui.Restore) error {
	if startExe == "" {
		return exitError{1, errors.New("couldn't restart: the running binary's path is unknown; start dillodash again")}
	}
	env := []string{ui.RestoreEnv + "=" + r.Encode()}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, ui.RestoreEnv+"=") {
			env = append(env, kv)
		}
	}
	if err := execSelf(startExe, append([]string{startExe}, os.Args[1:]...), env); err != nil {
		return exitError{1, fmt.Errorf("couldn't restart %s: %w; start dillodash again", startExe, err)}
	}
	return nil
}

// updateCmd is `dillodash update [--rollback]`: install the latest
// release now (whatever [update] auto says: asking is consent), or put
// the .prev binaries back.
func updateCmd(args []string, w io.Writer) error {
	var rollback bool
	if _, err := subFlags("update", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&rollback, "rollback", false, "put the .prev binaries back")
	}); err != nil {
		return err
	}
	var gh client
	if c, err := newClient(ghapi.Options{Host: "github.com", Version: version}); err == nil {
		gh = c
	} else if !rollback {
		return exitError{2, fmt.Errorf("update reads its releases from github.com: %w — gh auth login --hostname github.com", err)}
	}
	u := newChecker(gh)
	if rollback {
		res := u.Installer.Rollback()
		for _, p := range res.Updated {
			fmt.Fprintf(w, "rolled back %s (its replacement is now the .prev)\n", p)
		}
		for _, n := range res.Notes {
			fmt.Fprintf(w, "  note: %s\n", n)
		}
		if res.Err != nil {
			return exitError{1, res.Err}
		}
		fmt.Fprintln(w, "restart dillodash to run it")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runUpdate(ctx, u, w)
}

// runUpdate checks, installs and reports.
func runUpdate(ctx context.Context, u *selfupdate.Updater, w io.Writer) error {
	st := u.Check(ctx, true)
	if st.Latest == "" {
		why := st.Err
		if why == "" {
			why = "no answer"
		}
		return exitError{1, fmt.Errorf("couldn't read the latest release (%s): check gh auth status --hostname github.com, or by hand: %s", why, selfupdate.ManualHint)}
	}
	fmt.Fprintf(w, "dillodash %s; the latest release is %s\n", st.Running, st.Latest)
	switch {
	case !selfupdate.Comparable(st.Running):
		return exitError{2, fmt.Errorf("%s is not a release build — update replaces release builds only: gh extension install solcyber/gh-dillodash", st.Running)}
	case !st.Stale():
		fmt.Fprintln(w, "already current")
		return nil
	}
	res := u.Install(ctx, st)
	for _, p := range res.Updated {
		fmt.Fprintf(w, "installed %s over %s (the old one is %s.prev)\n", res.Version, p, p)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
	if res.Err != nil {
		return exitError{1, fmt.Errorf("%s NOT installed: %v; by hand: %s", st.Latest, res.Err, selfupdate.ManualHint)}
	}
	if len(res.Updated) == 0 {
		return exitError{1, fmt.Errorf("%s NOT installed: no install here can be replaced in place (the notes above say why; gh dillodash config check lists them); by hand: %s", st.Latest, selfupdate.ManualHint)}
	}
	if notes := firstLines(st.Notes, 8); notes != "" {
		fmt.Fprintf(w, "what changed in %s:\n%s\n", res.Version, notes)
	}
	fmt.Fprintln(w, "restart dillodash to run it (R in a running dashboard)")
	return nil
}

// firstLines is the release notes' opening paragraph, indented, at most
// n lines: it stops at the first blank line or heading after some text,
// so a list is never cut mid-item.
func firstLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			if len(out) > 0 {
				break
			}
			continue
		}
		if len(out) == n {
			out[n-1] += " …"
			break
		}
		out = append(out, "  "+l)
	}
	return strings.Join(out, "\n")
}

// findOptions say where this machine's dillodash installs live.
func findOptions() selfupdate.FindOptions {
	return selfupdate.FindOptions{
		Exe: startExe, Running: version, DataDir: ghconfig.DataDir(),
		Path: os.Getenv("PATH"), GOOS: runtime.GOOS, Version: runVersion,
	}
}

// runVersion asks another dillodash binary its version (config check
// only), bounded so a hung binary can't hold the report.
func runVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	return string(out), err
}

// newChecker is the build check and the installer: the latest release
// is read from github.com through gh's login (the dashboard's own client
// when it talks to github.com). With no token for github.com the check
// only reads its cache and nothing is installed. Tests replace it.
var newChecker = func(gh client) *selfupdate.Updater {
	var src selfupdate.Source
	var dl selfupdate.Downloader
	if s, ok := gh.(selfupdate.Source); ok && gh != nil && gh.Host() == "github.com" {
		src = s
	} else if c, err := newClient(ghapi.Options{Host: "github.com", Version: version}); err == nil {
		src, _ = c.(selfupdate.Source)
	}
	dl, _ = src.(selfupdate.Downloader)
	home, _ := os.UserHomeDir()
	find := findOptions()
	return &selfupdate.Updater{
		Checker:   &selfupdate.Checker{Src: src, Path: selfupdate.CachePath(os.Getenv, home), Find: find},
		Installer: &selfupdate.Installer{DL: dl, Run: runProgram, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Find: find},
	}
}

// runProgram runs argv (gh attestation verify) and returns its combined
// output, bounded so a hung gh can't hold an install.
func runProgram(ctx context.Context, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
}

// buildCheck is config check's build report: a fresh check, and every
// install found.
func buildCheck(ctx context.Context) (selfupdate.Status, []selfupdate.Install) {
	c := newChecker(nil)
	return c.Check(ctx, true), selfupdate.Find(c.Find)
}

// client is what main needs of a GitHub client.
type client interface {
	ghapi.Client
	Host() string
	TokenSource() string
}

// newClient builds the real client; tests replace it.
var newClient = func(opts ghapi.Options) (client, error) {
	c, err := ghapi.New(opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// isTerminal reports whether fd is a terminal; tests replace it.
var isTerminal = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }

// needTerminal refuses to draw the TUI into a pipe or a file.
func needTerminal() error {
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		return exitError{2, errors.New("the dashboard needs a terminal on stdin and stdout; gh dillodash config check works without one")}
	}
	return nil
}

// refuseBot refuses to run the dashboard as a login listed in [bots]
// (or any *[bot] account): every key that posts would sign as the bot,
// and the audit trail of who approved only means something when it is
// the human who decided. The usual cause is a CI shell's GH_TOKEN
// leaking into the terminal, so the hint names the token source.
func refuseBot(cfg *config.Config, me, source string) error {
	if !cfg.Bots.IsBot(me) {
		return nil
	}
	return exitError{2, fmt.Errorf("signed in as %s, a bot login ([bots] logins, or the [bot] suffix); dillodash posts reviews, comments and merges as the signed-in user, so it will not run as a bot — %s", me, doctor.BotHint(source))}
}

// runDemo opens the dashboard on the offline fixture. The display flags
// apply; --config and --repo have nothing to override.
func runDemo(ctx context.Context, f flags) error {
	cfg := demo.Config()
	var gh ghapi.Client = demo.Client()
	if f.demoNight {
		clock.Now = demo.NightClock() // the night reads the same whenever it is replayed
		cfg, gh = demo.NightConfig(), demo.NightClient(demo.NightStart).GH
	}
	if f.icons != "" {
		cfg.Display.Icons = f.icons
	}
	if f.motion != "" {
		cfg.Display.Motion = f.motion
	}
	if f.theme != "" {
		cfg.Display.Theme = f.theme
	}
	if f.mono {
		cfg.Display.Theme = "mono"
	}
	if f.refreshSet {
		cfg.Display.RefreshSecs = f.refresh
		if f.refresh < config.MinRefreshSecs {
			fmt.Fprintf(os.Stderr, "warning: --refresh: %d is below the floor of %d, using %d\n", f.refresh, config.MinRefreshSecs, config.MinRefreshSecs)
			cfg.Display.RefreshSecs = config.MinRefreshSecs
		}
	}
	if err := needTerminal(); err != nil {
		return err
	}
	setupDisplay(cfg)
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		return err
	}
	return ui.Run(ctx, ui.Options{Config: cfg, GH: gh, Workflows: wf, Me: demo.Me, Host: "github.com", Version: version + " demo"})
}

func configCmd(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		if len(args) > 0 {
			fmt.Print(usage())
			return errHelp
		}
		return exitError{2, errors.New("config: check, pull, init or migrate? (see gh dillodash help)")}
	}
	switch args[0] {
	case "check":
		f, err := parseFlags(args[1:])
		if err != nil {
			return err
		}
		cfg, err := loadConfig(f)
		if err != nil {
			return err
		}
		autoUpdateEnv(cfg, os.Getenv) // report what the dashboard would do
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return doctor.Run(ctx, os.Stdout, cfg, version, buildCheck)
	case "pull":
		rest := args[1:]
		spec := ""
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			spec, rest = rest[0], rest[1:]
		}
		var path, host string
		var force bool
		if _, err := subFlags("config pull", rest, func(fs *flag.FlagSet) {
			fs.StringVar(&path, "config", "", "config path")
			fs.StringVar(&host, "host", "", "GitHub host")
			fs.BoolVar(&force, "force", false, "replace a hand-written config or switch repos")
		}); err != nil {
			return err
		}
		if path == "" {
			path = config.InstallPath(os.Getenv)
		}
		gh, err := newClient(ghapi.Options{Host: host, Version: version})
		if err != nil {
			return exitError{2, err}
		}
		return pullConfig(context.Background(), gh, spec, path, force, os.Stdout)
	case "init":
		var path string
		if _, err := subFlags("config init", args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&path, "config", "", "config path")
		}); err != nil {
			return err
		}
		if path == "" {
			path = config.InstallPath(os.Getenv)
		}
		if _, err := os.Stat(path); err == nil {
			return exitError{2, fmt.Errorf("%s already exists: edit it, then gh dillodash config check (config pull replaces it with your org's)", path)}
		}
		if err := config.WriteFileAtomic(path, []byte(config.Example()), 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s — set [reviews] repos, then: gh dillodash config check\n", path)
		return nil
	case "migrate":
		var src, dst string
		var force bool
		if _, err := subFlags("config migrate", args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&src, "from", "", "the legacy config.ini")
			fs.StringVar(&dst, "config", "", "config path")
			fs.BoolVar(&force, "force", false, "overwrite an existing config")
		}); err != nil {
			return err
		}
		if src == "" {
			_, legacy, _ := config.FindPath("", os.Getenv)
			src = legacy
		}
		if src == "" {
			return exitError{2, errors.New("config migrate: no legacy config.ini found; pass --from PATH")}
		}
		ini, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		out, notes, err := config.MigrateINI(ini)
		if err != nil {
			return err
		}
		if dst == "" {
			dst = config.InstallPath(os.Getenv)
		}
		if _, err := os.Stat(dst); err == nil && !force {
			return exitError{2, fmt.Errorf("%s already exists; pass --force to overwrite", dst)}
		}
		if err := backup(dst); err != nil {
			return err
		}
		if err := config.WriteFileAtomic(dst, []byte(out), 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s from %s\n", dst, src)
		for _, n := range notes {
			fmt.Println("  -", n)
		}
		return nil
	}
	return exitError{2, fmt.Errorf("config: unknown subcommand %q (see gh dillodash help)", args[0])}
}

// backup keeps a copy of path as path.bak before an overwrite, when the
// file exists.
func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return config.WriteFileAtomic(path+".bak", data, 0o600)
}

func fontCmd(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		if len(args) > 0 {
			fmt.Print(usage())
			return errHelp
		}
		return exitError{2, errors.New("font: install, check or license? (see gh dillodash help)")}
	}
	if args[0] == "license" || args[0] == "licence" {
		if _, err := subFlags("font license", args[1:], nil); err != nil {
			return err
		}
		fmt.Printf("%s %s is redistributed under these licences:\n\n", fonts.Family, fonts.Version)
		for _, l := range fonts.Licenses() {
			fmt.Printf("==== %s ====\n\n%s\n", l.Name, l.Data)
		}
		return nil
	}
	if args[0] != "install" && args[0] != "check" {
		return exitError{2, fmt.Errorf("font: unknown subcommand %q (see gh dillodash help)", args[0])}
	}
	var force bool
	if _, err := subFlags("font "+args[0], args[1:], func(fs *flag.FlagSet) {
		fs.BoolVar(&force, "force", false, "reinstall even when current")
	}); err != nil {
		return err
	}
	o := fonts.Options{Out: os.Stdout, Force: force}
	var res fonts.Result
	var err error
	switch args[0] {
	case "install":
		res, err = fonts.Install(o)
	case "check":
		res, err = fonts.Check(o)
	}
	if err != nil {
		return err
	}
	for _, p := range res.Installed {
		fmt.Println("current:", p)
	}
	for _, p := range res.Missing {
		fmt.Println("missing:", p)
	}
	for _, n := range res.Notes {
		fmt.Println(n)
	}
	for _, l := range fonts.SSHHint(o) {
		fmt.Println(l)
	}
	if args[0] == "check" && len(res.Missing) > 0 {
		return exitError{1, fmt.Errorf("%d font file(s) missing or outdated: gh dillodash font install", len(res.Missing))}
	}
	return nil
}

// pullConfig installs or refreshes the config from a repo: it fetches
// the file through the gh token, refuses one the parser rejects, and
// stamps the installed copy with its source so a bare `config pull`
// refreshes it and `config check` can tell whether it is current.
func pullConfig(ctx context.Context, gh ghapi.Client, spec, path string, force bool, w io.Writer) error {
	var src config.Source
	var installed []byte
	if data, err := os.ReadFile(path); err == nil {
		installed = data
	}
	old, stamped := config.ParseSource(installed)
	if spec == "" {
		if !stamped {
			return exitError{2, fmt.Errorf("%s was not pulled from a repo; say where: gh dillodash config pull OWNER/REPO", path)}
		}
		src = old
	} else {
		s, err := config.ParseSpec(spec)
		if err != nil {
			return exitError{2, err}
		}
		src = s
	}
	// never silently replace a config someone wrote by hand, or switch
	// the repo it follows: both need --force, and keep a .bak
	if len(installed) > 0 && !force {
		switch {
		case !stamped:
			return exitError{2, fmt.Errorf("%s exists and was not pulled from a repo; pass --force to replace it (a copy goes to %s.bak)", path, path)}
		case old.Spec() != src.Spec():
			return exitError{2, fmt.Errorf("%s was pulled from %s, not %s; pass --force to switch (a copy goes to %s.bak)", path, old.Spec(), src.Spec(), path)}
		}
	}
	data, sha, err := gh.FileContents(ctx, src.Repo, src.Path, "")
	if err != nil {
		return fmt.Errorf("could not read %s: %w", src.Spec(), err)
	}
	data = config.StripHeader(data)
	cfg, err := config.Parse(data, src.Spec(), config.Overrides{})
	if err != nil {
		return fmt.Errorf("refusing to install %s: it does not validate:\n%w", src.Spec(), err)
	}
	for _, warn := range cfg.Warnings {
		fmt.Fprintln(w, "warning:", warn)
	}
	if stamped && !force && old.Spec() == src.Spec() && old.SHA == sha {
		fmt.Fprintf(w, "%s is current: %s @ %s (pulled %s)\n", path, src.Spec(), model.ShortSHA(sha), old.At.Local().Format("2006-01-02 15:04"))
		return nil
	}
	src.SHA, src.At = sha, clock.Now()
	if len(installed) > 0 && (!stamped || old.Spec() != src.Spec()) {
		if err := backup(path); err != nil {
			return err
		}
	}
	if err := config.WriteFileAtomic(path, config.Stamp(src, data), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(w, "installed %s from %s @ %s\n", path, src.Spec(), model.ShortSHA(sha))
	fmt.Fprintln(w, "next: gh dillodash config check")
	return nil
}
