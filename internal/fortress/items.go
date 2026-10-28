package fortress

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Kind is what an item is on the map. The core places PRs (dwarves),
// drafts, issues (goblins), epics (trolls), branches with no PR (ruins)
// and default-branch commits (statues). Workflows add the rest through
// Workflow.FortressItems: a "queued" PR sleeps in the barracks, a
// "release" trades at the depot (Zone "depot", or "far" once merged), and
// stage units are "wagon" (tagged go) or "hold" (on hold / untagged) at
// the depot; "stage" is accepted as an alias for "wagon".
type Kind string

// The kinds.
const (
	KindPR      Kind = "pr"
	KindDraft   Kind = "draft"
	KindIssue   Kind = "issue"
	KindEpic    Kind = "epic"
	KindBranch  Kind = "branch"
	KindCommit  Kind = "commit"
	KindRelease Kind = "release"
	KindWagon   Kind = "wagon"
	KindHold    Kind = "hold"
	KindQueued  Kind = "queued"
	KindStage   Kind = "stage" // alias: a wagon
)

// Kinds maps a kind to its glyph name.
var Kinds = map[Kind]string{
	KindPR: "df.dwarf", KindDraft: "df.draft", KindIssue: "df.goblin",
	KindEpic: "df.troll", KindBranch: "df.ruin", KindCommit: "df.statue",
	KindRelease: "df.merchant", KindWagon: "df.wagon", KindHold: "df.wagon.hold",
	KindQueued: "df.dwarf", KindStage: "df.wagon",
}

// LegendRow is one line of the legend box.
type LegendRow struct {
	Glyph string // glyph name
	Color string // DF color name
	Text  string
}

// Legend is the legend box, in order.
var Legend = []LegendRow{
	{"df.dwarf", "yellow", "PR awaiting review (blinks !)"},
	{"df.dwarf", "lred", "PR failing CI (on fire)"},
	{"df.dwarf", "lcyan", "PR approved"},
	{"df.dwarf", "lmagenta", "PR changes requested"},
	{"df.dwarf", "white", "PR, no review asked; walks: CI running"},
	{"df.draft", "dgray", "draft PR"},
	{"df.goblin", "lgray", "issue (yellow: yours, red: bug)"},
	{"df.troll", "lgray", "epic issue"},
	{"df.ruin", "brown", "branch with no PR (denser: more)"},
	{"df.statue", "lgray", "commits on the default branch"},
	{"df.merchant", "yellow", "release PR needs approval (depot)"},
	{"df.wagon", "yellow", "stage unit tagged go"},
	{"df.wagon.hold", "dgray", "stage unit on hold / untagged"},
	{"df.sleep", "lblue", "queued for the window (barracks)"},
}

// BugLabels are the label prefixes that paint an issue red.
var BugLabels = []string{"bug", "incident", "security", "sev"}

// The animations: alert blinks !, fire blinks the fire, sleep blinks z,
// pace steps sideways (CI running), glint sparkles (a fresh commit),
// walk is a merchant on the move.
const (
	AnimAlert = "alert"
	AnimFire  = "fire"
	AnimSleep = "sleep"
	AnimPace  = "pace"
	AnimGlint = "glint"
	AnimWalk  = "walk"
)

// The zones: items with a zone fill it instead of standing on their age
// row; anything older than the city's limit goes "beyond".
const (
	ZoneBarracks = "barracks"
	ZoneDepot    = "depot"
	ZoneFar      = "far"
	ZoneBeyond   = "beyond"
)

// Open is what Enter opens for an item: a PR or issue detail.
type Open struct {
	Kind   string // "pr" | "issue"
	Repo   model.Repo
	Number int
}

// Item is one thing on the map.
type Item struct {
	Kind Kind
	Repo model.Repo
	// ID is stable across refreshes (PRID, IssueID, "branch:<repo>:<name>",
	// "commit:<repo>:<oid>"); a workflow item whose ID matches a core
	// item's replaces it (a queued PR is a sleeper, not a dwarf).
	ID string
	// Age in days decides the row; Zone (when set) decides the place.
	Age   float64
	Zone  string
	Color string // a DF color name (Colors)
	Anim  string // "" or one of the Anim* constants
	// Text is the look panel's first line ("infra#101 · awaiting review ·
	// by bob"); its first word is the item's ref in the announcements.
	Text  string
	Title string
	URL   string
	Open  *Open
	// Urgency orders the tab walk (lower first) and bolds <= 2.
	Urgency int

	// Lane is the repo's lane; Items sets it.
	Lane int
	// Placed and the cell are set by Place.
	Placed bool
	Cell   Cell
}

// GlyphName is the item's shape.
func (it *Item) GlyphName() string {
	if g, ok := Kinds[it.Kind]; ok {
		return g
	}
	return "df.dwarf"
}

// PRID is the ID of a PR's item.
func PRID(repo model.Repo, number int) string {
	return fmt.Sprintf("pr:%s#%d", repo.Name, number)
}

// IssueID is the ID of an issue's item.
func IssueID(repo model.Repo, number int) string {
	return fmt.Sprintf("issue:%s#%d", repo.Name, number)
}

// Age is the days since an instant, as a float (0 when unknown).
func Age(t, now time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return max(0, now.Sub(t).Hours()/24)
}

// PRState is what the dashboard knows about a PR's review and CI state.
type PRState struct {
	Label    string // the chip label (model.DecisionLabel or a workflow's)
	NeedsYou bool   // the chip counts in "N need you"
	CI       model.CICluster
}

// Classify is the (color, anim, state text) of a PR.
func Classify(st PRState) (color, anim, state string) {
	if st.Label == model.LabelDraft {
		return "dgray", "", "draft"
	}
	switch {
	case st.NeedsYou || st.Label == model.LabelAwaiting:
		color, anim = "yellow", AnimAlert
	case st.Label == model.LabelChanges:
		color, anim = "lmagenta", ""
	default:
		color, anim = "lcyan", ""
	}
	state = st.Label
	if len(st.CI.Failed) > 0 {
		color, anim = "lred", AnimFire
		state += " · CI failing (" + strings.Join(st.CI.Failed[:min(2, len(st.CI.Failed))], ", ") + ")"
	} else if strings.ContainsAny(st.CI.Glyphs, "~.") {
		anim = AnimPace
		state += " · CI running"
	}
	return color, anim, state
}

// Input is everything Items reads: the dashboard's last refresh.
type Input struct {
	Repos  []model.Repo // the lanes, in config order
	Me     string
	Host   string
	Now    time.Time
	PRs    []*model.PR
	Issues []*model.Issue
	Data   map[model.Repo]ghapi.FortressData
	// PRState classifies a PR (nil: model.DecisionLabel, no CI).
	PRState func(pr *model.PR) PRState
	// SkipIssue drops issues a workflow owns (nil: none).
	SkipIssue func(i *model.Issue) bool
	// Extra are the workflows' items; their IDs shadow the core's.
	Extra []*Item
}

// Items is everything the map draws, from the last refresh.
func Items(in Input) []*Item {
	laneOf := map[model.Repo]int{}
	for i, r := range in.Repos {
		laneOf[r] = i
	}
	now := in.Now
	if now.IsZero() {
		now = clock.Now()
	}
	shadow := map[string]bool{}
	for _, it := range in.Extra {
		shadow[it.ID] = true
	}
	var items []*Item
	add := func(it *Item) {
		it.Lane = laneOf[it.Repo]
		if it.Kind == KindStage {
			it.Kind = KindWagon
		}
		items = append(items, it)
	}
	seen := map[model.Ident]bool{}
	heads := map[model.Repo]map[string]bool{}
	head := func(repo model.Repo, name string) {
		if heads[repo] == nil {
			heads[repo] = map[string]bool{}
		}
		heads[repo][name] = true
	}
	who := func(login string) string {
		if login == "" {
			return "?"
		}
		return login
	}
	// the review repos' PRs: dwarves (a workflow may have shadowed one)
	for _, p := range in.PRs {
		seen[p.Ident()] = true
		head(p.Repo, p.HeadRefName)
		id := PRID(p.Repo, p.Number)
		if shadow[id] {
			continue
		}
		created := p.CreatedAt
		if created.IsZero() {
			created = p.UpdatedAt
		}
		st := PRState{Label: model.DecisionLabel(p, in.Me, false)}
		if in.PRState != nil {
			st = in.PRState(p)
		}
		color, anim, state := Classify(st)
		kind := KindPR
		if color == "dgray" {
			kind = KindDraft
		}
		urgency := 7
		switch color {
		case "yellow":
			urgency = 2
		case "lred":
			urgency = 3
		}
		add(&Item{Kind: kind, Repo: p.Repo, ID: id, Age: Age(created, now), Color: color, Anim: anim,
			Text:  fmt.Sprintf("%s#%d · %s · by %s", p.Repo.Name, p.Number, state, who(p.Author.Login)),
			Title: p.Title, URL: ghapi.PRURL(in.Host, p.Repo, p.Number),
			Open: &Open{"pr", p.Repo, p.Number}, Urgency: urgency})
	}
	// issues: goblins (trolls for epics)
	for _, i := range in.Issues {
		if in.SkipIssue != nil && in.SkipIssue(i) {
			continue
		}
		var labels []string
		for _, n := range i.Labels.Names() {
			if n != "" {
				labels = append(labels, strings.ToLower(n))
			}
		}
		sort.Strings(labels)
		mine := false
		for _, a := range i.Assignees.Nodes {
			if strings.EqualFold(a.Login, in.Me) && in.Me != "" {
				mine = true
			}
		}
		bug, epic := false, false
		for _, l := range labels {
			for _, b := range BugLabels {
				if strings.HasPrefix(l, b) {
					bug = true
				}
			}
			if strings.Contains(l, "epic") {
				epic = true
			}
		}
		color := "lgray"
		switch {
		case mine:
			color = "yellow"
		case bug:
			color = "lred"
		}
		kind := KindIssue
		if epic {
			kind = KindEpic
		}
		text := fmt.Sprintf("%s#%d · issue", i.Repo.Name, i.Number)
		if mine {
			text += " · assigned to you"
		}
		if len(labels) > 0 {
			text += " · " + strings.Join(labels[:min(3, len(labels))], ", ")
		}
		created := i.CreatedAt
		if created.IsZero() {
			created = i.UpdatedAt
		}
		urgency := 8
		if mine {
			urgency = 5
		}
		add(&Item{Kind: kind, Repo: i.Repo, ID: IssueID(i.Repo, i.Number), Age: Age(created, now), Color: color,
			Text: text, Title: i.Title, URL: ghapi.IssueURL(in.Host, i.Repo, i.Number),
			Open: &Open{"issue", i.Repo, i.Number}, Urgency: urgency})
	}
	// the fortress query's other open PRs (ones the list did not carry,
	// e.g. into another base): dwarves, no CI known
	for _, repo := range in.Repos {
		info, ok := in.Data[repo]
		if !ok {
			continue
		}
		for _, p := range info.OpenPRs {
			if p == nil {
				continue
			}
			head(repo, p.HeadRefName)
			if seen[model.Ident{Repo: repo, Number: p.Number}] {
				continue
			}
			id := PRID(repo, p.Number)
			if shadow[id] {
				continue
			}
			d := p.ReviewDecision
			color := "white"
			switch {
			case p.IsDraft:
				color = "dgray"
			case d == "APPROVED":
				color = "lcyan"
			case d == "CHANGES_REQUESTED":
				color = "lmagenta"
			case d == "REVIEW_REQUIRED":
				color = "yellow"
			}
			kind, anim, urgency := KindPR, "", 7
			if p.IsDraft {
				kind = KindDraft
			}
			if color == "yellow" {
				anim, urgency = AnimAlert, 2
			}
			decision := "no review asked"
			if d != "" {
				decision = strings.ToLower(strings.ReplaceAll(d, "_", " "))
			}
			base := p.BaseRefName
			if base == "" {
				base = "?"
			}
			add(&Item{Kind: kind, Repo: repo, ID: id, Age: Age(p.CreatedAt, now), Color: color, Anim: anim,
				Text:  fmt.Sprintf("%s#%d · into %s · %s · by %s · CI not read", repo.Name, p.Number, base, decision, who(p.Author.Login)),
				Title: p.Title, URL: ghapi.PRURL(in.Host, repo, p.Number),
				Open: &Open{"pr", repo, p.Number}, Urgency: urgency})
		}
	}
	// branches with no open PR (ruins) and default-branch commits (statues)
	for _, repo := range in.Repos {
		info, ok := in.Data[repo]
		if !ok {
			continue
		}
		skip := map[string]bool{info.Default: true, "main": true, "master": true}
		for _, b := range info.Branches {
			if b.Name == "" || skip[b.Name] || heads[repo][b.Name] {
				continue
			}
			ago := "?"
			if !b.CommittedDate.IsZero() {
				ago, _ = clock.ShortAge(b.CommittedDate, now)
			}
			add(&Item{Kind: KindBranch, Repo: repo, ID: "branch:" + repo.Name + ":" + b.Name,
				Age: Age(b.CommittedDate, now), Color: "brown",
				Text: fmt.Sprintf("%s · branch %s · last commit %s ago · no open PR", repo.Name, b.Name, ago),
				URL:  ghapi.TreeURL(in.Host, repo, b.Name), Urgency: 10})
		}
		def := info.Default
		if def == "" {
			def = "main"
		}
		for _, cm := range info.Commits {
			age := Age(cm.CommittedDate, now)
			color, anim := "lgray", ""
			if age < 1 {
				color, anim = "white", AnimGlint
			}
			ago, _ := clock.ShortAge(cm.CommittedDate, now)
			add(&Item{Kind: KindCommit, Repo: repo, ID: "commit:" + repo.Name + ":" + cm.Oid, Age: age,
				Color: color, Anim: anim,
				Text:  fmt.Sprintf("%s %s · %s · %s ago", repo.Name, def, shortSHA(cm.Oid), ago),
				Title: cm.MessageHeadline, URL: ghapi.CommitURL(in.Host, repo, cm.Oid), Urgency: 11})
		}
	}
	for _, it := range in.Extra {
		if it == nil {
			continue
		}
		if it.Urgency == 0 {
			it.Urgency = 9
		}
		add(it)
	}
	return items
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// fhash is the placement hash of an ID: the first 32 bits of its MD5, so
// an item keeps its spot across refreshes and processes.
func fhash(s string) uint32 {
	sum := md5.Sum([]byte(s))
	return binary.BigEndian.Uint32(sum[:4])
}

// Hash is fhash, for tests.
func Hash(s string) uint32 { return fhash(s) }

func stacks(k Kind) bool { return k == KindBranch || k == KindCommit }

// Place gives each item a cell: zone items fill their zone, everything
// else its age row in its repo's lane (nearest free cell, at most two
// rows off). Returns the items by cell and the number left off the map
// for want of room. Branches and commits stack: a cell holds any number
// of one of those kinds (ruins pile up as a heatmap).
func Place(items []*Item, m *Map, lanes int) (map[Cell][]*Item, int) {
	occ := map[Cell][]*Item{}
	hidden := 0
	n := max(1, lanes)
	zones := map[string][]Cell{}
	order := append([]*Item(nil), items...)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if stacks(a.Kind) != stacks(b.Kind) {
			return !stacks(a.Kind)
		}
		if (a.Kind == KindBranch) != (b.Kind == KindBranch) {
			return a.Kind != KindBranch
		}
		if a.Urgency != b.Urgency {
			return a.Urgency < b.Urgency
		}
		return a.ID < b.ID
	})
	for _, it := range order {
		it.Placed = false
		z := it.Zone
		if it.Age > float64(m.City.Limit) && z == "" {
			z = ZoneBeyond
		}
		if z != "" {
			if _, ok := zones[z]; !ok {
				cells := m.ZoneCells(z)
				// every other column, so neighbours read apart
				var even, odd []Cell
				for _, rc := range cells {
					if rc.C%2 == 0 {
						even = append(even, rc)
					} else {
						odd = append(odd, rc)
					}
				}
				zones[z] = append(even, odd...)
			}
			var free []Cell
			for _, rc := range zones[z] {
				if _, taken := occ[rc]; !taken {
					free = append(free, rc)
				}
			}
			if len(free) == 0 && it.Kind == KindBranch {
				// a full zone still takes ruins: they pile up (heatmap)
				for _, rc := range zones[z] {
					if occ[rc][0].Kind == KindBranch {
						free = append(free, rc)
					}
				}
				sort.SliceStable(free, func(i, j int) bool { return len(occ[free[i]]) < len(occ[free[j]]) })
			}
			if len(free) == 0 {
				hidden++
				continue
			}
			rc := free[0]
			occ[rc] = append(occ[rc], it)
			it.Placed, it.Cell = true, rc
			continue
		}
		row := m.AgeRow(it.Age)
		h := int(fhash(it.ID))
		placed := false
		for _, dr := range []int{0, 1, -1, 2, -2} {
			r := row + dr
			if r < 0 || r >= m.Rows || !m.Span[r].OK {
				continue
			}
			a, b := m.Span[r].A, m.Span[r].B
			w := float64(b-a) / float64(n)
			l0 := int(float64(a) + float64(it.Lane)*w)
			l1 := max(l0+1, int(float64(a)+float64(it.Lane+1)*w))
			width := l1 - l0
			pref := l0 + h%width
			for off := 0; off < width; off++ {
				sign := -1
				if off%2 == 1 {
					sign = 1
				}
				c := l0 + mod(pref-l0+(off+1)/2*sign, width)
				if !m.Placeable(r, c) {
					continue
				}
				here := occ[Cell{r, c}]
				// ruins and statues pile up in a cell of their own kind
				if here == nil || (stacks(it.Kind) && here[0].Kind == it.Kind) {
					it.Placed, it.Cell = true, Cell{r, c}
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			hidden++
			continue
		}
		occ[it.Cell] = append(occ[it.Cell], it)
	}
	return occ, hidden
}

func mod(a, b int) int { return ((a % b) + b) % b }

// Order is the placed items in tab order: most urgent, then youngest.
func Order(items []*Item) []*Item {
	var out []*Item
	for _, it := range items {
		if it.Placed {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Urgency != b.Urgency {
			return a.Urgency < b.Urgency
		}
		if a.Age != b.Age {
			return a.Age < b.Age
		}
		return a.ID < b.ID
	})
	return out
}

// Season names the season the way Dwarf Fortress does: "Late Autumn 2026".
func Season(d time.Time) string {
	m := d.Month()
	var season string
	switch m {
	case 12, 1, 2:
		season = "Winter"
	case 3, 4, 5:
		season = "Spring"
	case 6, 7, 8:
		season = "Summer"
	default:
		season = "Autumn"
	}
	part := "Late"
	switch m {
	case 12, 3, 6, 9:
		part = "Early"
	case 1, 4, 7, 10:
		part = "Mid"
		if d.Day() > 20 {
			part = "Late"
		}
	}
	return fmt.Sprintf("%s %s %d", part, season, d.Year())
}
