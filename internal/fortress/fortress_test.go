package fortress

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/theme"
)

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
	now    = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestProjections(t *testing.T) {
	// Broadway and Houston is the Manhattan grid's origin
	if x, y := ManhattanXY(40.7253, -73.9965); math.Abs(x) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Errorf("origin = (%v, %v)", x, y)
	}
	// 59th and 5th, the corner of Central Park, is 59 streets up
	if _, y := ManhattanXY(40.7644, -73.9730); math.Abs(y-59) > 2 {
		t.Errorf("59th St projects to %v", y)
	}
	// Chicago's grid starts at State and Madison; 35th St is -35
	if x, y := ChicagoXY(41.8819, -87.6278); math.Abs(x) > 1e-6 || math.Abs(y) > 1e-9 {
		t.Errorf("State & Madison = (%v, %v)", x, y)
	}
	if _, y := ChicagoXY(41.8309, -87.6278); math.Abs(y+35) > 1e-9 {
		t.Errorf("35th St projects to %v", y)
	}
	// a latitude between two known streets interpolates
	if _, y := ChicagoXY((41.8309+41.8380)/2, -87.6278); math.Abs(y+33) > 1e-9 {
		t.Errorf("between 35th and 31st = %v", y)
	}
	// Lake Michigan is east of State
	if x, _ := ChicagoXY(41.8819, -87.5); x <= 0 {
		t.Errorf("the lake is east: x = %v", x)
	}
}

func TestScanSpans(t *testing.T) {
	square := []XY{{0, 0}, {4, 0}, {4, 4}, {0, 4}}
	got := scanSpans(square, 2)
	if len(got) != 1 || got[0] != [2]float64{0, 4} {
		t.Errorf("square spans = %v", got)
	}
	if got := scanSpans(square, 5); len(got) != 0 {
		t.Errorf("outside spans = %v", got)
	}
	// a U shape: two spans across its arms, one across its base
	u := []XY{{0, 0}, {6, 0}, {6, 6}, {4, 6}, {4, 2}, {2, 2}, {2, 6}, {0, 6}}
	if got := scanSpans(u, 4); len(got) != 2 || got[0] != [2]float64{0, 2} || got[1] != [2]float64{4, 6} {
		t.Errorf("U arms = %v", got)
	}
	if got := scanSpans(u, 1); len(got) != 1 || got[0] != [2]float64{0, 6} {
		t.Errorf("U base = %v", got)
	}
}

func TestMapRasterizes(t *testing.T) {
	for _, key := range CityKeys {
		for _, wh := range [][2]int{{80, 20}, {120, 36}, {200, 56}} {
			w, h := wh[0], wh[1]
			f := NewMap(key, w, h, ZoomFit, 5, 0)
			land := 0
			for r := 0; r < f.Rows; r++ {
				for c := 0; c < f.Cols; c++ {
					if f.Main[r][c] {
						land++
					}
				}
			}
			if f.Rows != h || f.Cols < w || land <= w*h/40 {
				t.Errorf("%s %dx%d: rows=%d cols=%d land=%d", key, w, h, f.Rows, f.Cols, land)
			}
		}
	}
}

func TestManhattanGeography(t *testing.T) {
	mh := NewMap("manhattan", 160, 50, ZoomStreet, 5, 0)
	r, c := mh.CellOf(ManhattanXY(40.782, -73.966)) // the Great Lawn
	if mh.T[r][c] != 'p' || mh.RowY(r) < 59 || mh.RowY(r) > 110 {
		t.Errorf("Central Park: code %q at y %v", mh.T[r][c], mh.RowY(r))
	}
	if mh.What(r, c) != "Central Park" {
		t.Errorf("the Great Lawn is in %q", mh.What(r, c))
	}
	r, c = mh.CellOf(ManhattanXY(40.760, -74.010)) // mid-Hudson at 42nd
	if mh.T[r][c] != 'w' || mh.Main[r][c] {
		t.Errorf("the Hudson: code %q main %v", mh.T[r][c], mh.Main[r][c])
	}
	if math.Abs(mh.RowY(mh.AgeRow(14))-14) >= 1 {
		t.Errorf("14th St is %v days", mh.RowY(mh.AgeRow(14)))
	}
	if _, ok := mh.StreetRows[mh.AgeRow(14)]; !ok {
		t.Error("14th St has no margin label")
	}
	// Broadway is a road on the island
	r, c = mh.CellOf(ManhattanXY(40.7497, -73.9880))
	if mh.T[r][c] != 'B' && mh.T[r][c] != '-' {
		t.Errorf("Broadway at 34th: %q", mh.T[r][c])
	}
}

func TestChicagoGeography(t *testing.T) {
	ch := NewMap("chicago", 160, 50, ZoomStreet, 5, 0)
	r := ch.AgeRow(35)
	if math.Abs(ch.RowY(r)+35) >= 1 {
		t.Errorf("35th St is at y %v", ch.RowY(r))
	}
	east := -1
	for c := 0; c < ch.Cols; c++ {
		if ch.T[r][c] == 'w' {
			east = c
			break
		}
	}
	if east < 0 || !ch.Span[r].OK || east < ch.Span[r].A {
		t.Errorf("Lake Michigan at 35th: first water col %d, span %+v", east, ch.Span[r])
	}
	rr, cc := ch.CellOf(ChicagoXY(41.8875, -87.6310))
	if ch.T[rr][cc] != 'w' {
		t.Errorf("the river by the Loop: %q", ch.T[rr][cc])
	}
	if ch.What(rr, cc) != "the Chicago River" {
		t.Errorf("the river is %q", ch.What(rr, cc))
	}
}

func prFixture(n int, created string) *model.PR {
	p := &model.PR{Repo: infra, Number: n, Title: "t", State: "OPEN", CreatedAt: ts(created),
		HeadRefName: "feature-x", BaseRefName: "main"}
	p.Author.Login = "bob"
	return p
}

func fixtureInput() Input {
	var prs []*model.PR
	for i := 0; i < 6; i++ {
		p := prFixture(4000+i, "2026-10-2"+string(rune('7'-i))+"T12:00:00Z")
		p.HeadRefName = "feature-" + p.Title + string(rune('0'+i))
		prs = append(prs, p)
	}
	prs[0].HeadRefName = "feature-x4000"
	prs[1].IsDraft = true
	states := map[int]PRState{
		4000: {Label: model.LabelAwaiting, NeedsYou: true, CI: model.CICluster{Glyphs: "+x", Failed: []string{"plan"}}},
	}
	var issues []*model.Issue
	for i := 0; i < 4; i++ {
		is := &model.Issue{Repo: infra, Number: 3900 + i, Title: "i", CreatedAt: ts("2026-10-01T00:00:00Z").AddDate(0, -3*i, 0)}
		if i == 0 {
			is.Labels.Nodes = []model.Label{{Name: "bug"}}
		}
		issues = append(issues, is)
	}
	data := map[model.Repo]ghapi.FortressData{
		infra: {Default: "main", BranchTotal: 2, Branches: []ghapi.FortressBranch{
			{Name: "feature-x4000", CommittedDate: ts("2026-10-20T00:00:00Z")},
			{Name: "old", CommittedDate: ts("2026-08-01T00:00:00Z")},
		}, Commits: []ghapi.FortressCommit{
			{Oid: strings.Repeat("a", 40), MessageHeadline: "x", CommittedDate: ts("2026-10-28T06:00:00Z")},
		}},
		portal: {OpenPRs: []*model.PR{{Repo: portal, Number: 7, Title: "dev pr", CreatedAt: ts("2026-10-25T00:00:00Z"),
			BaseRefName: "dev", HeadRefName: "feat", ReviewDecision: "REVIEW_REQUIRED"}}},
	}
	return Input{
		Repos: []model.Repo{infra, portal}, Me: "alice", Host: "github.com", Now: now,
		PRs: prs, Issues: issues, Data: data,
		PRState: func(pr *model.PR) PRState {
			if st, ok := states[pr.Number]; ok {
				return st
			}
			return PRState{Label: model.DecisionLabel(pr, "alice", false), NeedsYou: !pr.IsDraft}
		},
	}
}

func TestItems(t *testing.T) {
	items := Items(fixtureInput())
	kinds := map[Kind]bool{}
	byID := map[string]*Item{}
	portalSeen := false
	for _, it := range items {
		kinds[it.Kind] = true
		byID[it.ID] = it
		if it.Repo == portal {
			portalSeen = true
			if it.Lane != 1 {
				t.Errorf("%s is in lane %d", it.ID, it.Lane)
			}
		}
	}
	for _, k := range []Kind{KindPR, KindDraft, KindIssue, KindBranch, KindCommit} {
		if !kinds[k] {
			t.Errorf("no %s item", k)
		}
	}
	if !portalSeen {
		t.Error("the other repo's open PR is missing")
	}
	if _, ok := byID["branch:infra:feature-x4000"]; ok {
		t.Error("a branch with an open PR is a ruin")
	}
	if _, ok := byID["branch:infra:old"]; !ok {
		t.Error("the old branch is not a ruin")
	}
	if it := byID[PRID(infra, 4000)]; it == nil || it.Anim != AnimFire || it.Color != "lred" || !strings.Contains(it.Text, "CI failing (plan)") {
		t.Errorf("failing CI: %+v", it)
	}
	if it := byID[PRID(infra, 4001)]; it == nil || it.Kind != KindDraft || it.Color != "dgray" {
		t.Errorf("draft: %+v", it)
	}
	if it := byID[IssueID(infra, 3900)]; it == nil || it.Color != "lred" || it.Open == nil || it.Open.Kind != "issue" {
		t.Errorf("bug issue: %+v", it)
	}
	if it := byID[PRID(portal, 7)]; it == nil || it.Color != "yellow" || !strings.Contains(it.Text, "into dev") || !strings.Contains(it.Text, "CI not read") {
		t.Errorf("portal PR: %+v", it)
	}
	if it := byID["commit:infra:"+strings.Repeat("a", 40)]; it == nil || it.Anim != AnimGlint || it.Color != "white" || it.URL != "https://github.com/acme/infra/commit/"+strings.Repeat("a", 40) {
		t.Errorf("fresh commit: %+v", it)
	}
	if it := byID[PRID(infra, 4002)]; it == nil || it.URL != "https://github.com/acme/infra/pull/4002" || it.Open == nil || it.Open.Number != 4002 || math.Abs(it.Age-3) > 0.01 {
		t.Errorf("PR 4002: %+v", it)
	}
}

func TestWorkflowItemsShadowTheCore(t *testing.T) {
	in := fixtureInput()
	in.Extra = []*Item{
		{Kind: KindQueued, Repo: infra, ID: PRID(infra, 4002), Color: "lblue", Anim: AnimSleep, Zone: ZoneBarracks, Text: "infra#4002 · queued"},
		{Kind: KindStage, Repo: portal, ID: "wagon:web-portal:go0", Color: "yellow", Zone: ZoneDepot, Text: "web-portal stage"},
	}
	items := Items(in)
	var queued, wagon *Item
	for _, it := range items {
		if it.ID == PRID(infra, 4002) {
			if queued != nil {
				t.Error("the queued PR is on the map twice")
			}
			queued = it
		}
		if it.ID == "wagon:web-portal:go0" {
			wagon = it
		}
	}
	if queued == nil || queued.Kind != KindQueued || queued.Zone != ZoneBarracks {
		t.Errorf("queued = %+v", queued)
	}
	if wagon == nil || wagon.Kind != KindWagon || wagon.Lane != 1 || wagon.Urgency != 9 {
		t.Errorf("wagon = %+v", wagon)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		st               PRState
		color, anim, sub string
	}{
		{PRState{Label: model.LabelDraft}, "dgray", "", "draft"},
		{PRState{Label: model.LabelAwaiting, NeedsYou: true}, "yellow", AnimAlert, "awaiting review"},
		{PRState{Label: "applied, needs review", NeedsYou: true}, "yellow", AnimAlert, "applied"},
		{PRState{Label: model.LabelChanges}, "lmagenta", "", "changes requested"},
		{PRState{Label: model.LabelApproved}, "lcyan", "", "approved"},
		{PRState{Label: model.LabelApproved, CI: model.CICluster{Glyphs: "x", Failed: []string{"a", "b", "c"}}}, "lred", AnimFire, "CI failing (a, b)"},
		{PRState{Label: model.LabelApproved, CI: model.CICluster{Glyphs: "+~"}}, "lcyan", AnimPace, "CI running"},
	}
	for _, c := range cases {
		color, anim, state := Classify(c.st)
		if color != c.color || anim != c.anim || !strings.Contains(state, c.sub) {
			t.Errorf("Classify(%+v) = %s %s %q", c.st, color, anim, state)
		}
	}
}

func TestPlacement(t *testing.T) {
	items := Items(fixtureInput())
	for _, key := range CityKeys {
		fm := NewMap(key, 120, 36, ZoomFit, 2, 0)
		a, hidden := Place(items, fm, 2)
		b, _ := Place(items, fm, 2)
		if len(a) != len(b) {
			t.Errorf("%s: placement differs: %d vs %d cells", key, len(a), len(b))
		}
		for cell := range a {
			if _, ok := b[cell]; !ok {
				t.Errorf("%s: cell %v moved", key, cell)
			}
		}
		if hidden != 0 {
			t.Errorf("%s: %d hidden", key, hidden)
		}
		for cell, here := range a {
			if len(here) > 1 {
				for _, it := range here {
					if it.Kind != here[0].Kind || !stacks(it.Kind) {
						t.Errorf("%s: %v stacks %s on %s", key, cell, it.Kind, here[0].Kind)
					}
				}
			}
		}
		beyond := fm.City.Zones[ZoneBeyond]
		old, young := 0, 0
		for _, it := range items {
			if !it.Placed {
				t.Errorf("%s: %s not placed", key, it.ID)
				continue
			}
			if !fm.Placeable(it.Cell.R, it.Cell.C) && it.Age <= float64(fm.City.Limit) && it.Zone == "" {
				t.Errorf("%s: %s stands on %q", key, it.ID, fm.T[it.Cell.R][it.Cell.C])
			}
			if it.Kind == KindIssue && it.Age > float64(fm.City.Limit) {
				old++
				if y := fm.RowY(it.Cell.R); y < beyond.Y0 || y > beyond.Y1 {
					t.Errorf("%s: old %s at y %v, not beyond", key, it.ID, y)
				}
			}
			if it.Zone == "" && it.Age < 60 {
				young++
				if d := it.Cell.R - fm.AgeRow(it.Age); d < -2 || d > 2 {
					t.Errorf("%s: %s is %d rows off its street", key, it.ID, d)
				}
			}
		}
		if old == 0 || young == 0 {
			t.Errorf("%s: fixture has %d old and %d young", key, old, young)
		}
	}
}

func TestPlacementStacksRuinsWhenFull(t *testing.T) {
	fm := NewMap("chicago", 80, 24, ZoomFit, 1, 0)
	var items []*Item
	for i := 0; i < 400; i++ {
		items = append(items, &Item{Kind: KindBranch, Repo: infra, ID: "branch:infra:b" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Age: 1000, Color: "brown"})
	}
	cells, hidden := Place(items, fm, 1)
	if hidden != 0 {
		t.Errorf("%d ruins hidden", hidden)
	}
	stacked := 0
	for _, here := range cells {
		if len(here) > 1 {
			stacked++
		}
	}
	if stacked == 0 {
		t.Error("a full zone should stack ruins")
	}
}

func TestHash(t *testing.T) {
	if Hash("x") != 0x9dd4e461 {
		t.Errorf("Hash(x) = %x", Hash("x"))
	}
	if Hash("pr:infra#1") == Hash("pr:infra#2") {
		t.Error("hash collides")
	}
}

func TestGlyphs(t *testing.T) {
	fire := &Item{Kind: KindPR, Anim: AnimFire, ID: "x"}
	alert := &Item{Kind: KindPR, Anim: AnimAlert, ID: "x"}
	if Glyph(fire, 0, true) != "df.fire" || Glyph(alert, 0, true) != "df.alert" {
		t.Error("still draws the status glyph")
	}
	sleeper := &Item{Kind: KindQueued, Anim: AnimSleep, ID: "y"}
	if g := Glyph(sleeper, 0, true); g != "df.sleep" {
		t.Errorf("sleeper = %s", g)
	}
	// moving, the glyph alternates between the shape and the status
	seen := map[string]bool{}
	for tt := 0.0; tt < 4; tt += 0.5 {
		seen[Glyph(alert, tt, false)] = true
	}
	if !seen["df.dwarf"] || !seen["df.alert"] {
		t.Errorf("alert never blinks: %v", seen)
	}
	ruin := &Item{Kind: KindBranch, ID: "r"}
	if Glyph(ruin, 1, false) != "df.ruin" {
		t.Error("a branch is a ruin")
	}
	plain := &Item{Kind: KindIssue, ID: "i"}
	if Glyph(plain, 1, false) != "df.goblin" || StatusGlyph(plain) != "df.goblin" {
		t.Error("an issue is a goblin")
	}
}

func TestLegendCoversEveryKind(t *testing.T) {
	have := map[string]bool{}
	for _, row := range Legend {
		have[row.Glyph] = true
	}
	for k, g := range Kinds {
		if !have[g] {
			t.Errorf("kind %s (%s) has no legend row", k, g)
		}
	}
}

func TestDFGlyphsExistInEverySet(t *testing.T) {
	for _, set := range []glyphs.Set{glyphs.Nerd, glyphs.Unicode, glyphs.ASCII} {
		all := glyphs.All(set)
		names := []string{"df.water", "df.cursor", "df.wild", "df.tree", "df.ground", "df.street", "df.road", "df.bridge", "df.rail", "df.alert", "df.fire", "df.sleep", "df.glint", "df.up", "df.down"}
		for _, g := range Kinds {
			names = append(names, g)
		}
		for _, n := range names {
			if all[n] == "" {
				t.Errorf("%s set lacks %s", set, n)
			}
		}
	}
}

func TestSeason(t *testing.T) {
	cases := map[string]string{
		"2026-10-28": "Late Autumn 2026", "2026-10-10": "Mid Autumn 2026",
		"2026-09-05": "Early Autumn 2026", "2026-11-30": "Late Autumn 2026",
		"2026-12-05": "Early Winter 2026", "2027-01-10": "Mid Winter 2027",
		"2027-01-25": "Late Winter 2027", "2027-02-14": "Late Winter 2027",
		"2027-03-01": "Early Spring 2027", "2027-04-21": "Late Spring 2027",
		"2027-06-15": "Early Summer 2027", "2027-07-04": "Mid Summer 2027",
		"2027-08-31": "Late Summer 2027",
	}
	for day, want := range cases {
		if got := Season(ts(day + "T12:00:00Z")); got != want {
			t.Errorf("Season(%s) = %q, want %q", day, got, want)
		}
	}
}

func TestAge(t *testing.T) {
	if Age(time.Time{}, now) != 0 {
		t.Error("unknown is 0")
	}
	if a := Age(now.Add(-36*time.Hour), now); math.Abs(a-1.5) > 1e-9 {
		t.Errorf("36h = %v days", a)
	}
	if Age(now.Add(time.Hour), now) != 0 {
		t.Error("the future is 0")
	}
}

func TestSGR(t *testing.T) {
	if s := SGR("yellow", false, false, theme.Color256); !strings.Contains(s, "38;5;226") || !strings.Contains(s, "48;5;16") {
		t.Errorf("256 yellow = %q", s)
	}
	if s := SGR("yellow", false, false, theme.Color16); !strings.Contains(s, "33") || !strings.HasSuffix(s, ";1m") {
		t.Errorf("16 yellow = %q", s)
	}
	if s := SGR("blue", false, false, theme.Color16); strings.Contains(s, ";1") {
		t.Errorf("16 blue is not bold: %q", s)
	}
	if s := SGR("lred", false, false, theme.TrueColor); !strings.Contains(s, "38;2;255;0;0") {
		t.Errorf("truecolor lred = %q", s)
	}
	if s := SGR("yellow", false, false, theme.Mono); s != "\x1b[1m" {
		t.Errorf("mono yellow = %q", s)
	}
	if s := SGR("brown", false, false, theme.Mono); s != "\x1b[2m" {
		t.Errorf("mono brown = %q", s)
	}
	if s := SGR("lgray", false, false, theme.Mono); s != "\x1b[0m" {
		t.Errorf("mono lgray = %q", s)
	}
	if s := SGR("yellow", true, true, theme.Mono); s != "\x1b[1;7m" {
		t.Errorf("mono cursor = %q", s)
	}
	if s := SGR("nonsense", false, false, theme.Color256); !strings.Contains(s, "38;5;250") {
		t.Errorf("unknown is lgray: %q", s)
	}
}

func TestTerrain(t *testing.T) {
	glyphs.Use(glyphs.ASCII)
	defer glyphs.Use(glyphs.Nerd)
	if g, c := Terrain('w', 0, 0, 0, true); g != "~" || (c != "blue" && c != "lblue") {
		t.Errorf("water = %q %s", g, c)
	}
	if g, c := Terrain('-', 1, 1, 0, true); g != "-" || c != "dgray" {
		t.Errorf("street = %q %s", g, c)
	}
	if g, c := Terrain('=', 1, 1, 0, true); g != "=" || c != "brown" {
		t.Errorf("bridge = %q %s", g, c)
	}
	if g, c := Terrain('?', 1, 1, 0, true); g != " " || c != "black" {
		t.Errorf("unknown = %q %s", g, c)
	}
	// the hash is stable per cell
	g1, c1 := Terrain('o', 7, 9, 0, true)
	g2, c2 := Terrain('o', 7, 9, 3, false)
	if g1 != g2 || c1 != c2 {
		t.Error("wild land flickers")
	}
}

func newTestView(t *testing.T, in Input) *View {
	t.Helper()
	v := NewView("manhattan")
	v.Clock = func() time.Time { return now }
	v.Build = func() ([]*Item, []Lane) {
		return Items(in), []Lane{{Repo: infra}, {Repo: portal, Label: "portal"}}
	}
	v.Open("manhattan")
	v.Prepare(30, 120, "g1", true)
	return v
}

func TestViewCursorAndKeys(t *testing.T) {
	v := newTestView(t, fixtureInput())
	// the youngest PR awaiting review most wants a look (urgency 2,
	// ahead of the one on fire at 3)
	under := v.Under()
	if len(under) == 0 || under[0].ID != PRID(infra, 4002) {
		t.Fatalf("the cursor starts on the youngest PR awaiting review, not %+v", under)
	}
	cur := v.Cur
	v.Move(0, 1)
	if v.Cur != (Cell{cur.R, cur.C + 1}) {
		t.Errorf("l moved to %v", v.Cur)
	}
	v.Move(-999, -999)
	if v.Cur != (Cell{0, 0}) {
		t.Errorf("clamped to %v", v.Cur)
	}
	v.Cur = cur
	v.Next(+1)
	if v.Cur == cur || len(v.Under()) == 0 {
		t.Errorf("tab stayed at %v", v.Cur)
	}
	v.Next(-1)
	if v.Cur != cur {
		t.Errorf("shift-tab went to %v, not back to %v", v.Cur, cur)
	}
	// zooming keeps the cursor on its item
	v.SetZoom(ZoomStreet)
	if v.Zoom != ZoomStreet || v.Map.UnitsPerRow() != 1 {
		t.Errorf("zoom = %s, %v per row", v.Zoom, v.Map.UnitsPerRow())
	}
	if u := v.Under(); len(u) == 0 || u[0].ID != PRID(infra, 4002) {
		t.Errorf("after zoom the cursor is on %+v", u)
	}
	v.SetZoom(ZoomFit)
	if u := v.Under(); v.Zoom != ZoomFit || len(u) == 0 || u[0].ID != PRID(infra, 4002) {
		t.Errorf("after fit the cursor is on %+v", u)
	}
	// the other city, and the log remembers the arrivals
	v.Open(OtherCity(v.City))
	v.Prepare(30, 120, "g1", true)
	if v.City != "chicago" || v.Map.Key != "chicago" {
		t.Errorf("city = %s", v.City)
	}
	if n := len(v.Log); n != 2 || !strings.Contains(v.Log[1].Msg, "Chicago") || v.Log[1].When != "12:00" {
		t.Errorf("log = %+v", v.Log)
	}
}

func TestViewAnnouncements(t *testing.T) {
	in := fixtureInput()
	v := newTestView(t, in)
	v.Log = nil
	// a new PR and a new issue arrive, one PR leaves, one catches fire
	in.PRs = in.PRs[1:]
	in.PRs = append(in.PRs, prFixture(4100, "2026-10-28T00:00:00Z"))
	in.Issues = append(in.Issues, &model.Issue{Repo: infra, Number: 3999, Title: "new", CreatedAt: now})
	states := in.PRState
	in.PRState = func(pr *model.PR) PRState {
		if pr.Number == 4002 {
			return PRState{Label: model.LabelAwaiting, NeedsYou: true, CI: model.CICluster{Glyphs: "x", Failed: []string{"lint"}}}
		}
		return states(pr)
	}
	v.Build = func() ([]*Item, []Lane) { return Items(in), []Lane{{Repo: infra}, {Repo: portal}} }
	v.Prepare(30, 120, "g2", true)
	var msgs []string
	for _, e := range v.Log {
		msgs = append(msgs, e.Msg)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"Migrants have arrived: infra#4100", "A goblin is sighted: infra#3999",
		"A dwarf has caught fire: infra#4002 CI failing", "infra#4000 has left the fortress"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log lacks %q:\n%s", want, joined)
		}
	}
	if len(v.Log) != 4 {
		t.Errorf("the log keeps four, has %d", len(v.Log))
	}
	// the first landing of the branches layer is not news
	v.Log = nil
	v.Prepare(30, 120, "g3", false)
	v.Prepare(30, 120, "g4", true)
	if len(v.Log) != 0 {
		t.Errorf("the layer landing announced: %+v", v.Log)
	}
}

func TestViewDraws(t *testing.T) {
	glyphs.Use(glyphs.ASCII)
	defer glyphs.Use(glyphs.Nerd)
	v := newTestView(t, fixtureInput())
	c := canvas.New(120, 30)
	v.Draw(c, 30, 120, Frame{Still: true, Tier: theme.Mono, Season: "Late Autumn 2026"})
	screen := strings.Join(c.Lines(), "\n")
	for _, want := range []string{"Fortress of Manhattan", "Late Autumn 2026", "dwarves", "legend", "PR awaiting review",
		"street = days open", "Houston St = today", "not to scale", "lanes W to E: 1 infra 2 portal", "look",
		"infra#4002", "awaiting review", "by bob", "3 days old", "Strike the earth", "14th", "HUDSON"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks %q:\n%s", want, screen)
		}
	}
	// the cursor sits on the item (X over the dwarf)
	line := []rune(c.Text(2 + v.Cur.R - v.Top))
	if x := Margin + v.Cur.C - v.Left; line[x] != 'X' {
		t.Errorf("cursor cell is %q", line[x])
	}
	// the draft stands on its cell as its own glyph
	for _, it := range v.Items {
		if it.Kind == KindDraft && it.Placed {
			y, x := 2+it.Cell.R-v.Top, Margin+it.Cell.C-v.Left
			if g := []rune(c.Text(y))[x]; g != 'o' {
				t.Errorf("draft cell is %q", g)
			}
		}
	}
	// zoomed in, the scale note changes and the edge counters appear
	v.SetZoom(ZoomStreet)
	v.Cur = Cell{v.Map.Rows / 2, v.Cur.C}
	c = canvas.New(120, 30)
	v.Draw(c, 30, 120, Frame{Still: true, Tier: theme.Mono, Season: "x"})
	screen = strings.Join(c.Lines(), "\n")
	if !strings.Contains(screen, "1 row = 1 day") || !strings.Contains(screen, "more") {
		t.Errorf("zoomed screen:\n%s", screen)
	}
	// too small says what it needs
	c = canvas.New(50, 18)
	v.Draw(c, 18, 50, Frame{Still: true, Tier: theme.Mono})
	if !strings.Contains(strings.Join(c.Lines(), "\n"), "at least 60x20") {
		t.Error("a small terminal says nothing")
	}
}

func TestViewLookAtTerrain(t *testing.T) {
	glyphs.Use(glyphs.ASCII)
	defer glyphs.Use(glyphs.Nerd)
	v := newTestView(t, fixtureInput())
	// park the cursor on an empty cell of the island, on a street of some age
	found := false
	for r := 0; r < v.Map.Rows && !found; r++ {
		for c := 0; c < v.Map.Cols; c++ {
			if v.Map.IsMain(r, c) && v.Map.RowY(r) > 100 {
				if _, taken := v.Cells[Cell{r, c}]; !taken {
					v.Cur, found = Cell{r, c}, true
					break
				}
			}
		}
	}
	if !found {
		t.Fatal("no empty island cell")
	}
	c := canvas.New(120, 30)
	v.Draw(c, 30, 120, Frame{Still: true, Tier: theme.Mono})
	screen := strings.Join(c.Lines(), "\n")
	if !strings.Contains(screen, "Manhattan") || !strings.Contains(screen, "days old here") {
		t.Errorf("look at terrain:\n%s", screen)
	}
}
