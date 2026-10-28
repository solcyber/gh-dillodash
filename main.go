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
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/solcyber/gh-dillodash/internal/amos"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/demo"
	"github.com/solcyber/gh-dillodash/internal/doctor"
	"github.com/solcyber/gh-dillodash/internal/fonts"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/markdown"
	"github.com/solcyber/gh-dillodash/internal/scenes"
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
  gh dillodash config pull [OWNER/REPO[:PATH]]
                                  install (or refresh) the config from a repo you can read
  gh dillodash config init        write a starter config
  gh dillodash config migrate     convert a pr-dash config.ini to config.toml
  gh dillodash font install|check install the bundled Nerd Font (icons = "nerd")
  gh dillodash glyph-test         print every glyph and the palette (checks the terminal font)
  gh dillodash amos               say hello to Amos
  gh dillodash version

flags:
  --config PATH      config file (default: ~/.config/dillodash/config.toml)
  --repo OWNER/NAME  review this repo (repeatable; replaces [reviews] repos)
  --refresh SECS     auto-refresh interval (floor 5)
  --icons SET        nerd | unicode | ascii
  --theme NAME       auto | mono
  --motion MODE      full | off
  --mono             no colors, attributes only (also: NO_COLOR)
  --demo             open the dashboard on an offline fixture, no GitHub needed
`

type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(s string) error { *r = append(*r, s); return nil }

func main() {
	bi, ok := debug.ReadBuildInfo()
	version = resolveVersion(version, bi, ok)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dillodash:", err)
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

// run dispatches the command line; nil means exit 0.
func run(args []string) error {
	err := dispatch(args)
	if errors.Is(err, errHelp) {
		return nil
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
			glyphs.Use(glyphs.Set(flagValue(args[1:], "--icons", "nerd")))
			theme.Setup(colorprofile.Detect(os.Stdout, os.Environ()), false)
			glyphs.Report(os.Stdout, theme.Palette)
			return nil
		case "config":
			return configCmd(args[1:])
		case "font":
			return fontCmd(args[1:])
		}
	}
	return dashboard(args)
}

func flagValue(args []string, name, def string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return def
}

type flags struct {
	demo       bool
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
			return nil, exitError{2, fmt.Errorf("no config.toml found; %s is the pr-dash format. Run: gh dillodash config migrate", legacy)}
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
	set := glyphs.Set(cfg.Display.Icons)
	glyphs.Use(set)
	switch set {
	case glyphs.ASCII:
		markdown.Glyphs = markdown.ASCIIGlyphs
	case glyphs.Unicode:
		g := markdown.NerdGlyphs
		g.Shield, g.Window, g.Warn = glyphs.G("shield"), glyphs.G("window"), glyphs.G("warn")
		markdown.Glyphs = g
	default:
		markdown.Glyphs = markdown.NerdGlyphs
	}
	mono := cfg.Display.Theme == "mono" || os.Getenv("NO_COLOR") != ""
	theme.Setup(colorprofile.Detect(os.Stdout, os.Environ()), mono)
	fx.Motion = cfg.Display.Motion
	scenes.Mascot = cfg.Display.Mascot
}

func dashboard(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if f.demo {
		return runDemo(ctx, f)
	}
	cfg, err := loadConfig(f)
	if err != nil {
		return err
	}
	gh, err := ghapi.New(ghapi.Options{Host: cfg.GitHub.Host, Version: version})
	if err != nil {
		return exitError{2, fmt.Errorf("%w\nSign in first: gh auth login", err)}
	}
	// fail fast (and loudly, outside the TUI) if the token is bad, and
	// warn if gh's active account is not the one the config expects
	me, err := gh.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("GitHub auth failed: %w (gh auth status shows the account)", err)
	}
	if cfg.GitHub.Login != "" && !strings.EqualFold(cfg.GitHub.Login, me) {
		fmt.Fprintf(os.Stderr, "warning: config expects login %q but gh is signed in as %q\n", cfg.GitHub.Login, me)
		fmt.Fprintln(os.Stderr, "actions will be performed as", me, "(gh auth switch changes it); ctrl-c now to abort")
		time.Sleep(3 * time.Second)
	}
	setupDisplay(cfg)
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		return exitError{2, err}
	}
	return ui.Run(ctx, ui.Options{
		Config:    cfg,
		GH:        gh,
		Workflows: wf,
		Me:        me,
		Host:      gh.Host(),
		Version:   version,
	})
}

// runDemo opens the dashboard on the offline fixture. The display flags
// apply; --config and --repo have nothing to override.
func runDemo(ctx context.Context, f flags) error {
	cfg := demo.Config()
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
	setupDisplay(cfg)
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		return err
	}
	return ui.Run(ctx, ui.Options{Config: cfg, GH: demo.Client(), Workflows: wf, Me: demo.Me, Host: "github.com", Version: version + " demo"})
}

func configCmd(args []string) error {
	if len(args) == 0 {
		return exitError{2, errors.New("config: check, pull, init or migrate?")}
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
		return doctor.Run(context.Background(), os.Stdout, cfg, version)
	case "pull":
		rest := args[1:]
		spec := ""
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			spec, rest = rest[0], rest[1:]
		}
		path := flagValue(rest, "--config", "")
		if path == "" {
			path = config.DefaultPath(os.Getenv)
		}
		gh, err := ghapi.New(ghapi.Options{Host: flagValue(rest, "--host", ""), Version: version})
		if err != nil {
			return exitError{2, fmt.Errorf("%w\nSign in first: gh auth login", err)}
		}
		return pullConfig(context.Background(), gh, spec, path, hasFlag(rest, "--force"), os.Stdout)
	case "init":
		path := config.DefaultPath(os.Getenv)
		if _, err := os.Stat(path); err == nil {
			return exitError{2, fmt.Errorf("%s already exists", path)}
		}
		if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(config.Example()), 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s — set [reviews] repos, then: gh dillodash config check\n", path)
		return nil
	case "migrate":
		src := flagValue(args[1:], "--from", "")
		if src == "" {
			_, legacy, _ := config.FindPath("", os.Getenv)
			src = legacy
		}
		if src == "" {
			return exitError{2, errors.New("config migrate: no pr-dash config.ini found; pass --from PATH")}
		}
		ini, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		out, notes, err := config.MigrateINI(ini)
		if err != nil {
			return err
		}
		dst := config.DefaultPath(os.Getenv)
		if _, err := os.Stat(dst); err == nil && !hasFlag(args, "--force") {
			return exitError{2, fmt.Errorf("%s already exists; pass --force to overwrite", dst)}
		}
		if err := os.MkdirAll(dirOf(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(out), 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s from %s\n", dst, src)
		for _, n := range notes {
			fmt.Println("  -", n)
		}
		return nil
	}
	return exitError{2, fmt.Errorf("config: unknown subcommand %q", args[0])}
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		return path[:i]
	}
	return "."
}

func fontCmd(args []string) error {
	if len(args) == 0 {
		return exitError{2, errors.New("font: install or check?")}
	}
	o := fonts.Options{Out: os.Stdout, Force: hasFlag(args, "--force")}
	var res fonts.Result
	var err error
	switch args[0] {
	case "install":
		res, err = fonts.Install(o)
	case "check":
		res, err = fonts.Check(o)
	default:
		return exitError{2, fmt.Errorf("font: unknown subcommand %q", args[0])}
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
	if spec == "" {
		s, ok := config.ParseSource(installed)
		if !ok {
			return exitError{2, fmt.Errorf("%s was not pulled from a repo; say where: gh dillodash config pull OWNER/REPO", path)}
		}
		src = s
	} else {
		s, err := config.ParseSpec(spec)
		if err != nil {
			return exitError{2, err}
		}
		src = s
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
	if old, ok := config.ParseSource(installed); ok && !force && old.Spec() == src.Spec() && old.SHA == sha {
		fmt.Fprintf(w, "%s is current: %s @ %s (pulled %s)\n", path, src.Spec(), short(sha), old.At.Local().Format("2006-01-02 15:04"))
		return nil
	}
	src.SHA, src.At = sha, time.Now()
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, config.Stamp(src, data), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(w, "installed %s from %s @ %s\n", path, src.Spec(), short(sha))
	fmt.Fprintln(w, "next: gh dillodash config check")
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
