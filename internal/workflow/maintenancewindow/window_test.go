package maintenancewindow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/wftest"
)

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
)

const (
	pid  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pidC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	bot  = "infra-bot"
)

// testCfg is the acme fixture: manifests on acme/infra, the conductor
// infra-bot, the default New York window labelled ET.
func testCfg() *config.Config {
	cfg := config.Default()
	cfg.Reviews.Repos = []model.Repo{infra}
	mw := config.DefaultMaintenanceWindow()
	mw.TZLabel = "ET"
	mw.ManifestTitle = "Window {night} 23:00 ET"
	mw.ManifestRepo = infra
	mw.ConductorBot = bot
	cfg.MaintenanceWindow = mw
	ta := config.DefaultTerraformApprove()
	ta.DigestBot = bot
	cfg.TerraformApprove = ta
	return cfg
}

func at(iso string) time.Time {
	t, err := clock.ParseUTC(iso)
	if err != nil {
		panic(err)
	}
	return t
}

func check(t *testing.T, name string, cond bool) {
	t.Helper()
	if !cond {
		t.Errorf("FAIL - %s", name)
	}
}

func boolp(b bool) *bool { return &b }

func markerComment(night string, revertible bool, queuedAt, login string) model.Comment {
	m, _ := json.Marshal(map[string]any{"comment_id": 1, "plan_id": pid, "night": night, "revertible": revertible,
		"disruptive": true, "slow_types": []string{}, "queued_at": queuedAt})
	return model.Comment{Author: model.Actor{Login: login}, CreatedAt: at(queuedAt),
		Body: "**Queued for the maintenance window** …\n\n<!-- window-queue " + string(m) + " -->"}
}

func botC(body, iso string) model.Comment {
	return model.Comment{Author: model.Actor{Login: bot}, CreatedAt: at(iso), Body: body}
}

func human(login, body, iso string) model.Comment {
	return model.Comment{Author: model.Actor{Login: login}, CreatedAt: at(iso), Body: body}
}

var digestA = botC("### plan `nonprod/15-window-canary`\n\nPlan-id: `"+pid+"`", "2026-10-21T16:20:00Z")

func labelled(names ...string) model.Labels {
	var l model.Labels
	for _, n := range names {
		l.Nodes = append(l.Nodes, model.Label{Name: n})
	}
	return l
}

// qpr is a queued PR with a marker and a stack.
func qpr(number int, labels []string, marker map[string]any, stack string) *model.PR {
	pr := &model.PR{Repo: infra, Number: number, Title: fmt.Sprintf("PR %d", number), Labels: labelled(labels...)}
	info := &Info{Stack: stack}
	if marker != nil {
		raw, _ := json.Marshal(marker)
		info.Marker = ParseMarker([]model.Comment{{Author: model.Actor{Login: bot}, Body: "<!-- window-queue " + string(raw) + " -->"}}, testCfg().MaintenanceWindow)
	}
	pr.Set(Ext, info)
	return pr
}

func manifestIssue(number int, m map[string]any, title string, labels ...string) *model.Issue {
	body := "x"
	if m != nil {
		raw, _ := json.Marshal(m)
		body = "Maintenance window …\n\n<!-- window-manifest:begin\n" + string(raw) + "\nwindow-manifest:end -->\n"
	}
	return &model.Issue{Repo: infra, Number: number, Title: title, Body: body, Labels: labelled(labels...),
		Author: model.Actor{Login: bot}}
}

func manifestFor(number int, night, phase string, items []map[string]any, attendees []string, comments ...model.Comment) *model.Issue {
	m := map[string]any{"night": night, "phase": phase, "items": items, "notes": []string{}}
	if attendees != nil {
		m["attendees"] = attendees
	}
	i := manifestIssue(number, m, "Window "+night+" 23:00 ET", "window-manifest")
	i.Comments.Nodes = comments
	return i
}

func rowShape(rows []workflow.Row) []string {
	var out []string
	for _, r := range rows {
		switch v := r.Item.(type) {
		case string:
			out = append(out, "header:"+strings.TrimSpace(v))
		case *model.PR:
			out = append(out, fmt.Sprintf("%s:%d", r.Kind, v.Number))
		case *model.Issue:
			out = append(out, fmt.Sprintf("%s:%d", r.Kind, v.Number))
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func segsText(segs []text.Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// ---- night maths ----

func TestNightMaths(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	nq := func(iso string) string { return NextQueueNight(at(iso), cfg) }
	check(t, "10:00 EDT queues for tonight", nq("2026-10-21T14:00:00Z") == "2026-10-21")
	check(t, "20:59 EDT still tonight", nq("2026-10-22T00:59:00Z") == "2026-10-21")
	check(t, "21:00 EDT (cutoff) -> tomorrow", nq("2026-10-22T01:00:00Z") == "2026-10-22")
	check(t, "00:30 EDT next day -> that day", nq("2026-10-22T04:30:00Z") == "2026-10-22")
	check(t, "20:59 EST (first EST night)", nq("2026-11-02T01:59:00Z") == "2026-11-01")
	check(t, "21:00 EST -> next day", nq("2026-11-02T02:00:00Z") == "2026-11-02")

	w2u := func(night string, hm clock.HM) time.Time {
		t, err := WallToUTC(night, hm, cfg)
		if err != nil {
			panic(err)
		}
		return t
	}
	check(t, "cutoff EDT 10-21 = 01:00Z", w2u("2026-10-21", clock.HM{H: 21}).Equal(at("2026-10-22T01:00:00Z")))
	check(t, "start EST 11-02 = 04:00Z", w2u("2026-11-02", clock.HM{H: 23}).Equal(at("2026-11-03T04:00:00Z")))
	check(t, "start EDT 2027-03-14 = 03:00Z", w2u("2027-03-14", clock.HM{H: 23}).Equal(at("2027-03-15T03:00:00Z")))
	_, err := WallToUTC("not-a-date", clock.HM{H: 23}, cfg)
	check(t, "a malformed night is an error", err != nil)

	check(t, "after the cutoff a plain approve queues for tomorrow night",
		QueueNightPhrase(at("2026-10-23T01:30:00Z"), cfg) == "tomorrow night's" && QueueNightPhrase(at("2026-10-22T22:00:00Z"), cfg) == "tonight's")
	check(t, "in the release window: 23:05 yes, 15:00 no, 22:59 yes, 23:30 no",
		InReleaseWindow(at("2026-10-22T03:05:00Z"), cfg) && !InReleaseWindow(at("2026-10-21T19:00:00Z"), cfg) &&
			InReleaseWindow(at("2026-10-22T02:59:00Z"), cfg) && !InReleaseWindow(at("2026-10-22T03:30:00Z"), cfg))
	check(t, "countdown to the start", Countdown("2026-10-21", at("2026-10-22T00:57:00Z"), cfg) == "starts in 2h03m" &&
		Countdown("2026-10-21", at("2026-10-22T03:00:00Z"), cfg) == "")
	check(t, "night labels", NightLabel("2026-10-21", at("2026-10-21T22:00:00Z"), cfg) == "window 2026-10-21 23:00 ET, tonight" &&
		NightLabel("2026-10-22", at("2026-10-21T22:00:00Z"), cfg) == "window 2026-10-22 23:00 ET, tomorrow night" &&
		NightLabel("2026-10-24", at("2026-10-21T22:00:00Z"), cfg) == "window 2026-10-24 23:00 ET, Sat")

	other := config.DefaultMaintenanceWindow()
	other.Timezone, other.Start, other.Cutoff = "Europe/London", clock.HM{H: 22}, clock.HM{H: 20}
	other.TZLabel = "UK"
	check(t, "another zone and start follow the config", NextQueueNight(at("2026-10-21T19:30:00Z"), other) == "2026-10-22" &&
		NightLabel("2026-10-21", at("2026-10-21T12:00:00Z"), other) == "window 2026-10-21 22:00 UK, tonight")
}

// ---- markers and manifests ----

func TestMarkersAndManifests(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	mk := ParseMarker([]model.Comment{markerComment("2026-10-20", true, "2026-10-21T16:31:00Z", bot),
		markerComment("2026-10-21", true, "2026-10-21T16:31:00Z", bot)}, cfg)
	check(t, "latest bot marker wins", mk != nil && mk.Night == "2026-10-21" && mk.PlanID == pid && mk.Revertible != nil && *mk.Revertible)
	check(t, "a marker from a human is ignored", ParseMarker([]model.Comment{markerComment("2026-10-21", true, "2026-10-21T16:31:00Z", "mallory")}, cfg) == nil)
	check(t, "malformed marker ignored", ParseMarker([]model.Comment{{Author: model.Actor{Login: bot}, Body: "<!-- window-queue {not json} -->"}}, cfg) == nil)
	check(t, "non-dict marker JSON ignored", ParseMarker([]model.Comment{{Author: model.Actor{Login: bot}, Body: "<!-- window-queue [1, 2] -->"}}, cfg) == nil)
	check(t, "stack from the newest digest", LatestPlanStack([]model.Comment{digestA}, nil) == "nonprod/15-window-canary")
	nochg := botC("### plan `prod/12-other` — no changes\nPlan ran green.", "2026-10-21T16:25:00Z")
	check(t, "queued stack: the newest real digest, not a no-changes notice", LatestPlanStack([]model.Comment{digestA, nochg}, nil) == "nonprod/15-window-canary")
	check(t, "queued stack: the marker's own stacks first, joined like the conductor",
		LatestPlanStack([]model.Comment{nochg}, &Marker{Stacks: []string{"nonprod/10-b", "prod/10-a"}}) == "nonprod/10-b + prod/10-a")

	mans := ParseManifests([]*model.Issue{manifestFor(3545, "2026-10-21", "frozen", []map[string]any{{"pr": 3544}}, nil)}, cfg)
	check(t, "manifest parsed by night", len(mans) == 1 && mans["2026-10-21"] != nil && len(mans["2026-10-21"].Items) == 1 && mans["2026-10-21"].Items[0].PR == 3544)
	check(t, "issue without a block is ignored", len(ParseManifests([]*model.Issue{{Title: "Window 2026-10-21 23:00 ET", Body: "no block"}}, cfg)) == 0)
	noNight := manifestIssue(3546, map[string]any{"items": []any{}}, "Window 2026-10-23 23:00 ET")
	check(t, "a block without a night reads the title", ParseManifests([]*model.Issue{noNight}, cfg)["2026-10-23"] != nil)
	dup := ParseManifests([]*model.Issue{
		manifestIssue(3551, map[string]any{"night": "2026-10-22", "items": []any{}, "phase": "dup"}, "x"),
		manifestIssue(3550, map[string]any{"night": "2026-10-22", "items": []any{}, "phase": "first"}, "x")}, cfg)
	check(t, "duplicate manifests: lowest issue number wins", dup["2026-10-22"].Issue == 3550 && dup["2026-10-22"].Phase == "first")

	mi := func(number int, night string, labels ...string) *model.Issue {
		return &model.Issue{Repo: infra, Number: number, Title: "Window " + night + " 23:00 ET", Labels: labelled(labels...)}
	}
	check(t, "manifest night: label + title", ManifestIssueNight(mi(3891, "2026-10-26", "window-manifest", "do-not-groom", "groomed"), cfg) == "2026-10-26")
	check(t, "manifest night: the title alone is not enough", ManifestIssueNight(mi(1, "2026-10-26", "bug"), cfg) == "")
	odd := mi(2, "2026-10-26", "window-manifest")
	odd.Title = "Window tonight"
	check(t, "manifest night: the label alone is not enough", ManifestIssueNight(odd, cfg) == "")
	custom := testCfg().MaintenanceWindow
	custom.ManifestTitle, custom.ManifestLabel = "Nightly {night}", "nightly"
	check(t, "the manifest title and label follow the config",
		ManifestIssueNight(&model.Issue{Title: "Nightly 2026-10-26", Labels: labelled("nightly")}, custom) == "2026-10-26" &&
			ManifestIssueNight(mi(3, "2026-10-26", "window-manifest"), custom) == "")
}

// ---- grouping and status ----

func fixtureManifests() map[string]*Manifest {
	return map[string]*Manifest{
		"2026-10-21": {Night: "2026-10-21", Items: []Item{{PR: 3544, Flag: "scheduled", StartUTC: "2026-10-22T03:04:00Z", Status: "scheduled"}}},
		"2026-10-20": {Night: "2026-10-20", Items: []Item{{PR: 3500, Flag: "attended-only", Reasons: []string{"not revertible and no attendee"}, Status: "attended-only"}}},
	}
}

func TestPlacementAndRows(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	now := at("2026-10-21T22:00:00Z") // 18:00 EDT, before the cutoff
	scheduled := qpr(3544, []string{"window:queued", "window:scheduled"}, map[string]any{"night": "2026-10-21", "revertible": true, "queued_at": "2026-10-21T16:31:00Z"}, "nonprod/x")
	tomorrow := qpr(3600, []string{"window:queued"}, map[string]any{"night": "2026-10-22", "revertible": false, "queued_at": "2026-10-22T02:00:00Z"}, "nonprod/x")
	rolled := qpr(3500, []string{"window:queued", "window:rolled"}, map[string]any{"night": "2026-10-20", "revertible": true, "queued_at": "2026-10-20T15:00:00Z"}, "nonprod/x")
	plain := qpr(3543, []string{"window:queued"}, map[string]any{"night": "2026-10-21", "revertible": true, "queued_at": "2026-10-21T16:35:00Z"}, "nonprod/x")
	manifests := fixtureManifests()

	rows := QueueRows([]*model.PR{tomorrow, rolled, plain, scheduled}, nil, manifests, now, nil, nil, cfg)
	shape := rowShape(rows)
	check(t, "section header counts all four", shape[0] == "header:queued for the maintenance window (4)")
	var headers []string
	for _, r := range rows[1:] {
		if r.Kind == "header" {
			headers = append(headers, r.Item.(string))
		}
	}
	check(t, "tonight's sub-header comes first", len(headers) == 2 && strings.Contains(headers[0], "2026-10-21 23:00 ET, tonight"))
	check(t, "tomorrow's sub-header second", len(headers) == 2 && strings.Contains(headers[1], "2026-10-22 23:00 ET, tomorrow night"))
	check(t, "sub-headers carry their night", rows[1].Meta[workflow.NightRow] == "2026-10-21")
	var order []string
	for _, s := range shape {
		if strings.HasPrefix(s, "queued:") {
			order = append(order, s)
		}
	}
	check(t, "tonight: scheduled first, then by queue time; tomorrow last", eq(order, []string{"queued:3544", "queued:3500", "queued:3543", "queued:3600"}))
	check(t, "scheduled shows its ET slot", InfoOf(scheduled).Status == "scheduled 23:04 ET")
	check(t, "plain queued", InfoOf(plain).Status == "queued")
	check(t, "rolled shows the reason", InfoOf(rolled).Status == "rolled: not revertible and no attendee")
	check(t, "each PR knows its night", InfoOf(scheduled).Night == "2026-10-21" && InfoOf(tomorrow).Night == "2026-10-22")
	p, _ := Place(rolled, manifests, now, cfg)
	check(t, "a rolled item from a past night moves to the next night", p.Night == "2026-10-21")

	late := at("2026-10-22T01:30:00Z") // 21:30 EDT
	p, _ = Place(plain, manifests, late, cfg)
	check(t, "after the cutoff an unscheduled item shows tomorrow", p.Night == "2026-10-22")
	p, _ = Place(scheduled, manifests, late, cfg)
	check(t, "after the cutoff a scheduled item stays tonight", p.Night == "2026-10-21")
	p, _ = Place(qpr(1, []string{"window:queued"}, nil, ""), map[string]*Manifest{}, now, cfg)
	check(t, "no marker at all falls back to the next night", p.Night == "2026-10-21")

	// malformed window data degrades to a placeholder instead of a crash
	bad := qpr(5, []string{"window:queued", "window:scheduled"}, map[string]any{"night": "not-a-date"}, "")
	badMans := map[string]*Manifest{"2026-10-21": {Night: "2026-10-21", Items: []Item{{PR: 5, Flag: "scheduled", StartUTC: "soon"}}}}
	rows = QueueRows([]*model.PR{bad}, nil, badMans, now, nil, nil, cfg)
	check(t, "malformed window data degrades to queued (?)", InfoOf(bad).Status == "queued (?)" && rowShape(rows)[len(rows)-1] == "queued:5")
	_, err := Place(qpr(6, []string{"window:queued"}, map[string]any{"night": "not-a-date"}, ""), map[string]*Manifest{}, now, cfg)
	check(t, "a malformed marker night is an error", err != nil)

	// a held item reads awaiting /release with the ET deadline
	held := qpr(3544, []string{"window:queued", "window:scheduled"}, map[string]any{"night": "2026-10-21"}, "")
	heldMans := map[string]*Manifest{"2026-10-21": {Night: "2026-10-21", Items: []Item{{PR: 3544, Flag: "scheduled", Revertible: boolp(false),
		StartUTC: "2026-10-22T03:00:00Z", ReleaseByUTC: "2026-10-22T03:05:00Z", Status: "waiting for /release until 2026-10-22T03:05:00Z"}}}}
	p, _ = Place(held, heldMans, at("2026-10-22T03:01:00Z"), cfg)
	check(t, "a held item reads awaiting /release with the ET deadline", p.Status == "awaiting /release, 23:05 ET")
}

func TestQueuedPlanState(t *testing.T) {
	c := testCfg()
	cfg, ta := c.MaintenanceWindow, c.TerraformApprove
	queuedA := markerComment("2026-10-22", true, "2026-10-22T16:33:40Z", bot)
	digestB := botC("### plan `prod/09-portal`\n\nPlan-id: `"+pidB+"`\nPlan: 2 to add, 0 to change, 0 to destroy.", "2026-10-22T23:19:44Z")
	digestAold := digestA
	digestAold.CreatedAt = at("2026-10-22T16:26:18Z")
	head := func(committed, oid string) *model.PR {
		pr := &model.PR{Repo: infra, Number: 3609, HeadRefOid: oid}
		pr.Commits.Nodes = append(pr.Commits.Nodes, struct {
			Commit model.Commit `json:"commit"`
		}{model.Commit{CommittedDate: at(committed)}})
		return pr
	}
	hd := head("2026-10-22T23:18:45Z", "cccccccccccc")
	mkA := ParseMarker([]model.Comment{queuedA}, cfg)
	check(t, "queued plan re-planned by a later push: re-planned", QueuedPlanState(mkA, []model.Comment{digestAold, queuedA, digestB}, hd, ta) == FateReplanned)
	approveB := human("alice", `/approve `+pidB+` --outside-window="restore the broken route now"`, "2026-10-22T23:34:58Z")
	check(t, "then the new plan approved outside the window: superseded", QueuedPlanState(mkA, []model.Comment{digestAold, queuedA, digestB, approveB}, hd, ta) == FateSuperseded)
	check(t, "queued plan still the live plan: queued", QueuedPlanState(mkA, []model.Comment{digestAold, queuedA}, head("2026-10-22T16:00:00Z", ""), ta) == FateQueued)
	check(t, "no plan in reach: queued", QueuedPlanState(mkA, []model.Comment{queuedA}, hd, ta) == FateQueued)
	check(t, "a marker with no plan-id: queued", QueuedPlanState(&Marker{Night: "2026-10-22"}, []model.Comment{digestB}, hd, ta) == FateQueued)
	meta, _ := json.Marshal(map[string]any{"set_id": pidC, "head_sha": "cccccccccccc",
		"members": []map[string]any{{"stack": "prod/09-portal", "plan_id": pidB, "destroys": 0}}})
	setmeta := botC("### plan-set\n<!-- plan-set: "+string(meta)+" -->", "2026-10-22T23:20:00Z")
	check(t, "queued as the current head's plan-set id: queued", QueuedPlanState(&Marker{PlanID: pidC}, []model.Comment{digestB, setmeta}, hd, ta) == FateQueued)

	requeue := qpr(3609, []string{"window:queued"}, map[string]any{"night": "2026-10-22", "plan_id": pid}, "")
	InfoOf(requeue).Stale, InfoOf(requeue).Reapprove = true, true
	check(t, "a stale entry is not window-queued", !IsQueued(requeue, cfg) && HasQueuedLabel(requeue, cfg))
	check(t, "a stale entry asks for a re-approve", InfoOf(requeue).NeedsReapprove())
	movedOn := qpr(3609, []string{"window:queued"}, map[string]any{"night": "2026-10-22", "plan_id": pid}, "")
	InfoOf(movedOn).Stale = true
	check(t, "superseded: out of the queue, no re-approve", !IsQueued(movedOn, cfg) && !InfoOf(movedOn).NeedsReapprove())
	check(t, "no annotation yet: the label alone queues", IsQueued(&model.PR{Labels: labelled("window:queued")}, cfg))
}

// ---- the w key: attend, release, cancel ----

const night = "2026-10-21"

func TestWindowNights(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	wn := func(me string, issues ...*model.Issue) map[string]*Night { return WindowNights(issues, me, cfg) }
	nights := wn("alice", manifestFor(3600, night, "open", nil, nil,
		human("alice", "/attend", "2026-10-21T18:00:00Z"),
		botC("@alice is attending the night of 2026-10-21. Post `/release` here at 23:00 ET to check in.", "2026-10-21T18:00:40Z"),
		human("mallory", "/attend", "2026-10-21T18:05:00Z"),
		botC("@mallory: only members of @acme/release-approvers can run window commands.", "2026-10-21T18:05:30Z")))
	check(t, "before the freeze, attendees are the confirmed attends", eq(nights[night].Attendees, []string{"alice"}))
	check(t, "my confirmed attend reads attending", nights[night].Mine.Attend == StateAttending)
	check(t, "someone else's refusal isn't mine",
		wn("alice", manifestFor(3600, night, "open", nil, nil, human("mallory", "/attend", "2026-10-21T18:05:00Z")))[night].Mine.Attend == "")
	mal := wn("mallory", manifestFor(3600, night, "open", nil, nil,
		human("mallory", "/attend", "2026-10-21T18:05:00Z"),
		botC("@mallory: only members of @acme/release-approvers can run window commands.", "2026-10-21T18:05:30Z")))[night]
	check(t, "a non-member's refusal lands on their last command", mal.Mine.Attend == StateNotMember && len(mal.Attendees) == 0)
	check(t, "posted, no reply yet", wn("alice", manifestFor(3600, night, "open", nil, nil, human("alice", "/attend", "2026-10-21T18:00:00Z")))[night].Mine.Attend == StatePosted)
	early := wn("alice", manifestFor(3600, night, "frozen", nil, []string{"alice"},
		human("alice", "/release", "2026-10-22T02:50:00Z"),
		botC("`/release` counts from 23:00 ET; post it again then.", "2026-10-22T02:50:30Z")))[night]
	check(t, "an early release reads early (the reply names nobody)", early.Mine.Release == StateEarly)
	done := wn("alice", manifestFor(3600, night, "running", nil, []string{"Alice"},
		human("alice", "/release", "2026-10-22T03:00:10Z"),
		botC("Checked in: @alice. Non-revertible items may start.", "2026-10-22T03:00:40Z")))[night]
	check(t, "after the freeze, attendees are the frozen list (lower-cased)", eq(done.Attendees, []string{"alice"}))
	check(t, "checked in", done.Mine.Release == StateCheckedIn && len(done.CheckedIn) == 1 && done.CheckedIn[0].Login == "alice" &&
		done.CheckedIn[0].At.Equal(at("2026-10-22T03:00:40Z")))
	dup := wn("alice", manifestFor(3601, night, "open", nil, nil), manifestFor(3600, night, "open", nil, nil))
	check(t, "two issues for a night: the lowest number wins", dup[night].Issue == 3600)
	check(t, "attendance text", AttendanceText(done, cfg) == "running; attending: @alice; checked in: @alice 23:00 ET" &&
		AttendanceText(&Night{}, cfg) == "open; attending: nobody")

	custom := testCfg().MaintenanceWindow
	custom.AttendCommand, custom.ReleaseCommand = "/here", "/go"
	n := WindowNights([]*model.Issue{manifestFor(3600, night, "open", nil, nil,
		human("alice", "/here", "2026-10-21T18:00:00Z"),
		botC("`/here` from @alice is after the cutoff for the night of 2026-10-21.", "2026-10-21T18:00:40Z"))}, "alice", custom)[night]
	check(t, "the conductor's replies follow the configured commands", n.Mine.Attend == StateLate)
}

func TestReleaseSpanAndCommandChecks(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	held := Item{PR: 1, Flag: "scheduled", Revertible: boolp(false), StartUTC: "2026-10-22T03:00:00Z", ReleaseByUTC: "2026-10-22T03:05:00Z"}
	later := Item{PR: 2, Flag: "scheduled", Revertible: boolp(false), StartUTC: "2026-10-22T03:09:00Z", ReleaseByUTC: "2026-10-22T03:14:00Z"}
	old := Item{PR: 3, Flag: "scheduled", Revertible: boolp(false), StartUTC: "2026-10-22T03:02:00Z"}
	rs := func(items ...Item) (time.Time, time.Time) { return ReleaseSpan(&Night{Items: items}, night, cfg) }
	from, until := rs()
	check(t, "release counts from 22:59 EDT", from.Equal(at("2026-10-22T02:59:00Z")) && until.IsZero())
	revertible := held
	revertible.Revertible = boolp(true)
	_, until = rs(revertible)
	check(t, "no held item: no deadline", until.IsZero())
	_, until = rs(later, held)
	check(t, "the first held item's release_by", until.Equal(at("2026-10-22T03:05:00Z")))
	_, until = rs(old, later)
	check(t, "an older manifest falls back to the slot", until.Equal(at("2026-10-22T03:02:00Z")))
	attended := held
	attended.Flag = "attended-only"
	_, until = rs(attended)
	check(t, "an attended-only (rolled) item sets no deadline", until.IsZero())

	cc := func(cmd string, info *Night, me, iso string, item *Item) (bool, string) {
		return CommandCheck(cmd, info, night, me, at(iso), item, cfg)
	}
	openN := WindowNights([]*model.Issue{manifestFor(3600, night, "open", nil, nil)}, "alice", cfg)[night]
	ok, why := cc(CmdAttend, openN, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "/attend at 15:00 EDT: ok, 6h to the cutoff", ok && strings.Contains(why, "6h00m") && strings.Contains(why, "23:00 ET") && strings.Contains(why, "21:00 ET"))
	ok, why = cc(CmdAttend, openN, "alice", "2026-10-22T01:00:00Z", nil)
	check(t, "/attend at 21:00 EDT: past the cutoff", !ok && strings.Contains(why, "past the 21:00 ET cutoff"))
	attending := WindowNights([]*model.Issue{manifestFor(3600, night, "open", nil, nil,
		human("alice", "/attend", "2026-10-21T18:00:00Z"),
		botC("@alice is attending the night of 2026-10-21.", "2026-10-21T18:00:40Z"))}, "alice", cfg)[night]
	ok, _ = cc(CmdAttend, attending, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "/attend twice: refused", !ok)
	posted := WindowNights([]*model.Issue{manifestFor(3600, night, "open", nil, nil, human("alice", "/attend", "2026-10-21T18:00:00Z"))}, "alice", cfg)[night]
	ok, why = cc(CmdAttend, posted, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "/attend posted, no reply: wait", !ok && strings.Contains(why, "is posted"))
	frozen := WindowNights([]*model.Issue{manifestFor(3600, night, "frozen",
		[]map[string]any{{"pr": 1, "flag": "scheduled", "revertible": false, "start_utc": "2026-10-22T03:00:00Z", "release_by_utc": "2026-10-22T03:05:00Z"}},
		[]string{"alice"})}, "alice", cfg)[night]
	ok, why = cc(CmdRelease, frozen, "alice", "2026-10-22T02:58:00Z", nil)
	check(t, "/release at 22:58: too early", !ok && strings.Contains(why, "too early") && strings.Contains(why, "22:59 ET"))
	ok, why = cc(CmdRelease, frozen, "alice", "2026-10-22T02:59:00Z", nil)
	check(t, "/release at 22:59: ok, names the deadline", ok && strings.Contains(why, "23:05 ET"))
	ok, why = cc(CmdRelease, frozen, "alice", "2026-10-22T03:06:00Z", nil)
	check(t, "/release after the deadline: ok, says it stopped waiting", ok && strings.Contains(why, "stopped waiting at 23:05 ET"))
	ok, why = cc(CmdRelease, frozen, "mallory", "2026-10-22T03:00:00Z", nil)
	check(t, "/release from a non-attendee: refused, names who attends", !ok && strings.Contains(why, "@alice"))
	ok, why = cc(CmdRelease, frozen, "alice", "2026-10-22T03:30:00Z", nil)
	check(t, "/release after 23:30: the window closed", !ok && strings.Contains(why, "closed at 23:30 ET"))
	done := WindowNights([]*model.Issue{manifestFor(3600, night, "running", nil, []string{"alice"},
		human("alice", "/release", "2026-10-22T03:00:10Z"), botC("Checked in: @alice.", "2026-10-22T03:00:40Z"))}, "alice", cfg)[night]
	ok, _ = cc(CmdRelease, done, "alice", "2026-10-22T03:01:00Z", nil)
	check(t, "/release after checking in: refused", !ok)
	closed := *openN
	closed.Phase = "closed"
	ok, why = cc(CmdAttend, &closed, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "a closed night refuses everything", !ok && strings.Contains(why, "over (closed)"))
	ok, _ = cc(CmdCancel, frozen, "alice", "2026-10-22T02:30:00Z", &held)
	check(t, "/window-cancel before the slot: ok", ok)
	dispatched := held
	dispatched.Status = "dispatched"
	ok, why = cc(CmdCancel, frozen, "alice", "2026-10-22T03:01:00Z", &dispatched)
	check(t, "/window-cancel after the slot: refused", !ok && why == "#1 has already started")
	waiting := held
	waiting.Status = "waiting for /release until 2026-10-22T03:05:00Z"
	ok, _ = cc(CmdCancel, frozen, "alice", "2026-10-22T03:01:00Z", &waiting)
	check(t, "/window-cancel while it waits for /release: ok", ok)
	ok, why = cc(CmdAttend, nil, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "no manifest: refused, says so", !ok && why == "no manifest issue for the night of 2026-10-21 yet")
	ok, why = cc("dance", openN, "alice", "2026-10-21T19:00:00Z", nil)
	check(t, "an unknown command is refused", !ok && strings.Contains(why, "unknown"))
}

// ---- the night header ----

func newApp(cfg *config.Config) (*wftest.App, *Workflow) {
	app := wftest.New(cfg, "alice")
	w := New(cfg.MaintenanceWindow, cfg.TerraformApprove)
	return app, w
}

func TestNightSegs(t *testing.T) {
	cfg := testCfg()
	app, w := newApp(cfg)
	mw := cfg.MaintenanceWindow
	openN := WindowNights([]*model.Issue{manifestFor(3600, night, "open", nil, nil)}, "alice", mw)[night]
	app.SetState(Name, &State{Nights: map[string]*Night{night: openN}, Manifests: map[string]*Manifest{}})
	held := qpr(3544, []string{"window:queued"}, map[string]any{"revertible": false}, "")
	InfoOf(held).Night = night
	app.PRs = []*model.PR{held}
	segs := segsText(w.NightSegs(app, night, at("2026-10-21T19:00:00Z")))
	check(t, "header: a held item with nobody attending asks for /attend", strings.Contains(segs, "needs /attend (w) by 21:00 ET") && strings.Contains(segs, "#3600"))
	segs = segsText(w.NightSegs(app, night, at("2026-10-22T01:30:00Z")))
	check(t, "header: past the cutoff with nobody, it says items roll", strings.Contains(segs, "nobody attending"))
	frozen := WindowNights([]*model.Issue{manifestFor(3600, night, "frozen",
		[]map[string]any{{"pr": 1, "flag": "scheduled", "revertible": false, "start_utc": "2026-10-22T03:00:00Z", "release_by_utc": "2026-10-22T03:05:00Z"}},
		[]string{"alice"})}, "alice", mw)[night]
	app.SetState(Name, &State{Nights: map[string]*Night{night: frozen}, Manifests: map[string]*Manifest{}})
	segs = segsText(w.NightSegs(app, night, at("2026-10-22T03:00:00Z")))
	check(t, "header: at 23:00 asks for the /release with the deadline", strings.Contains(segs, "/release now (w) by 23:05 ET") && strings.Contains(segs, "attending: @alice"))
	done := WindowNights([]*model.Issue{manifestFor(3600, night, "running", nil, []string{"alice"},
		human("alice", "/release", "2026-10-22T03:00:10Z"), botC("Checked in: @alice.", "2026-10-22T03:00:40Z"))}, "alice", mw)[night]
	app.SetState(Name, &State{Nights: map[string]*Night{night: done}, Manifests: map[string]*Manifest{}})
	segs = segsText(w.NightSegs(app, night, at("2026-10-22T03:01:00Z")))
	check(t, "header: checked in", strings.Contains(segs, "checked in: @alice") && !strings.Contains(segs, "/release now"))
	check(t, "an unknown night adds nothing", w.NightSegs(app, "2026-12-25", at("2026-10-22T03:01:00Z")) == nil)

	saved := clock.Now
	defer func() { clock.Now = saved }()
	clock.Now = func() time.Time { return at("2026-10-22T00:57:00Z") }
	extra, more := w.HeaderExtra(app, workflow.Row{Kind: "header", Item: "  x", Meta: map[string]any{workflow.NightRow: night}}, 120)
	check(t, "a night header gets the countdown and the attendance", strings.Contains(extra, "starts in 2h03m") && strings.Contains(segsText(more), "checked in"))
	extra, more = w.HeaderExtra(app, workflow.Row{Kind: "header", Item: "pull requests (2)"}, 120)
	check(t, "a plain header gets nothing", extra == "" && more == nil)
}

// ---- the manifest issue lives under its night ----

func TestManifestRowsAndSections(t *testing.T) {
	cfg := testCfg()
	mw := cfg.MaintenanceWindow
	const mn = "2026-10-26"
	now26 := at("2026-10-27T02:00:00Z") // 22:00 EDT on 10-26
	m3891 := &model.Issue{Repo: infra, Number: 3891, Title: "Window " + mn + " 23:00 ET", Labels: labelled("window-manifest", "do-not-groom", "groomed"), Author: model.Actor{Login: bot}}
	q3922 := qpr(3922, []string{"window:queued", "window:scheduled"}, map[string]any{"night": mn, "revertible": false, "queued_at": "2026-10-26T19:14:00Z"}, "prod/07-warehouse")
	mans26 := map[string]*Manifest{mn: {Night: mn, Phase: "running", Items: []Item{{PR: 3922, Flag: "scheduled", StartUTC: "2026-10-27T03:12:50Z", Status: "scheduled"}}}}
	dupe := &model.Issue{Repo: infra, Number: 3999, Title: "Window " + mn + " 23:00 ET", Labels: labelled("window-manifest")}
	rows := QueueRows([]*model.PR{q3922}, nil, mans26, now26, []*model.Issue{dupe, m3891}, nil, mw)
	check(t, "manifest row sits right under its night's sub-header, before the PRs",
		eq(rowShape(rows), []string{"header:queued for the maintenance window (1)", "header:window 2026-10-26 23:00 ET, tonight", "issue:3891", "queued:3922"}))
	check(t, "the manifest row knows its night and its phase", rows[2].Meta[workflow.NightRow] == mn && rows[2].Meta["label"] == "manifest running")
	rows = QueueRows(nil, nil, mans26, now26, []*model.Issue{m3891}, nil, mw)
	check(t, "a manifest with no queued PR left keeps its night", eq(rowShape(rows), []string{"header:queued for the maintenance window (0)", "header:window 2026-10-26 23:00 ET, tonight", "issue:3891"}))
	check(t, "showAlone false (filtered out) drops a manifest-only night",
		QueueRows(nil, nil, mans26, now26, []*model.Issue{m3891}, func(*model.Issue) bool { return false }, mw) == nil)
	check(t, "nothing queued, no manifest: no section", QueueRows(nil, nil, map[string]*Manifest{}, now26, nil, nil, mw) == nil)

	app, w := newApp(cfg)
	savedNow := clock.Now
	clock.Now = func() time.Time { return now26 }
	t.Cleanup(func() { clock.Now = savedNow })
	plainIssue := &model.Issue{Repo: infra, Number: 3916, Title: "demo tenant", Author: model.Actor{Login: "bob"}}
	app.IssueList = []*model.Issue{m3891, plainIssue}
	app.PRs = []*model.PR{q3922, {Repo: infra, Number: 3601, Title: "normal"}}
	app.SetState(Name, &State{Manifests: mans26, Nights: map[string]*Night{mn: {Issue: 3891}}})
	check(t, "the issues list leaves the manifest out", w.Claims(workflow.Row{Kind: "issue", Item: m3891}) && !w.Claims(workflow.Row{Kind: "issue", Item: plainIssue}))
	check(t, "the queued PR is claimed, a normal one not", w.Claims(workflow.Row{Kind: "pr", Item: q3922}) && !w.Claims(workflow.Row{Kind: "pr", Item: app.PRs[1]}))
	rows = w.Sections(app, nil, "alice")
	check(t, "the section lists the manifest row then the queued row", eq(rowShape(rows), []string{"header:queued for the maintenance window (1)", "header:window 2026-10-26 23:00 ET, tonight", "issue:3891", "queued:3922"}))
	app.RowList = rows
	check(t, "the summary counts tonight's queue", segsText(w.SummarySegs(app)) == " 1 queued tonight" || strings.HasSuffix(segsText(w.SummarySegs(app)), "1 queued tonight"))
	app.ViewName = "list"
	app.Sel = 2
	n, pr := w.Target(app)
	check(t, "w on the manifest row acts on its night, with no PR", n == mn && pr == nil)
	app.ViewName, app.DetailItm, app.Kind = "detail", m3891, "issue"
	n, pr = w.Target(app)
	check(t, "w in the manifest's detail view acts on its night", n == mn && pr == nil)
	app.DetailItm, app.Kind = &model.PR{Repo: infra, Number: 3922}, "pr"
	n, pr = w.Target(app)
	check(t, "w in a queued PR's detail view finds the listed PR", n == mn && pr == q3922)
	check(t, "filtering for a queued PR keeps its night's manifest row", eq(rowShape(w.Sections(app, []string{"3922"}, "alice")), rowShape(rows)))
	check(t, "the filter matches the stack", len(w.Sections(app, []string{"warehouse"}, "alice")) == 4 && w.SearchText(workflow.Row{Kind: RowKind, Item: q3922}) == "prod/07-warehouse")
	check(t, "a filter matching neither drops everything", w.Sections(app, []string{"nothing-matches"}, "alice") == nil)
}

// ---- application releases in the window ----

type releases []*model.PR

func (r releases) QueuedForWindow() []*model.PR { return r }

func TestReleases(t *testing.T) {
	cfg := testCfg()
	mw := cfg.MaintenanceWindow
	const mn = "2026-10-26"
	now26 := at("2026-10-27T02:00:00Z")
	rel42 := &model.PR{Repo: portal, Number: 42, Title: "Release portal 2026-10-27", HeadRefName: "release/2026-10-27",
		Author: model.Actor{Login: "bob"}, ReviewDecision: "APPROVED", Labels: labelled("window:queued", "window:scheduled")}
	tf42 := qpr(42, []string{"window:queued", "window:scheduled"}, map[string]any{"night": mn, "revertible": true, "queued_at": "2026-10-26T19:00:00Z"}, "prod/07-warehouse")
	relItem := Item{ID: "rel:web-portal#42", Kind: "app", Repo: "web-portal", PR: 42, Flag: "scheduled", StartUTC: "2026-10-27T03:04:00Z", Status: "scheduled", Revertible: boolp(true)}
	tfItem := Item{ID: "pr42", Kind: "tf", PR: 42, Flag: "scheduled", StartUTC: "2026-10-27T03:00:00Z", Status: "scheduled", Revertible: boolp(true)}
	mans := map[string]*Manifest{mn: {Night: mn, Phase: "frozen", Items: []Item{tfItem, relItem}}}
	check(t, "itemRef: repo+pr, rel: id with and without owner, plain",
		itemRef(relItem) == ref{"web-portal", 42} && itemRef(Item{ID: "rel:acme/x#7"}) == ref{"x", 7} &&
			itemRef(Item{ID: "rel:x#7"}) == ref{"x", 7} && itemRef(tfItem) == ref{"", 42})
	check(t, "prRef distinguishes a release from a manifest-repo PR of the same number", prRef(rel42, mw) == ref{"web-portal", 42} && prRef(tf42, mw) == ref{"", 42})
	_, it := WindowItem(rel42, mans, mw)
	_, it2 := WindowItem(tf42, mans, mw)
	check(t, "WindowItem picks the release's own item, not the same-numbered tf item", it != nil && it.ID == "rel:web-portal#42" && it2 != nil && it2.ID == "pr42")
	p, _ := Place(rel42, mans, now26, mw)
	check(t, "a scheduled release is placed on its night with its slot", p.Night == mn && p.Status == "scheduled 23:04 ET")
	m3891 := &model.Issue{Repo: infra, Number: 3891, Title: "Window " + mn + " 23:00 ET", Labels: labelled("window-manifest")}
	rows := QueueRows([]*model.PR{tf42}, []*model.PR{rel42}, mans, now26, []*model.Issue{m3891}, nil, mw)
	check(t, "a queued release keeps its own row kind under the night",
		eq(rowShape(rows), []string{"header:queued for the maintenance window (2)", "header:window 2026-10-26 23:00 ET, tonight", "issue:3891", "queued:42", "release:42"}))

	app, w := newApp(cfg)
	savedNow := clock.Now
	clock.Now = func() time.Time { return now26 }
	t.Cleanup(func() { clock.Now = savedNow })
	check(t, "/window-cancel on a release names the manifest's rel: id",
		w.CancelText(rel42, &relItem) == "/window-cancel rel:web-portal#42" && w.CancelText(rel42, nil) == "/window-cancel rel:web-portal#42" &&
			w.CancelText(tf42, &tfItem) == "/window-cancel #42")
	app.PRs = []*model.PR{tf42}
	app.IssueList = []*model.Issue{m3891}
	app.SetState(ReleaseState, releases{rel42}) // the release workflow lists only what it queues
	app.SetState(Name, &State{Manifests: mans, Nights: map[string]*Night{mn: {Issue: 3891, Phase: "frozen", Items: []Item{tfItem, relItem}}}})
	rows = w.Sections(app, nil, "alice")
	check(t, "the queued release sits under its night", eq(rowShape(rows), []string{"header:queued for the maintenance window (2)", "header:window 2026-10-26 23:00 ET, tonight", "issue:3891", "queued:42", "release:42"}))
	app.RowList, app.ViewName, app.Sel = rows, "list", 4
	n, pr := w.Target(app)
	check(t, "w on a queued release row acts on its night and PR", n == mn && pr == rel42)
	w.AfterApply(app)
	check(t, "a queued release makes the worker fetch the manifests", w.relQueued.Load())
}

// ---- refresh and the w key end to end ----

func TestRefreshAnnotateApply(t *testing.T) {
	cfg := testCfg()
	app, w := newApp(cfg)
	queued := &model.PR{Repo: infra, Number: 3544, Title: "queued thing", Labels: labelled("window:queued"), HeadRefOid: "h1"}
	queued.Commits.Nodes = append(queued.Commits.Nodes, struct {
		Commit model.Commit `json:"commit"`
	}{model.Commit{CommittedDate: at("2026-10-21T16:00:00Z")}})
	normal := &model.PR{Repo: infra, Number: 3601, Title: "normal"}
	app.Fake.Comments = map[model.Ident][]model.Comment{queued.Ident(): {digestA, markerComment(night, false, "2026-10-21T16:31:00Z", bot)}}
	app.Fake.Labelled = map[model.Repo]map[string][]*model.Issue{infra: {"window-manifest": {
		manifestFor(3600, night, "open", nil, nil, human("alice", "/attend", "2026-10-21T18:00:00Z"),
			botC("@alice is attending the night of 2026-10-21.", "2026-10-21T18:00:40Z"))}}}
	prs := []*model.PR{normal, queued}
	st, warns, _ := w.Refresh(app.Context(), app.Fake, cfg.Reviews.Repos, prs, nil)
	w.Annotate(prs, nil, st)
	check(t, "no warnings", len(warns) == 0)
	info := InfoOf(queued)
	check(t, "the queued PR is annotated with its marker and stack", info != nil && info.Marker != nil && info.Marker.Night == night &&
		info.Stack == "nonprod/15-window-canary" && !info.Stale && !info.Reapprove)
	check(t, "the normal PR is left alone", InfoOf(normal) == nil)
	w.Apply(app, st)
	got := w.state(app)
	check(t, "manifests and nights land in the state", got.Manifests[night] != nil && got.Nights[night] != nil &&
		got.Nights[night].Issue == 3600 && got.Nights[night].Mine.Attend == StateAttending)
	check(t, "the manifests were read from the manifest repo", app.Fake.Reads["labelled"] == 1 && app.Fake.Reads["comments"] == 1)

	st, _, _ = w.Refresh(app.Context(), app.Fake, cfg.Reviews.Repos, []*model.PR{normal}, nil)
	check(t, "nothing queued: nothing fetched", app.Fake.Reads["labelled"] == 1 && len(st.(*State).Manifests) == 0)
	app.Fake.Fail["labelled"] = errors.New("GET -> 403")
	st, warns, _ = w.Refresh(app.Context(), app.Fake, cfg.Reviews.Repos, prs, nil)
	check(t, "manifests unreadable: a warning, the PRs still annotated", len(warns) == 1 && len(st.(*State).infos) == 1)
	delete(app.Fake.Fail, "labelled")
	app.Fake.Fail["comments"] = errors.New("GET -> 403")
	st, _, _ = w.Refresh(app.Context(), app.Fake, cfg.Reviews.Repos, prs, nil)
	check(t, "comments unreadable: the PR shows its label only", len(st.(*State).infos) == 0)
	w.Apply(app, nil)
	check(t, "a nil state applies as empty", w.state(app).Nights != nil && len(w.state(app).Nights) == 0)

	// a re-planned queue entry: the label stays, the PR needs a fresh approve
	delete(app.Fake.Fail, "comments")
	digestB := botC("### plan `prod/09-portal`\n\nPlan-id: `"+pidB+"`", "2026-10-22T23:19:44Z")
	queued.Commits.Nodes[0].Commit.CommittedDate = at("2026-10-22T23:18:45Z")
	app.Fake.Comments[queued.Ident()] = []model.Comment{digestA, markerComment("2026-10-22", true, "2026-10-22T16:33:40Z", bot), digestB}
	st, _, _ = w.Refresh(app.Context(), app.Fake, cfg.Reviews.Repos, prs, nil)
	w.Annotate(prs, nil, st)
	check(t, "a stale entry is not queued and asks for a re-approve", !IsQueued(queued, cfg.MaintenanceWindow) && InfoOf(queued).Reapprove &&
		!w.Claims(workflow.Row{Kind: "pr", Item: queued}))
}

func TestDoWindow(t *testing.T) {
	cfg := testCfg()
	for _, tc := range []struct {
		pick   int
		cmd    string
		launch bool
		phase  string
	}{{1, "/release", true, "frozen"}, {0, "/attend", false, "open"}} {
		app, w := newApp(cfg)
		app.ViewName, app.DetailItm = "list", nil
		app.Picks, app.Confirms = []int{tc.pick}, []bool{true}
		frozen := WindowNights([]*model.Issue{manifestFor(4040, "2026-10-28", tc.phase, nil, []string{"alice"})}, "alice", cfg.MaintenanceWindow)["2026-10-28"]
		app.SetState(Name, &State{Nights: map[string]*Night{"2026-10-28": frozen}, Manifests: map[string]*Manifest{}})
		saved := clock.Now
		clock.Now = func() time.Time { return at("2026-10-28T14:00:00Z") } // 10:00 ET
		if tc.cmd == "/release" {
			clock.Now = func() time.Time { return at("2026-10-29T03:00:00Z") } // 23:00 ET
		}
		w.DoWindow(app)
		clock.Now = saved
		check(t, "w: "+tc.cmd+" posts on the manifest", len(app.Fake.CommentsPosted) == 1 && app.Fake.CommentsPosted[0].Number == 4040 &&
			app.Fake.CommentsPosted[0].Body == tc.cmd && app.Fake.CommentsPosted[0].Repo == infra)
		check(t, "w: "+tc.cmd+" launches or celebrates", (app.Launched == 1) == tc.launch && (len(app.Celebrated) == 1) == !tc.launch)
		check(t, "w: the menu names the night and the attendance", strings.HasPrefix(app.Menus[0][0], "window 2026-10-28 23:00 ET, manifest #4040: "+tc.phase+"; attending: "))
		check(t, "w: the question names the manifest", strings.Contains(app.Questions[0], "post "+tc.cmd+" on manifest #4040 (window 2026-10-28 23:00 ET)?"))
		check(t, "w: the reply is fetched soon", len(app.Refreshes) == 1 && len(app.Soon) == 1 && app.Soon[0] == 45*time.Second)
		check(t, "w: the command reads posted until the conductor replies", frozen.Mine.Get(strings.TrimPrefix(tc.cmd, "/")) == StatePosted)
	}

	app, w := newApp(cfg)
	app.ViewName, app.DetailItm = "list", nil
	app.Picks, app.Confirms = []int{1}, []bool{true}
	frozen := WindowNights([]*model.Issue{manifestFor(4040, "2026-10-28", "frozen", nil, []string{"alice"})}, "alice", cfg.MaintenanceWindow)["2026-10-28"]
	app.SetState(Name, &State{Nights: map[string]*Night{"2026-10-28": frozen}, Manifests: map[string]*Manifest{}})
	app.Fake.Fail["comment"] = errors.New("POST -> 500: boom")
	saved := clock.Now
	clock.Now = func() time.Time { return at("2026-10-29T03:00:00Z") }
	w.DoWindow(app)
	check(t, "w: a failed /release never launches", app.Launched == 0 && strings.HasPrefix(app.Last(), "fail: /release NOT posted"))

	app, w = newApp(cfg)
	app.ViewName, app.DetailItm = "list", nil
	app.SetState(Name, &State{Nights: map[string]*Night{"2026-10-28": frozen}, Manifests: map[string]*Manifest{}})
	app.Picks = []int{1}
	clock.Now = func() time.Time { return at("2026-10-28T14:00:00Z") }
	w.DoWindow(app)
	check(t, "w: a command the conductor would bounce is not posted", len(app.Fake.CommentsPosted) == 0 && strings.HasPrefix(app.Last(), "notice: /release not posted: too early"))
	check(t, "w: the menu marks it (not now)", strings.Contains(app.Menus[0][2], "/release (not now)"))
	app.Picks = []int{2}
	w.DoWindow(app)
	check(t, "w: the last entry opens the manifest", len(app.Opened) == 1 && app.Opened[0] == "infra#4040:issue")
	app.Picks = []int{-1}
	w.DoWindow(app)
	check(t, "w: Esc posts nothing", strings.HasPrefix(app.Last(), "notice: window: nothing posted"))
	app.Picks, app.Confirms = []int{0}, []bool{false}
	w.DoWindow(app)
	check(t, "w: declining posts nothing", strings.HasPrefix(app.Last(), "notice: /attend not posted"))
	clock.Now = saved

	app, w = newApp(cfg)
	app.ViewName, app.DetailItm = "list", nil
	w.DoWindow(app)
	check(t, "w with nothing queued says so", app.Last() == "notice: nothing is queued for a maintenance window")
	q := qpr(7, []string{"window:queued"}, nil, "")
	InfoOf(q).Night = "2026-12-01"
	app.PRs = []*model.PR{q}
	app.RowList = []workflow.Row{{Kind: RowKind, Item: q}}
	app.Sel = 0
	w.DoWindow(app)
	check(t, "w on a night without a manifest says so", app.Last() == "notice: no manifest issue for the night of 2026-12-01 yet (r refreshes)")
	keys := w.Keys()
	check(t, "w is bound in the list and the detail view", len(keys) == 2 && keys[0].Ch == "w" && keys[0].View == "list" && keys[1].View == "detail" && keys[0].Label == "window")
}

// ---- rows, legend, help ----

func TestQueuedRowRendering(t *testing.T) {
	cfg := testCfg()
	app, w := newApp(cfg)
	rk := w.RowKinds()[RowKind]
	q := qpr(3544, []string{"window:queued"}, map[string]any{"night": night, "revertible": false}, "nonprod/15-window-canary")
	InfoOf(q).Status = "scheduled 23:04 ET"
	rows := []workflow.Row{{Kind: RowKind, Item: q}}
	for _, width := range []int{40, 60, 80, 120, 200} {
		L := rk.Layout(app, rows, width, 5, 0)
		segs := rk.Segs(app, rows[0], L, width, 0)
		check(t, fmt.Sprintf("the queued row fits at %d columns", width), 2+text.SegsWidth(segs) <= width-1)
		if width >= 120 {
			check(t, "wide: stack and revertibility shown", L["stack"] == 24 && L["rev"] > 0 && strings.Contains(segsText(segs), "attended night needed") &&
				strings.Contains(segsText(segs), "nonprod/15-window-canary"))
		}
		if width == 40 {
			_, hasRev := L["rev"]
			check(t, "40 columns: the revertibility column is dropped first", !hasRev)
		}
	}
	segs := rk.Segs(app, rows[0], rk.Layout(app, rows, 120, 5, 0), 120, 0)
	check(t, "a scheduled row wears the scheduled chip", segs[2].Style == "cap:chip.sched" || segsText(segs[2:5]) != "")
	styles := map[string]bool{}
	for _, s := range segs {
		styles[s.Style] = true
	}
	check(t, "scheduled chip and attend style", styles["chip.sched"] && styles["rev.attend"] && styles["title"])
	InfoOf(q).Status = "rolled: no attendee"
	segs = rk.Segs(app, rows[0], rk.Layout(app, rows, 120, 5, 0), 120, 0)
	styles = map[string]bool{}
	for _, s := range segs {
		styles[s.Style] = true
	}
	check(t, "a rolled row wears the rolled chip", styles["chip.rolled"])
	check(t, "the row's title", rk.Title(rows[0]) == "PR 3544" && rk.Icon == "queued")

	legend := segsText(w.LegendRows()[0])
	check(t, "the legend shows the chips with the configured start", strings.Contains(legend, "scheduled 23:00 ET") && strings.Contains(legend, "attended night needed"))
	help := w.HelpRows()
	check(t, "the help row names the configured commands", len(help) == 1 && help[0][0] == "window" && strings.Contains(help[0][1], "/attend, /release or /window-cancel"))
	check(t, "the conductor is a bot", len(w.BotLogins()) == 1 && w.BotLogins()[0] == bot)
	check(t, "the workflow answers the night phrase and the release span for other workflows",
		w.QueueNightPhrase(at("2026-10-22T22:00:00Z")) == "tonight's" && w.InReleaseWindow(at("2026-10-22T03:05:00Z")))
	var _ workflow.Workflow = w
}
