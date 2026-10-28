package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Choices for the enumerated keys.
var (
	IconChoices   = []string{"nerd", "unicode", "ascii"}
	ThemeChoices  = []string{"auto", "mono"}
	MotionChoices = []string{"full", "off"}
	MascotChoices = []string{"sprite", "ascii", "none"}
	MergeChoices  = []string{"merge", "squash", "rebase"}
)

// Load reads and parses the TOML file at path, applying the overrides.
func Load(path string, ov Overrides) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &Error{Path: path, Why: "not found"}
		}
		return nil, &Error{Path: path, Why: err.Error()}
	}
	return Parse(data, path, ov)
}

// Parse parses TOML config text. path only labels errors. The one
// impurity: the release cache default reads $XDG_CACHE_HOME and $HOME to
// expand "~"; tests use parseEnv with a fake environment.
func Parse(data []byte, path string, ov Overrides) (*Config, error) {
	return parseEnv(data, path, ov, os.Getenv)
}

// parseEnv is Parse with an explicit environment (XDG_CACHE_HOME, HOME).
func parseEnv(data []byte, path string, ov Overrides, env func(string) string) (*Config, error) {
	p := &parser{path: path, env: env, known: map[string][]string{}}
	cfg := p.parse(data, ov)
	if len(p.errs) > 0 {
		return nil, errors.Join(p.errs...)
	}
	cfg.Warnings = p.warns
	return cfg, nil
}

// parser collects errors and warnings while walking the decoded tables.
type parser struct {
	path  string
	env   func(string) string
	errs  []error
	warns []string
	known map[string][]string // key -> the sections that know it (for top-level hints)
}

func (p *parser) errf(where, format string, args ...any) {
	p.errs = append(p.errs, &Error{Path: p.path, Where: where, Why: fmt.Sprintf(format, args...)})
}

func (p *parser) warnf(where, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if where != "" {
		msg = where + ": " + msg
	}
	p.warns = append(p.warns, msg)
}

// table is one decoded TOML table and the keys read from it so far; done
// turns the keys never read into "unknown key" warnings.
type table struct {
	p    *parser
	name string // "[display]", "[workflows.release_pr]"; "" for the top level
	m    map[string]any
	used map[string]bool
}

func (p *parser) newTable(name string, m map[string]any) *table {
	if m == nil {
		m = map[string]any{}
	}
	return &table{p: p, name: name, m: m, used: map[string]bool{}}
}

// where is the error location of a key in this table.
func (t *table) where(key string) string {
	if t.name == "" {
		return key
	}
	return t.name + " " + key
}

// subName is the display name of a nested table.
func (t *table) subName(key string) string {
	if t.name == "" {
		return "[" + key + "]"
	}
	return t.name[:len(t.name)-1] + "." + key + "]"
}

func (t *table) get(key string) (any, bool) {
	t.used[key] = true
	if t.name != "" {
		sec := t.name
		t.p.known[key] = append(t.p.known[key], sec)
	}
	v, ok := t.m[key]
	return v, ok
}

func (t *table) errf(key, format string, args ...any) { t.p.errf(t.where(key), format, args...) }
func (t *table) warnf(key, format string, args ...any) {
	t.p.warnf(t.where(key), format, args...)
}

// sub reads a nested table; present is false when absent. A value of
// another type is an error and reads as absent.
func (t *table) sub(key string) (sub *table, present bool) {
	v, ok := t.get(key)
	if !ok {
		return t.p.newTable(t.subName(key), nil), false
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.errf(key, "expected a table, got %s", typeName(v))
		return t.p.newTable(t.subName(key), nil), false
	}
	return t.p.newTable(t.subName(key), m), true
}

func (t *table) str(key, def string) string {
	v, ok := t.get(key)
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		t.errf(key, "expected a string, got %s", typeName(v))
		return def
	}
	return s
}

// int reads an integer; non-negative unless allowNeg.
func (t *table) int(key string, def int) int {
	v, ok := t.get(key)
	if !ok {
		return def
	}
	n, ok := v.(int64)
	if !ok {
		t.errf(key, "expected an integer, got %s", typeName(v))
		return def
	}
	if n < 0 {
		t.errf(key, "must not be negative, got %d", n)
		return def
	}
	return int(n)
}

func (t *table) boolean(key string, def bool) bool {
	v, ok := t.get(key)
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		t.errf(key, "expected a boolean (true or false), got %s", typeName(v))
		return def
	}
	return b
}

// strs reads an array of strings; absent returns a copy of def.
func (t *table) strs(key string, def []string) (out []string, present bool) {
	v, ok := t.get(key)
	if !ok {
		return append([]string(nil), def...), false
	}
	arr, ok := v.([]any)
	if !ok {
		t.errf(key, "expected an array of strings, got %s", typeName(v))
		return append([]string(nil), def...), false
	}
	out = make([]string, 0, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			t.errf(key, "entry %d: expected a string, got %s", i+1, typeName(e))
			continue
		}
		out = append(out, s)
	}
	return out, true
}

// hm reads an "HH:MM" string.
func (t *table) hm(key string, def clock.HM) clock.HM {
	s, ok := t.get(key)
	if !ok {
		return def
	}
	str, ok := s.(string)
	if !ok {
		t.errf(key, "expected a string like \"23:00\", got %s", typeName(s))
		return def
	}
	v, err := clock.ParseHM(str)
	if err != nil {
		t.errf(key, "%q is not HH:MM (00:00-23:59)", str)
		return def
	}
	return v
}

// tz reads an IANA timezone name.
func (t *table) tz(key, def string) string {
	s := t.str(key, def)
	if strings.TrimSpace(s) == "" {
		t.errf(key, "must be an IANA timezone like \"America/New_York\"")
		return def
	}
	if _, err := time.LoadLocation(s); err != nil {
		t.errf(key, "%q is not an IANA timezone (like \"America/New_York\")", s)
		return def
	}
	return s
}

// choice reads a string that must be one of the choices.
func (t *table) choice(key, def string, choices []string) string {
	s := t.str(key, def)
	if !contains(choices, s) {
		t.errf(key, "%q is not one of %s", s, strings.Join(choices, ", "))
		return def
	}
	return s
}

// nonEmpty reads a string that must not be blank.
func (t *table) nonEmpty(key, def string) string {
	s := t.str(key, def)
	if strings.TrimSpace(s) == "" {
		t.errf(key, "must not be empty")
		return def
	}
	return s
}

// repos reads an array of "owner/name" strings, deduped (case-insensitive,
// first spelling wins). present is false when the key is absent.
func (t *table) repos(key string) (repos []model.Repo, present bool) {
	raw, present := t.strs(key, nil)
	if !present {
		return nil, false
	}
	return t.p.parseRepos(t.where(key), raw), true
}

func (p *parser) parseRepos(where string, raw []string) []model.Repo {
	var out []model.Repo
	seen := map[string]bool{}
	for _, s := range raw {
		r, ok := model.ParseRepo(s)
		if !ok {
			p.errf(where, "%q is not \"owner/name\"", s)
			continue
		}
		k := strings.ToLower(r.String())
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// done warns about every key that was never read, with a suggestion.
func (t *table) done() {
	keys := make([]string, 0, len(t.m))
	for k := range t.m {
		if !t.used[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	candidates := make([]string, 0, len(t.used))
	for k := range t.used {
		candidates = append(candidates, k)
	}
	sort.Strings(candidates)
	for _, k := range keys {
		_, isTable := t.m[k].(map[string]any)
		kind := "key"
		if isTable {
			kind = "table"
		}
		hint := ""
		switch {
		case t.name == "" && isTable:
			// a top-level table: the closest known table
			if s := suggest(k, candidates); s != "" {
				hint = fmt.Sprintf(" (did you mean %s?)", t.subName(s))
			}
		case t.name == "":
			// a top-level scalar: the section that knows the key
			if s := t.p.suggestKnown(k); s != "" {
				hint = fmt.Sprintf(" (did you mean %s?)", s)
			}
		case isTable:
			if s := suggest(k, candidates); s != "" {
				hint = fmt.Sprintf(" (did you mean %s?)", t.subName(s))
			}
		default:
			if s := suggest(k, candidates); s != "" {
				hint = fmt.Sprintf(" (did you mean %s?)", s)
			}
		}
		where := t.where(k)
		if t.name == "" && isTable {
			where = "[" + k + "]"
		}
		t.p.warnf(where, "unknown %s, ignored%s", kind, hint)
	}
}

// suggestKnown finds "[section] key" for a key that belongs in a section:
// an exact match first, then the closest by edit distance.
func (p *parser) suggestKnown(key string) string {
	if secs := p.known[key]; len(secs) > 0 {
		return secs[0] + " " + key
	}
	all := make([]string, 0, len(p.known))
	for k := range p.known {
		all = append(all, k)
	}
	sort.Strings(all)
	if s := suggest(key, all); s != "" {
		return p.known[s][0] + " " + s
	}
	return ""
}

// ---------------------------------------------------------------------
// the schema
// ---------------------------------------------------------------------

func (p *parser) parse(data []byte, ov Overrides) *Config {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			p.errf("", "invalid TOML at line %d, column %d: %s", row, col, de.Error())
		} else {
			p.errf("", "invalid TOML: %s", err.Error())
		}
		return nil
	}
	cfg := Default()
	cfg.Path = p.path
	top := p.newTable("", root)

	p.parseGitHub(top, cfg)
	p.parseReviews(top, cfg, ov)
	p.parseDisplay(top, cfg, ov)
	p.parseBots(top, cfg)

	// [repos."owner/name"] is reserved for per-repo settings
	if _, ok := top.get("repos"); ok {
		p.warnf("[repos]", "reserved for a future release, ignored")
	}

	wt, _ := top.sub("workflows")
	p.parseTerraformApprove(wt, cfg)
	p.parseMaintenanceWindow(wt, cfg)
	p.parseReleasePR(wt, cfg)
	wt.done()

	top.done()
	return cfg
}

func (p *parser) parseGitHub(top *table, cfg *Config) {
	t, _ := top.sub("github")
	// unset: "" and the client resolves $GH_HOST, gh's default host, then
	// github.com; set, it must be a bare hostname
	host := strings.TrimSpace(t.str("host", ""))
	if strings.Contains(host, "/") || strings.Contains(host, ":") {
		t.errf("host", "%q must be a bare hostname like \"github.com\"", host)
	} else {
		cfg.GitHub.Host = host
	}
	cfg.GitHub.Login = strings.TrimSpace(t.str("login", ""))
	t.done()
}

func (p *parser) parseReviews(top *table, cfg *Config, ov Overrides) {
	t, _ := top.sub("reviews")
	repos, present := t.repos("repos")
	cfg.Reviews.CIRecheckSecs = t.int("ci_recheck_secs", cfg.Reviews.CIRecheckSecs)
	t.done()

	if len(ov.Repos) > 0 {
		repos = p.parseRepos("--repo", ov.Repos)
		present = true
	}
	if !present {
		if !hasErrorAt(p.errs, t.where("repos")) {
			t.errf("repos", "required: at least one \"owner/name\" (or pass --repo)")
		}
	} else if len(repos) == 0 && !hasErrorAt(p.errs, t.where("repos"), "--repo") {
		t.errf("repos", "must list at least one \"owner/name\"")
	}
	cfg.Reviews.Repos = repos
}

func (p *parser) parseDisplay(top *table, cfg *Config, ov Overrides) {
	t, _ := top.sub("display")
	d := &cfg.Display
	d.RefreshSecs = t.int("refresh_secs", d.RefreshSecs)
	if d.RefreshSecs < MinRefreshSecs {
		t.warnf("refresh_secs", "%d is below the floor of %d, using %d", d.RefreshSecs, MinRefreshSecs, MinRefreshSecs)
		d.RefreshSecs = MinRefreshSecs
	}
	d.Icons = t.choice("icons", d.Icons, IconChoices)
	d.Theme = t.choice("theme", d.Theme, ThemeChoices)
	d.Motion = t.choice("motion", d.Motion, MotionChoices)
	d.Mascot = t.choice("mascot", d.Mascot, MascotChoices)
	t.done()

	if ov.RefreshSecs != nil {
		d.RefreshSecs = *ov.RefreshSecs
		if d.RefreshSecs < MinRefreshSecs {
			p.warnf("--refresh", "%d is below the floor of %d, using %d", d.RefreshSecs, MinRefreshSecs, MinRefreshSecs)
			d.RefreshSecs = MinRefreshSecs
		}
	}
	override := func(flag, v string, choices []string, dst *string) {
		if v == "" {
			return
		}
		if !contains(choices, v) {
			p.errf(flag, "%q is not one of %s", v, strings.Join(choices, ", "))
			return
		}
		*dst = v
	}
	override("--icons", ov.Icons, IconChoices, &d.Icons)
	override("--theme", ov.Theme, ThemeChoices, &d.Theme)
	override("--motion", ov.Motion, MotionChoices, &d.Motion)
	if ov.Mono {
		d.Theme = "mono"
	}
}

func (p *parser) parseBots(top *table, cfg *Config) {
	t, _ := top.sub("bots")
	raw, _ := t.strs("logins", cfg.Bots.Logins)
	var out []string
	seen := map[string]bool{}
	for i, s := range raw {
		l := strings.ToLower(strings.TrimSpace(s))
		if l == "" {
			t.errf("logins", "entry %d is empty", i+1)
			continue
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	cfg.Bots.Logins = out
	t.done()
}

// workflowEnabled reads a workflow table's enabled flag; a table without
// one is off with a warning.
func (p *parser) workflowEnabled(wt *table, key string) (t *table, enabled bool) {
	t, present := wt.sub(key)
	if !present {
		return t, false
	}
	if _, ok := t.m["enabled"]; !ok {
		t.warnf("enabled", "missing, the workflow stays off (add enabled = true to turn it on)")
	}
	return t, t.boolean("enabled", false)
}

func (p *parser) parseTerraformApprove(wt *table, cfg *Config) {
	t, enabled := p.workflowEnabled(wt, "terraform_approve")
	w := DefaultTerraformApprove()
	if repos, ok := t.repos("repos"); ok && len(repos) > 0 {
		w.Repos = repos
	}
	w.DigestBot = strings.ToLower(t.nonEmpty("digest_bot", w.DigestBot))
	w.PlanWorkflow = t.nonEmpty("plan_workflow", w.PlanWorkflow)
	w.GateContext = t.nonEmpty("gate_context", w.GateContext)
	w.GateDonePrefixes, _ = t.strs("gate_done_prefixes", w.GateDonePrefixes)
	w.ApproveCommand = t.nonEmpty("approve_command", w.ApproveCommand)
	w.ConfirmDestroysFlag = t.nonEmpty("confirm_destroys_flag", w.ConfirmDestroysFlag)
	w.OutsideWindowFlag = t.nonEmpty("outside_window_flag", w.OutsideWindowFlag)
	w.OutsideWindowMinReason = t.int("outside_window_min_reason", w.OutsideWindowMinReason)

	if raw, ok := t.get("ceremony_window"); ok {
		m, isTable := raw.(map[string]any)
		if !isTable {
			t.errf("ceremony_window", "expected a table like { timezone = \"America/New_York\", from = \"23:00\", to = \"23:30\" }, got %s", typeName(raw))
		} else {
			ct := p.newTable(t.subName("ceremony_window"), m)
			var missing []string
			for _, k := range []string{"timezone", "from", "to"} {
				if _, ok := m[k]; !ok {
					missing = append(missing, k)
				}
			}
			cw := &CeremonyWindow{}
			cw.Timezone = ct.tz("timezone", "America/New_York")
			cw.From = ct.hm("from", clock.HM{H: 23, M: 0})
			cw.To = ct.hm("to", clock.HM{H: 23, M: 28})
			ct.done()
			if len(missing) > 0 {
				t.errf("ceremony_window", "needs timezone, from and to (missing %s)", strings.Join(missing, ", "))
			} else {
				if !cw.From.Before(cw.To) {
					t.warnf("ceremony_window", "from %s is not before to %s; the window is same-day and will never be open", cw.From, cw.To)
				}
				w.CeremonyWindow = cw
			}
		}
	}
	t.done()

	if !enabled {
		return
	}
	if len(w.Repos) == 0 {
		w.Repos = append([]model.Repo(nil), cfg.Reviews.Repos...)
	}
	cfg.TerraformApprove = w
}

func (p *parser) parseMaintenanceWindow(wt *table, cfg *Config) {
	t, enabled := p.workflowEnabled(wt, "maintenance_window")
	w := DefaultMaintenanceWindow()
	if s := strings.TrimSpace(t.str("manifest_repo", "")); s != "" {
		r, ok := model.ParseRepo(s)
		if !ok {
			t.errf("manifest_repo", "%q is not \"owner/name\"", s)
		} else {
			w.ManifestRepo = r
		}
	}
	w.Timezone = t.tz("timezone", w.Timezone)
	w.TZLabel = strings.TrimSpace(t.str("tz_label", ""))
	w.Cutoff = t.hm("cutoff", w.Cutoff)
	w.Start = t.hm("start", w.Start)
	w.ReleaseFrom = t.hm("release_from", w.ReleaseFrom)
	w.End = t.hm("end", w.End)
	if !(w.Cutoff.Before(w.ReleaseFrom) && w.ReleaseFrom.Before(w.Start) && w.Start.Before(w.End)) {
		t.warnf("cutoff, release_from, start, end", "expected cutoff < release_from < start < end, got %s, %s, %s, %s", w.Cutoff, w.ReleaseFrom, w.Start, w.End)
	}
	w.QueuedLabel = t.nonEmpty("queued_label", w.QueuedLabel)
	w.ScheduledLabel = t.nonEmpty("scheduled_label", w.ScheduledLabel)
	w.RolledLabel = t.nonEmpty("rolled_label", w.RolledLabel)
	w.ManifestLabel = t.nonEmpty("manifest_label", w.ManifestLabel)
	w.ManifestTitle = t.nonEmpty("manifest_title", w.ManifestTitle)
	if !strings.Contains(w.ManifestTitle, nightPlaceholder) {
		t.errf("manifest_title", "%q must contain {night}", w.ManifestTitle)
		w.ManifestTitle = DefaultMaintenanceWindow().ManifestTitle
	}
	w.ConductorBot = strings.ToLower(t.nonEmpty("conductor_bot", w.ConductorBot))
	w.Team = strings.TrimSpace(t.str("team", ""))
	if w.Team != "" {
		if _, ok := model.ParseRepo(w.Team); !ok {
			t.errf("team", "%q is not \"org/team\"", w.Team)
		}
	}
	w.AttendCommand = t.nonEmpty("attend_command", w.AttendCommand)
	w.ReleaseCommand = t.nonEmpty("release_command", w.ReleaseCommand)
	w.CancelCommand = t.nonEmpty("cancel_command", w.CancelCommand)
	t.done()

	if !enabled {
		return
	}
	if w.ManifestRepo.IsZero() && len(cfg.Reviews.Repos) > 0 {
		w.ManifestRepo = cfg.Reviews.Repos[0]
		if len(cfg.Reviews.Repos) > 1 {
			t.warnf("manifest_repo", "unset with %d review repos, using the first (%s)", len(cfg.Reviews.Repos), w.ManifestRepo)
		}
	}
	cfg.MaintenanceWindow = w
}

var placeholderRE = regexp.MustCompile(`\{[^{}]*\}`)

func (p *parser) parseReleasePR(wt *table, cfg *Config) {
	t, enabled := p.workflowEnabled(wt, "release_pr")
	w := DefaultReleasePR()

	if raw, ok := t.get("repos"); ok {
		switch v := raw.(type) {
		case string:
			if strings.ToLower(strings.TrimSpace(v)) != "auto" {
				t.errf("repos", "%q must be \"auto\" or a list of \"owner/name\"", v)
			}
		case []any:
			raws := make([]string, 0, len(v))
			for i, e := range v {
				s, ok := e.(string)
				if !ok {
					t.errf("repos", "entry %d: expected a string, got %s", i+1, typeName(e))
					continue
				}
				raws = append(raws, s)
			}
			repos := p.parseRepos(t.where("repos"), raws)
			if len(v) == 0 {
				t.errf("repos", "must be \"auto\" or a non-empty list of \"owner/name\"")
			}
			w.Auto = false
			w.Repos = repos
		default:
			t.errf("repos", "must be \"auto\" or a list of \"owner/name\", got %s", typeName(raw))
		}
	}
	w.Org = strings.TrimSpace(t.str("org", ""))
	w.Ruleset = t.nonEmpty("ruleset", w.Ruleset)
	w.DiscoverTTLSecs = t.int("discover_ttl_secs", w.DiscoverTTLSecs)
	cache, cachePresent := t.get("cache")
	w.Cache = DefaultReleaseCache
	if cachePresent {
		if s, ok := cache.(string); ok {
			if strings.TrimSpace(s) == "" {
				t.errf("cache", "must not be empty")
			} else {
				w.Cache = s
			}
		} else {
			t.errf("cache", "expected a string, got %s", typeName(cache))
		}
	}
	w.ProductionBranch = t.nonEmpty("production_branch", w.ProductionBranch)
	w.StagingBranch = t.nonEmpty("staging_branch", w.StagingBranch)
	w.ReleasePrefix = t.nonEmpty("release_prefix", w.ReleasePrefix)
	w.HotfixPrefix = t.nonEmpty("hotfix_prefix", w.HotfixPrefix)
	if w.ReleasePrefix == w.HotfixPrefix {
		t.errf("hotfix_prefix", "%q must differ from release_prefix", w.HotfixPrefix)
	}
	w.GoTagPrefix = t.nonEmpty("go_tag_prefix", w.GoTagPrefix)
	w.HoldTagPrefix = t.nonEmpty("hold_tag_prefix", w.HoldTagPrefix)
	w.ListOtherPRs = t.boolean("list_other_prs", w.ListOtherPRs)
	w.ReleaseNowCommand = t.nonEmpty("release_now_command", w.ReleaseNowCommand)
	w.ReleaseNowMinReason = t.int("release_now_min_reason", w.ReleaseNowMinReason)
	w.ApprovalQueuesForWindow = t.boolean("approval_queues_for_window", false)
	w.MergeMethod = t.choice("merge_method", w.MergeMethod, MergeChoices)
	w.MergedKeepSecs = t.int("merged_keep_secs", w.MergedKeepSecs)
	w.DeployStuckSecs = t.int("deploy_stuck_secs", w.DeployStuckSecs)
	w.DeployRefreshSecs = t.int("deploy_refresh_secs", w.DeployRefreshSecs)
	w.DeployApprovalComment = t.nonEmpty("deploy_approval_comment", w.DeployApprovalComment)
	for _, ph := range placeholderRE.FindAllString(w.DeployApprovalComment, -1) {
		switch ph {
		case "{repo}", "{number}", "{sha}":
		default:
			t.errf("deploy_approval_comment", "unknown placeholder %s (allowed: {repo} {number} {sha})", ph)
		}
	}
	t.done()

	if !enabled {
		return
	}
	if w.Org == "" {
		owners := map[string]bool{}
		var list []string
		for _, r := range cfg.Reviews.Repos {
			k := strings.ToLower(r.Owner)
			if !owners[k] {
				owners[k] = true
				list = append(list, r.Owner)
			}
		}
		switch len(list) {
		case 0:
			// no repos: that error is already reported by [reviews]
		case 1:
			w.Org = list[0]
		default:
			t.errf("org", "unset and the review repos span several owners (%s); set it", strings.Join(list, ", "))
		}
	}
	w.Cache = p.expandCache(w.Cache, w.Org)
	if w.ApprovalQueuesForWindow && cfg.MaintenanceWindow == nil {
		t.errf("approval_queues_for_window", "true needs [workflows.maintenance_window] enabled = true")
	}
	cfg.ReleasePR = w
}

// expandCache resolves {org}, "~" and, for the default, $XDG_CACHE_HOME.
func (p *parser) expandCache(cache, org string) string {
	if cache == DefaultReleaseCache {
		if xdg := p.env("XDG_CACHE_HOME"); xdg != "" {
			cache = filepath.Join(xdg, "dillodash", "release-repos-{org}.json")
		}
	}
	cache = strings.ReplaceAll(cache, "{org}", org)
	if cache == "~" || strings.HasPrefix(cache, "~/") {
		if home := homeDir(p.env); home != "" {
			cache = filepath.Join(home, strings.TrimPrefix(cache, "~"))
		}
	}
	return cache
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "a table"
	case time.Time, toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return "a date/time"
	}
	return fmt.Sprintf("%T", v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hasErrorAt reports whether an error was already recorded at one of the
// locations (so a follow-up "must list at least one" is not piled on).
func hasErrorAt(errs []error, wheres ...string) bool {
	for _, err := range errs {
		var e *Error
		if errors.As(err, &e) && contains(wheres, e.Where) {
			return true
		}
	}
	return false
}

// suggest picks the closest candidate to key by edit distance, or "" when
// nothing is close. Case and -/_ differences count for nothing, and a
// candidate that extends the key (merge -> merge_method) counts as one
// edit away.
func suggest(key string, candidates []string) string {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "_") }
	k := norm(key)
	best, bestD := "", -1
	for _, c := range candidates {
		nc := norm(c)
		d := levenshtein(k, nc)
		if len(k) >= 4 && len(nc) >= 4 && (strings.HasPrefix(nc, k) || strings.HasPrefix(k, nc)) && d > 1 {
			d = 1
		}
		if bestD < 0 || d < bestD {
			best, bestD = c, d
		}
	}
	limit := len(k) / 3
	if limit < 2 {
		limit = 2
	}
	if best == "" || bestD > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
