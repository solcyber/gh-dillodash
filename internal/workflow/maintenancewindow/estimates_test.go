package maintenancewindow

import (
	"strconv"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// appItem is an app release item as the event-driven freeze writes it.
func appItem(repo string, pr, order int, start, status string) map[string]any {
	return map[string]any{"id": "rel:" + repo + "#" + strconv.Itoa(pr), "kind": "app", "repo": repo, "pr": pr, "flag": "scheduled",
		"lane": "app", "lane_order": order, "start_utc": start, "start_is_estimate": true, "status": status,
		"T_s": 1188, "A_s": 612, "R_s": 276, "settle_s": 300, "samples": "live(n=10)", "revertible": true, "reasons": []string{}}
}

// TestManifestEstimateFields: the freeze's estimate fields are read
// when present, in any numeric spelling, and an older manifest without
// them reads as before.
func TestManifestEstimateFields(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	items := []map[string]any{
		appItem("web-portal", 212, 1, "2026-10-22T03:00:00Z", "dispatched"),
		appItem("etl-jobs", 158, 2, "2026-10-22T03:20:00Z", "waiting for rel:web-portal#212"),
		// an older item: none of the new fields
		{"id": "pr3544", "pr": 3544, "flag": "scheduled", "start_utc": "2026-10-22T03:04:00Z", "status": "scheduled", "T_s": 900},
		// odd spellings: a float, a string, a bad value
		{"id": "rel:db-migrations#9", "kind": "app", "repo": "db-migrations", "pr": 9, "flag": "scheduled", "lane": "app",
			"lane_order": "3", "T_s": 1187.2, "settle_s": "x", "start_is_estimate": "true", "dispatched_utc": "2026-10-22T03:41:00Z"},
	}
	items[0]["dispatched_utc"] = "2026-10-22T03:02:10Z"
	ms := ParseManifests([]*model.Issue{manifestFor(3600, night, "running", items, []string{"alice"})}, cfg)
	m := ms[night]
	if m == nil || len(m.Items) != 4 {
		t.Fatalf("manifest %+v", m)
	}
	web, etl, old, db := m.Items[0], m.Items[1], m.Items[2], m.Items[3]
	if web.TSecs != 1188 || web.ASecs != 612 || web.RSecs != 276 || web.SettleSecs != 300 || web.Lane != "app" || web.LaneOrder != 1 ||
		web.Samples != "live(n=10)" || !web.StartIsEstimate || web.DispatchedUTC != "2026-10-22T03:02:10Z" {
		t.Errorf("every field: %+v", web)
	}
	if old.TSecs != 900 || old.SettleSecs != 0 || old.LaneOrder != 0 || old.StartIsEstimate || old.DispatchedUTC != "" || old.Samples != "" {
		t.Errorf("an older item: %+v", old)
	}
	if db.TSecs != 1188 || db.LaneOrder != 3 || db.SettleSecs != 0 || !db.StartIsEstimate {
		t.Errorf("tolerant: a float rounds up, a numeric string reads, junk is unsaid: %+v", db)
	}
	if got := AheadInLane(m, &etl); got != "rel:web-portal#212" {
		t.Errorf("the conductor's waiting status names the one ahead: %q", got)
	}
	if got := AheadInLane(m, &web); got != "" {
		t.Errorf("the first place has none ahead: %q", got)
	}
	etl.Status = "" // no word from the conductor: by lane_order
	if got := AheadInLane(m, &etl); got != "rel:web-portal#212" {
		t.Errorf("by lane_order: %q", got)
	}
	done := true
	m.Items[0].Final = &done
	if got := AheadInLane(m, &etl); got != "" {
		t.Errorf("a final item ahead holds nothing up: %q", got)
	}

	st := &State{Manifests: ms}
	mi, ok := st.ManifestItem("db-migrations", 9)
	if !ok || mi.TSecs != 1188 || mi.Lane != "app" || mi.LaneOrder != 3 || !mi.StartIsEstimate || mi.DispatchedUTC == "" {
		t.Errorf("the provider passes them on: %+v", mi)
	}
}

// TestCancelAnEstimatedItem: /window-cancel refuses what the conductor
// counts as started: a dispatched item, or a fixed slot that passed and
// is not waiting. An estimated start that passed while the item waits
// in the lane is still cancellable.
func TestCancelAnEstimatedItem(t *testing.T) {
	mw := testCfg().MaintenanceWindow
	info := &Night{Phase: "running"}
	now := at("2026-10-22T03:30:00Z")
	est := Item{PR: 158, Flag: "scheduled", StartUTC: "2026-10-22T03:20:00Z", StartIsEstimate: true, Status: "waiting for rel:web-portal#212"}
	if ok, why := CommandCheck(CmdCancel, info, night, "alice", now, &est, mw); !ok {
		t.Errorf("past its estimate, still waiting in the lane: cancellable: %s", why)
	}
	est.Status = "scheduled"
	if ok, _ := CommandCheck(CmdCancel, info, night, "alice", now, &est, mw); !ok {
		t.Error("an estimate that passed is not a start")
	}
	est.DispatchedUTC, est.Status = "2026-10-22T03:12:00Z", "dispatched"
	if ok, why := CommandCheck(CmdCancel, info, night, "alice", at("2026-10-22T03:13:00Z"), &est, mw); ok || why != "#158 has already started" {
		t.Errorf("dispatched before its estimate: started: %v %q", ok, why)
	}
	fixed := Item{PR: 3544, Flag: "scheduled", StartUTC: "2026-10-22T03:20:00Z", Status: "scheduled"}
	if ok, _ := CommandCheck(CmdCancel, info, night, "alice", now, &fixed, mw); ok {
		t.Error("a fixed slot that passed: started")
	}
	fixed.Status = "waiting for terraform item(s) pr3600 to be final"
	if ok, _ := CommandCheck(CmdCancel, info, night, "alice", now, &fixed, mw); !ok {
		t.Error("any 'waiting for' is not started, as the conductor counts it")
	}
}

// TestPlaceSaysEst: a scheduled item with an estimated start reads
// "scheduled est. HH:MM" in the queue.
func TestPlaceSaysEst(t *testing.T) {
	cfg := testCfg().MaintenanceWindow
	pr := qpr(3544, []string{"window:queued", "window:scheduled"}, map[string]any{"night": night, "revertible": true, "queued_at": "2026-10-21T16:31:00Z"}, "staging/x")
	manifests := map[string]*Manifest{night: {Night: night, Items: []Item{{PR: 3544, Flag: "scheduled", StartUTC: "2026-10-22T03:04:00Z", Status: "scheduled", StartIsEstimate: true}}}}
	p, err := Place(pr, manifests, at("2026-10-21T22:00:00Z"), cfg)
	if err != nil || p.Status != "scheduled est. 23:04 ET" {
		t.Errorf("place: %q %v", p.Status, err)
	}
	manifests[night].Items[0].StartIsEstimate = false
	if p, _ := Place(pr, manifests, at("2026-10-21T22:00:00Z"), cfg); p.Status != "scheduled 23:04 ET" {
		t.Errorf("a fixed slot: %q", p.Status)
	}
}

// TestStripMarksTheDispatch: once dispatched, an item's mark sits where
// it started, not at its estimate.
func TestStripMarksTheDispatch(t *testing.T) {
	cfg := testCfg()
	_, w := newApp(cfg)
	mw := cfg.MaintenanceWindow
	const mn = "2026-10-26"
	start, _ := WallToUTC(mn, mw.Start, mw)
	end, _ := WallToUTC(mn, mw.End, mw)
	iso := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	item := Item{PR: 1, Flag: "scheduled", StartUTC: iso(end.Add(-2 * time.Minute)), Status: "running", StartIsEstimate: true}
	markAt := func(it Item) int {
		col := 0
		for _, sg := range w.stripSegs(&Night{Phase: "running", Items: []Item{it}}, mn, start.Add(-time.Hour), 60) {
			if sg.Style == "rev.attend" {
				return col
			}
			col += text.Width(sg.Text)
		}
		return -1
	}
	est := markAt(item)
	item.DispatchedUTC = iso(start.Add(time.Minute))
	sent := markAt(item)
	if est < 0 || sent < 0 || sent >= est {
		t.Errorf("the mark moves from the estimate (col %d) to the dispatch (col %d)", est, sent)
	}
}
