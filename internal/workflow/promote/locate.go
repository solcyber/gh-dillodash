package promote

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Where is the tier a change has reached.
type Where int

const (
	// OnFirst: merged into the first tier, not yet carried to staging.
	OnFirst Where = iota
	// Promoting: a PR into staging carries it and is open.
	Promoting
	// OnStage: on staging, ahead of production.
	OnStage
	// OnProd: on production (released); not listed.
	OnProd
)

// Change is one unit of work on its way to production: a PR merged into
// the first tier, and the PR that carries it into staging (open or
// merged). A staging PR with no first-tier PR in view is a change of its
// own (First nil).
type Change struct {
	Repo  model.Repo
	First *model.PR
	Promo *model.PR
	Where Where
	// Tag is the staging unit's tag while OnStage: "go", "hold" or "".
	Tag string
	// BlockedBy is, for a go unit, the first staging unit ahead of it that
	// is not go (its PR number): the release cut stops there. 0: none.
	BlockedBy int
	// Runs are the newest run of each workflow on the commit of the tier
	// the change is on (the first tier's merge commit, or the staging
	// unit's); nil while it is being promoted (the PR's checks say it).
	Runs []model.WorkflowRun
}

// Number is the change's PR number: the first-tier PR's, else the
// staging PR's.
func (c *Change) Number() int {
	if c.First != nil {
		return c.First.Number
	}
	if c.Promo != nil {
		return c.Promo.Number
	}
	return 0
}

// Title is the first-tier PR's title, else the staging PR's.
func (c *Change) Title() string {
	if c.First != nil {
		return c.First.Title
	}
	if c.Promo != nil {
		return c.Promo.Title
	}
	return ""
}

// Author is who opened the first-tier PR, else the staging PR.
func (c *Change) Author() string {
	if c.First != nil {
		return c.First.Author.Login
	}
	if c.Promo != nil {
		return c.Promo.Author.Login
	}
	return ""
}

// Ident keeps a change's row apart from a PR row of the same number (the
// list's cursor follows it): the number is negated.
func (c *Change) Ident() model.Ident { return model.Ident{Repo: c.Repo, Number: -c.Number()} }

// SHA is the commit the change's current tier deployed: the first-tier
// merge commit, or the staging unit's merge commit; "" otherwise.
func (c *Change) SHA() string {
	switch c.Where {
	case OnFirst:
		return mergeOid(c.First)
	case OnStage:
		return mergeOid(c.Promo)
	}
	return ""
}

// Since is when the change reached its current tier.
func (c *Change) Since() time.Time {
	switch {
	case c.Where == Promoting && c.Promo != nil:
		return c.Promo.CreatedAt
	case c.Where == OnStage && c.Promo != nil && c.Promo.MergedAt != nil:
		return *c.Promo.MergedAt
	case c.First != nil && c.First.MergedAt != nil:
		return *c.First.MergedAt
	}
	return time.Time{}
}

func mergeOid(p *model.PR) string {
	if p == nil || p.MergeCommit == nil {
		return ""
	}
	return p.MergeCommit.Oid
}

// refRE finds "#123" in a PR's title or body, but not "org/repo#123" (another
// repo) or "&#123;" (an entity).
var refRE = regexp.MustCompile(`(?:^|[^\w/&])#(\d+)\b`)

// refs is the PR numbers a staging PR names.
func refs(p *model.PR) map[int]bool {
	out := map[int]bool{}
	for _, m := range refRE.FindAllStringSubmatch(p.Title+"\n"+p.Body, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out[n] = true
		}
	}
	return out
}

// unitRE finds a staging unit's PR number in its commit subject: a merge
// commit or a squash (as the release cut reads it).
var unitRE = regexp.MustCompile(`Merge pull request #(\d+)|\(#(\d+)\)\s*$`)

// StageOrder is the PR numbers of the staging units ahead of production,
// oldest first, from the compare prod...stage: the first-parent chain
// from the head while it stays on the page. ok is false when the compare
// was cut short (more commits than the page).
func StageOrder(c ghapi.CompareResult) (units []int, ahead map[string]bool, ok bool) {
	ahead = map[string]bool{}
	bySHA := map[string]ghapi.CompareCommit{}
	for _, cm := range c.Commits {
		bySHA[cm.SHA] = cm
		ahead[cm.SHA] = true
	}
	n := c.AheadBy
	if n == 0 {
		n = len(c.Commits)
	}
	ok = n <= len(c.Commits)
	sha := ""
	if len(c.Commits) > 0 {
		sha = c.Commits[len(c.Commits)-1].SHA
	}
	seen := map[string]bool{}
	for sha != "" && !seen[sha] {
		seen[sha] = true
		cm, on := bySHA[sha]
		if !on {
			break
		}
		subject, _, _ := strings.Cut(cm.Message, "\n")
		if m := unitRE.FindStringSubmatch(subject); m != nil {
			num := m[1]
			if num == "" {
				num = m[2]
			}
			if v, err := strconv.Atoi(num); err == nil {
				units = append(units, v)
			}
		}
		if len(cm.Parents) == 0 {
			break
		}
		sha = cm.Parents[0]
	}
	for i, j := 0, len(units)-1; i < j; i, j = i+1, j-1 {
		units[i], units[j] = units[j], units[i]
	}
	return units, ahead, ok
}

// Inputs are what Locate reads of one repo.
type Inputs struct {
	Repo               model.Repo
	First, Stage, Prod string
	Set                ghapi.PromoteSet
	Units              []int           // staging units ahead of production, oldest first
	Ahead              map[string]bool // staging commits ahead of production
	AheadOK            bool            // false: the compare was cut short
	Go, Hold           map[int]bool    // tagged staging PR numbers
	// Contains reports a commit on a branch head's history (the first
	// tier's merge commit in a whole-tier promotion); errors count as no.
	Contains func(sha, head string) bool
}

// Locate places every change of one repo: each first-tier PR merged in
// view, linked to the staging PR that names it (#N in its title or body)
// or, for a whole-tier promotion (a staging PR from the first tier
// itself), that contains its merge commit; then every staging PR no
// first-tier PR links to. Back-merges (a PR from staging or production)
// are not changes. released counts what reached production.
func Locate(in Inputs) (changes []*Change, released int) {
	var stage []*model.PR
	for _, p := range append(append([]*model.PR(nil), in.Set.StageMerged...), in.Set.StageOpen...) {
		if p.HeadRefName == in.Prod {
			continue // a back-merge of production into staging
		}
		stage = append(stage, p)
	}
	named := make(map[*model.PR]map[int]bool, len(stage))
	for _, s := range stage {
		named[s] = refs(s)
	}
	linked := map[*model.PR]bool{}
	for _, f := range in.Set.FirstMerged {
		if f.HeadRefName == in.Stage || f.HeadRefName == in.Prod {
			continue // a back-merge into the first tier
		}
		var best *model.PR
		for _, s := range stage {
			if !named[s][f.Number] {
				continue
			}
			if better(s, best) {
				best = s
			}
		}
		if best == nil {
			for _, s := range stage {
				if s.HeadRefName != in.First || !after(s, f) || !better(s, best) {
					continue
				}
				if in.Contains != nil && in.Contains(mergeOid(f), s.HeadRefOid) {
					best = s
				}
			}
		}
		c := &Change{Repo: in.Repo, First: f, Promo: best}
		if best != nil {
			linked[best] = true
		}
		place(c, in)
		if c.Where == OnProd {
			released++
			continue
		}
		changes = append(changes, c)
	}
	for _, s := range stage {
		if linked[s] {
			continue
		}
		c := &Change{Repo: in.Repo, Promo: s}
		place(c, in)
		if c.Where == OnProd {
			released++
			continue
		}
		changes = append(changes, c)
	}
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Where != b.Where {
			return a.Where > b.Where // furthest along first: staging, promoting, first tier
		}
		return a.Since().Before(b.Since())
	})
	return changes, released
}

// better prefers a merged staging PR over an open one, and among merged
// ones the earliest (the change reached staging then).
func better(s, cur *model.PR) bool {
	if cur == nil {
		return true
	}
	sm, cm := s.MergedAt != nil, cur.MergedAt != nil
	if sm != cm {
		return sm
	}
	if sm {
		return s.MergedAt.Before(*cur.MergedAt)
	}
	return s.CreatedAt.Before(cur.CreatedAt)
}

// after: a staging PR merged before the first-tier PR merged can't carry
// it.
func after(s, f *model.PR) bool {
	if s.MergedAt == nil || f.MergedAt == nil {
		return true
	}
	return !s.MergedAt.Before(*f.MergedAt)
}

// place sets Where, Tag and BlockedBy.
func place(c *Change, in Inputs) {
	switch {
	case c.Promo == nil:
		c.Where = OnFirst
	case c.Promo.MergedAt == nil:
		c.Where = Promoting
	default:
		sha := mergeOid(c.Promo)
		onStage := in.Ahead[sha] || !in.AheadOK || sha == ""
		if !onStage {
			c.Where = OnProd
			return
		}
		c.Where = OnStage
		n := c.Promo.Number
		switch {
		case in.Go[n]:
			c.Tag = "go"
		case in.Hold[n]:
			c.Tag = "hold"
		}
		if c.Tag == "go" {
			for _, u := range in.Units {
				if u == n {
					break
				}
				if !in.Go[u] {
					c.BlockedBy = u
					break
				}
			}
		}
	}
}

// tagNumbers is the PR numbers of tag names under prefix ("go/612").
func tagNumbers(names []string, prefix string) map[int]bool {
	out := map[int]bool{}
	for _, t := range names {
		if v, err := strconv.Atoi(strings.TrimPrefix(t, prefix)); err == nil && strings.HasPrefix(t, prefix) {
			out[v] = true
		}
	}
	return out
}
