package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// MigrateINI converts a pr-dash config.ini into dillodash TOML. The old
// format:
//
//	[defaults]   repo = owner/name, user, refresh_secs, icons, theme, motion
//	[user.NAME]  token | token_file | token_command   (auth is now gh's job)
//	[releases]   repos = auto | none | a, b ; discover_ttl_secs ; cache
//
// The old build ran the terraform-approve, maintenance-window and
// release-PR protocols unconditionally, so every workflow table comes out
// enabled = true (release_pr off for repos = none) under a comment telling
// the user to switch off the ones their repos do not speak. notes explain
// what was dropped or changed. The output is parsed before it is returned,
// so it loads cleanly.
func MigrateINI(ini []byte) (toml string, notes []string, err error) {
	sections, order, err := parseINI(ini)
	if err != nil {
		return "", nil, err
	}
	get := func(sec, key string) (string, bool) {
		if m, ok := sections[sec]; ok {
			v, ok := m[key]
			return strings.TrimSpace(v), ok
		}
		return "", false
	}
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	// [defaults] repo
	repoStr, ok := get("defaults", "repo")
	if !ok || repoStr == "" {
		return "", nil, &Error{Where: "[defaults] repo", Why: "missing; the old build fell back to a built-in repo, dillodash needs it spelled out"}
	}
	repo, ok := model.ParseRepo(repoStr)
	if !ok {
		return "", nil, &Error{Where: "[defaults] repo", Why: fmt.Sprintf("%q is not \"owner/name\"", repoStr)}
	}
	note("[defaults] repo = %s became [reviews] repos = [%q]", repo, repo.String())

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# dillodash config, migrated from a pr-dash config.ini.\n")
	w("#\n")
	w("# The old build always ran the terraform-approve, maintenance-window and\n")
	w("# release-PR protocols, so each workflow below is enabled = true. Set\n")
	w("# enabled = false on any protocol your repos do not speak.\n\n")

	// [github]
	w("[github]\nhost = \"github.com\"\n")
	if user, ok := get("defaults", "user"); ok && user != "" {
		w("login = %s\n", tomlString(user))
		note("[defaults] user = %s became [github] login (dillodash warns when gh is signed in as someone else)", user)
	}
	w("\n")

	// [reviews]
	w("[reviews]\nrepos = [%s]\n\n", tomlString(repo.String()))

	// [display]
	w("[display]\n")
	if v, ok := get("defaults", "refresh_secs"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			w("refresh_secs = %d\n", n)
		} else {
			note("[defaults] refresh_secs = %q is not a number, dropped", v)
		}
	}
	for _, k := range []string{"icons", "theme", "motion"} {
		if v, ok := get("defaults", k); ok && v != "" {
			w("%s = %s\n", k, tomlString(v))
		}
	}
	for k := range sections["defaults"] {
		switch k {
		case "repo", "user", "refresh_secs", "icons", "theme", "motion":
		default:
			note("[defaults] %s: unknown key, dropped", k)
		}
	}
	w("\n")

	// [bots]
	w("[bots]\n")
	w("# The old build hard-wired a third bot login of its own; add it here if\n")
	w("# your repos have one.\n")
	w("logins = [\"github-actions\", \"dependabot\"]\n\n")

	// [user.*] dropped
	var users []string
	for _, s := range order {
		if strings.HasPrefix(s, "user.") {
			users = append(users, strings.TrimPrefix(s, "user."))
		}
	}
	if len(users) > 0 {
		note("[user.%s] dropped: dillodash authenticates through the gh CLI; run `gh auth login` (and `gh auth switch` to change accounts)", strings.Join(users, "], [user."))
	}

	// workflows
	w("[workflows.terraform_approve]\nenabled = true\n")
	w("# repos default to the [reviews] repos; see config.example.toml for every key.\n\n")

	w("[workflows.maintenance_window]\nenabled = true\n")
	w("manifest_repo = %s\n\n", tomlString(repo.String()))

	w("[workflows.release_pr]\n")
	releases := "auto"
	if v, ok := get("releases", "repos"); ok && v != "" {
		releases = v
	}
	switch strings.ToLower(releases) {
	case "none":
		w("enabled = false\n")
		note("[releases] repos = none became [workflows.release_pr] enabled = false")
	case "auto":
		w("enabled = true\nrepos = \"auto\"\n")
		w("org = %s\n", tomlString(repo.Owner))
		note("[releases] repos = auto kept: release repos are discovered in org %s by ruleset", repo.Owner)
	default:
		var list []string
		for _, r := range strings.Split(strings.ReplaceAll(releases, ";", ","), ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !strings.Contains(r, "/") {
				r = repo.Owner + "/" + r
			}
			list = append(list, tomlString(r))
		}
		if len(list) == 0 {
			return "", nil, &Error{Where: "[releases] repos", Why: "auto, none, or a comma list"}
		}
		w("enabled = true\nrepos = [%s]\n", strings.Join(list, ", "))
		w("org = %s\n", tomlString(repo.Owner))
		note("[releases] repos list became [workflows.release_pr] repos, each prefixed with %s/", repo.Owner)
	}
	if v, ok := get("releases", "discover_ttl_secs"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			w("discover_ttl_secs = %d\n", n)
		} else {
			note("[releases] discover_ttl_secs = %q is not a number, dropped", v)
		}
	}
	if v, ok := get("releases", "cache"); ok && v != "" {
		w("cache = %s\n", tomlString(v))
		note("[releases] cache kept as given; the new default is %s", DefaultReleaseCache)
	}
	for k := range sections["releases"] {
		switch k {
		case "repos", "discover_ttl_secs", "cache":
		default:
			note("[releases] %s: unknown key, dropped", k)
		}
	}
	for _, s := range order {
		switch {
		case s == "defaults", s == "releases", strings.HasPrefix(s, "user."):
		default:
			note("[%s]: unknown section, dropped", s)
		}
	}
	sort.Strings(notes[1:]) // keep the repo note first, the rest stable

	out := b.String()
	if _, err := Parse([]byte(out), "migrated config", Overrides{}); err != nil {
		return "", nil, fmt.Errorf("migration produced an invalid config: %w", err)
	}
	return out, notes, nil
}

// parseINI is the subset of configparser pr-dash used: [sections],
// key = value (or key: value), ; and # comment lines, no interpolation,
// keys lower-cased, later duplicates winning. Inline comments are value
// text, as in configparser's default.
func parseINI(data []byte) (sections map[string]map[string]string, order []string, err error) {
	sections = map[string]map[string]string{}
	cur := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			if !strings.HasSuffix(line, "]") {
				return nil, nil, &Error{Where: fmt.Sprintf("line %d", i+1), Why: "unterminated section header"}
			}
			cur = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := sections[cur]; !ok {
				sections[cur] = map[string]string{}
				order = append(order, cur)
			}
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep <= 0 {
			return nil, nil, &Error{Where: fmt.Sprintf("line %d", i+1), Why: "expected key = value"}
		}
		if cur == "" {
			return nil, nil, &Error{Where: fmt.Sprintf("line %d", i+1), Why: "key before any [section]"}
		}
		key := strings.ToLower(strings.TrimSpace(line[:sep]))
		sections[cur][key] = strings.TrimSpace(line[sep+1:])
	}
	return sections, order, nil
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
