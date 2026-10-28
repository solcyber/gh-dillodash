package releasepr

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// unitPR finds the staging PR number in a unit's subject: a merge commit
// or a squash.
var unitPR = regexp.MustCompile(`Merge pull request #(\d+)|\(#(\d+)\)\s*$`)

// stageSummary is a repo's synthetic stage row: units ahead of
// production by tag. The compare is fetched only when the (prod, stage)
// pair moved.
func (w *Workflow) stageSummary(ctx context.Context, gh ghapi.Client, repo model.Repo, prod, stage string) (*Stage, error) {
	row := &Stage{Repo: repo}
	if prod == "" || stage == "" {
		return row, nil
	}
	var compare ghapi.CompareResult
	switch cached, ok := w.stageCache[repo]; {
	case prod == stage:
		compare = ghapi.CompareResult{}
	case ok && cached.prod == prod && cached.stage == stage:
		compare = cached.compare
	default:
		c, err := gh.Compare(ctx, repo, w.cfg.ProductionBranch, w.cfg.StagingBranch)
		if err != nil {
			return row, err
		}
		compare = c
		w.stageCache[repo] = stageCached{prod: prod, stage: stage, compare: c}
	}
	goTags, err := gh.TagRefs(ctx, repo, w.cfg.GoTagPrefix)
	if err != nil {
		return row, err
	}
	holdTags, err := gh.TagRefs(ctx, repo, w.cfg.HoldTagPrefix)
	if err != nil {
		return row, err
	}
	u := StageUnits(compare, goTags, holdTags, w.cfg.GoTagPrefix, w.cfg.HoldTagPrefix)
	row.Units = &u
	return row, nil
}

// StageUnits is what the staging branch carries ahead of production,
// from a compare page (prod...stage) and the go/<N> and hold/<N> tag
// names: one unit per staging PR (a merge commit or a squash whose
// subject names the PR), counted by tag. The compare page carries no
// parent links, so the units are the commits whose subject names a PR
// rather than a first-parent walk; side commits inside a merged branch
// are left out either way.
func StageUnits(compare ghapi.CompareResult, goTags, holdTags []string, goPrefix, holdPrefix string) Units {
	goSet := tagSet(goTags, goPrefix)
	holdSet := tagSet(holdTags, holdPrefix)
	ahead := compare.AheadBy
	if ahead == 0 && compare.TotalCommits > 0 {
		ahead = compare.TotalCommits
	}
	if ahead == 0 {
		ahead = len(compare.Commits)
	}
	out := Units{Ahead: ahead, Truncated: ahead > len(compare.Commits)}
	for _, c := range compare.Commits {
		subject, _, _ := strings.Cut(c.Message, "\n")
		m := unitPR.FindStringSubmatch(subject)
		if m == nil {
			continue
		}
		n := m[1]
		if n == "" {
			n = m[2]
		}
		out.Units++
		switch {
		case goSet[n]:
			out.Go++
		case holdSet[n]:
			out.Hold++
		default:
			out.Untagged++
		}
	}
	return out
}

func tagSet(tags []string, prefix string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			out[strings.TrimPrefix(t, prefix)] = true
		}
	}
	return out
}

// StageText is the stage row's text: "stage == main", "stage: nothing
// ahead of main", or "stage: 3 units ahead of main · 1 go · 1 hold · 1
// untagged".
func StageText(u *Units, prod, stage string) string {
	if prod == "" {
		prod = "main"
	}
	if stage == "" {
		stage = "stage"
	}
	if u == nil || u.Units == 0 {
		if u != nil && u.Ahead == 0 {
			return stage + " == " + prod
		}
		return fmt.Sprintf("%s: nothing ahead of %s", stage, prod)
	}
	ge := ""
	if u.Truncated {
		ge = "≥"
	}
	sep := glyphs.G("sep")
	return fmt.Sprintf("%s: %s%d unit%s ahead of %s %s %d go %s %d hold %s %d untagged",
		stage, ge, u.Units, plural(u.Units, "", "s"), prod, sep, u.Go, sep, u.Hold, sep, u.Untagged)
}

// stageText renders a stage row with the configured branch names.
func (w *Workflow) stageText(s *Stage) string {
	return StageText(s.Units, w.cfg.ProductionBranch, w.cfg.StagingBranch)
}
