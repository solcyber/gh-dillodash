package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/selfupdate"
)

// BuildCheck reports the running build against the latest release and
// lists every install found; nil leaves the build out of the report.
type BuildCheck func(ctx context.Context) (selfupdate.Status, []selfupdate.Install)

// Build prints the version line and the installs. A stale build does
// not fail the check (the dashboard still runs), but it is the first
// thing the report says after the version, with the upgrade command;
// installs of different versions are named, since the one a person
// starts may not be the one they upgraded.
func Build(w io.Writer, st selfupdate.Status, installs []selfupdate.Install) {
	switch {
	case !selfupdate.Comparable(st.Running):
		fmt.Fprintf(w, "version: %s is not a release build — never compared with the latest release\n", st.Running)
	case st.Latest == "":
		why := "no check has answered yet"
		if st.Err != "" {
			why = "the check failed: " + st.Err
		}
		fmt.Fprintf(w, "version: latest release unknown (%s)\n", why)
	case st.Stale():
		fmt.Fprintf(w, "version: STALE — %s is out (checked %s); %s\n", st.Latest, ago(st.CheckedAt), st.Hint())
	default:
		fmt.Fprintf(w, "version: current (latest %s, checked %s)\n", st.Latest, ago(st.CheckedAt))
	}
	if st.Latest != "" && st.Err != "" {
		fmt.Fprintf(w, "  note: the last check failed (%s); that answer is from %s\n", st.Err, ago(st.CheckedAt))
	}
	if len(installs) == 0 {
		return
	}
	fmt.Fprintln(w, "installs:")
	versions := map[string]bool{}
	for _, in := range installs {
		var b strings.Builder
		fmt.Fprintf(&b, "  %-13s %s", in.Kind, in.Path)
		switch {
		case in.Kind == selfupdate.KindLauncher && in.Runs != "":
			fmt.Fprintf(&b, "  runs %s", in.Runs)
		case in.Kind == selfupdate.KindLauncher:
			b.WriteString("  a script: what it runs is upgraded, never the script")
		case in.Version != "":
			fmt.Fprintf(&b, "  %s", in.Version)
			versions[in.Version] = true
		}
		if in.Running {
			b.WriteString("  (running)")
		}
		if in.Pinned {
			b.WriteString("  (pinned)")
		}
		if selfupdate.Newer(st.Latest, in.Version) && !in.Running {
			fmt.Fprintf(&b, "  — STALE: %s", in.UpgradeHint(st.Latest))
		}
		if in.Note != "" {
			b.WriteString("  — " + in.Note)
		}
		fmt.Fprintln(w, b.String())
	}
	if len(versions) > 1 {
		var vs []string
		for v := range versions {
			vs = append(vs, v)
		}
		sortVersions(vs)
		fmt.Fprintf(w, "  note: installs of different versions (%s): the one you start may not be the one you upgraded\n", strings.Join(vs, ", "))
	}
}

func sortVersions(vs []string) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && (selfupdate.Compare(vs[j], vs[j-1]) > 0 || (selfupdate.Compare(vs[j], vs[j-1]) == 0 && vs[j] < vs[j-1])); j-- {
			vs[j], vs[j-1] = vs[j-1], vs[j]
		}
	}
}
