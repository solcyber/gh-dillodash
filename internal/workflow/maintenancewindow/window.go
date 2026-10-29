// Package maintenancewindow is the maintenance-window queue workflow. A
// plain approve of a disruptive plan QUEUES it: the dispatcher labels the
// PR with the queued label and posts a comment carrying a
// "<!-- window-queue {...} -->" marker; the cutoff freeze adds the
// scheduled or rolled label, and the night's manifest issue (label
// manifest_label, title manifest_title) holds each item's slot in a
// "<!-- window-manifest:begin {...} window-manifest:end -->" block. The
// dashboard shows those PRs in their own section grouped by the night
// they will run, and the w key posts the attend / release / cancel
// commands on the night's manifest, checked against the conductor's
// rules first so a command that would bounce says why instead.
//
// Every time here is the window's zone from the config; nothing is
// hard-coded.
package maintenancewindow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/workflow"
	"github.com/solcyber/gh-dillodash/internal/workflow/terraformapprove"
)

// The conductor's comment and issue shapes (the documented protocol).
var (
	// MarkerRE is the queue marker a bot comment carries.
	MarkerRE = regexp.MustCompile(`(?s)<!-- window-queue (\{.*?\}) -->`)
	// ManifestRE is the manifest block in a manifest issue's body.
	ManifestRE = regexp.MustCompile(`(?s)<!-- window-manifest:begin\s*(\{.*\})\s*window-manifest:end -->`)
	// RelIDRE is a release item's id: rel:<owner/>repo#N.
	RelIDRE = regexp.MustCompile(`^rel:(?:[\w.-]+/)?([\w.-]+)#(\d+)$`)
	// WaitingRE is an item's "waiting for /release until <iso>" status.
	waitingRE = regexp.MustCompile(`^waiting for \S+ until (\S+)`)
)

// Ext is the Ext key the window annotation lives under (pr.Get(Ext)).
const Ext = "window"

// Marker is the queue marker the dispatcher posts when a PR is queued.
type Marker struct {
	PlanID     string
	Night      string // YYYY-MM-DD
	Revertible *bool  // nil when the marker does not say
	Disruptive bool
	Stacks     []string // a set's members in apply order
	QueuedAt   string   // ISO
	raw        map[string]json.RawMessage
}

// Item is one manifest item: a PR of the manifest repo (PR), or a
// release of another repo (Repo + PR, or the rel: ID).
type Item struct {
	ID           string
	Kind         string
	Repo         string
	PR           int
	Flag         string // scheduled, attended-only, ...
	StartUTC     string
	ReleaseByUTC string
	Status       string
	Revertible   *bool
	Reasons      []string
}

// Manifest is a night's manifest as its issue carries it.
type Manifest struct {
	Issue     int // the issue number it was read from
	Night     string
	Phase     string // "" = open
	Items     []Item
	Attendees []string // nil until the freeze writes the list
}

// Info is the window annotation on a queued PR (pr.Get(Ext)).
type Info struct {
	Marker *Marker
	Stack  string // what the queued row names
	// Stale: the plan the PR was queued with is dead (a later push
	// re-planned it); the label stays but the PR is not queued.
	Stale bool
	// Reapprove: stale, and the PR's live plan has no approve yet.
	Reapprove bool
	// Status and Night are set when the section is built: the row's
	// status text and the night the w key acts on.
	Status string
	Night  string
}

// NeedsReapprove implements terraformapprove.Reapprover.
func (i *Info) NeedsReapprove() bool { return i != nil && i.Reapprove }

// InfoOf is the PR's window annotation, or nil.
func InfoOf(pr *model.PR) *Info {
	if pr == nil {
		return nil
	}
	i, _ := pr.Get(Ext).(*Info)
	return i
}

func ensureInfo(pr *model.PR) *Info {
	if i := InfoOf(pr); i != nil {
		return i
	}
	i := &Info{}
	pr.Set(Ext, i)
	return i
}

// ---- tolerant JSON ----

type obj map[string]json.RawMessage

func parseObj(s string) (obj, bool) {
	var o obj
	if err := json.Unmarshal([]byte(s), &o); err != nil || o == nil {
		return nil, false
	}
	return o, true
}

func (o obj) str(k string) string {
	var s string
	if raw, ok := o[k]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	// a number where a string was expected (a night as a bare int, say)
	var n json.Number
	if raw, ok := o[k]; ok && json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func (o obj) boolp(k string) *bool {
	var b bool
	if raw, ok := o[k]; ok && json.Unmarshal(raw, &b) == nil {
		return &b
	}
	return nil
}

func (o obj) num(k string) int {
	var n int
	if raw, ok := o[k]; ok && json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

func (o obj) strs(k string) []string {
	var items []json.RawMessage
	raw, ok := o[k]
	if !ok || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := []string{}
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, s)
		} else {
			var v any
			if json.Unmarshal(it, &v) == nil && v != nil {
				out = append(out, fmt.Sprint(v))
			}
		}
	}
	return out
}

func (o obj) objs(k string) []obj {
	var items []json.RawMessage
	raw, ok := o[k]
	if !ok || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []obj
	for _, it := range items {
		if sub, ok := parseObj(string(it)); ok {
			out = append(out, sub)
		}
	}
	return out
}

func (o obj) has(k string) bool { _, ok := o[k]; return ok }

func parseItem(o obj) Item {
	return Item{ID: o.str("id"), Kind: o.str("kind"), Repo: o.str("repo"), PR: o.num("pr"),
		Flag: o.str("flag"), StartUTC: o.str("start_utc"), ReleaseByUTC: o.str("release_by_utc"),
		Status: o.str("status"), Revertible: o.boolp("revertible"), Reasons: o.strs("reasons")}
}

// ---- markers ----

// ParseMarker is the latest queue marker the conductor bot posted among
// the comments, or nil.
func ParseMarker(comments []model.Comment, cfg *config.MaintenanceWindowConfig) *Marker {
	var found *Marker
	for _, c := range comments {
		if !isBot(c.Author.Login, cfg.ConductorBot) {
			continue
		}
		m := MarkerRE.FindStringSubmatch(c.Body)
		if m == nil {
			continue
		}
		o, ok := parseObj(m[1])
		if !ok {
			continue
		}
		mk := &Marker{PlanID: o.str("plan_id"), Night: o.str("night"), Revertible: o.boolp("revertible"),
			QueuedAt: o.str("queued_at"), Stacks: o.strs("stacks"), raw: o}
		if b := o.boolp("disruptive"); b != nil {
			mk.Disruptive = *b
		}
		found = mk
	}
	return found
}

func isBot(login, bot string) bool {
	return bot != "" && strings.HasPrefix(strings.ToLower(login), strings.ToLower(bot))
}

// LatestPlanStack is what a queued row names: the marker's own stacks
// (a set's members in apply order, joined as the conductor joins them),
// else the stack of the newest digest that carries a plan-id; a "no
// changes" notice for some other stack is not what's queued.
func LatestPlanStack(comments []model.Comment, marker *Marker) string {
	if marker != nil && len(marker.Stacks) > 0 {
		return strings.Join(marker.Stacks, " + ")
	}
	stack := ""
	for _, c := range comments {
		m := terraformapprove.PlanStackRE.FindStringSubmatch(c.Body)
		if m != nil && terraformapprove.PlanIDRE.MatchString(c.Body) && !terraformapprove.PlanNoticeRE.MatchString(c.Body) {
			stack = m[1]
		}
	}
	return stack
}

// ---- manifests ----

func parseManifest(i *model.Issue, cfg *config.MaintenanceWindowConfig) (*Manifest, bool) {
	m := ManifestRE.FindStringSubmatch(i.Body)
	if m == nil {
		return nil, false
	}
	o, ok := parseObj(m[1])
	if !ok {
		return nil, false
	}
	man := &Manifest{Issue: i.Number, Night: o.str("night"), Phase: o.str("phase")}
	for _, it := range o.objs("items") {
		man.Items = append(man.Items, parseItem(it))
	}
	if o.has("attendees") {
		if att := o.strs("attendees"); att != nil {
			man.Attendees = att
		}
	}
	if man.Night == "" {
		if n, ok := cfg.MatchNight(i.Title); ok {
			man.Night = n
		}
	}
	return man, man.Night != ""
}

// ParseManifests is {night: manifest} from the open manifest issues. If
// two are open for the same night (two approvals raced to create it),
// the lowest-numbered issue wins and the other is a duplicate: the
// conductor's rule.
func ParseManifests(issues []*model.Issue, cfg *config.MaintenanceWindowConfig) map[string]*Manifest {
	out := map[string]*Manifest{}
	sorted := append([]*model.Issue(nil), issues...)
	// lowest number written last, so it wins
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Number > sorted[b].Number })
	for _, i := range sorted {
		if man, ok := parseManifest(i, cfg); ok {
			out[man.Night] = man
		}
	}
	return out
}

// ManifestIssueNight is the night a manifest issue is for, else "". The
// conductor's own test: the manifest label and the exact manifest title.
// Such an issue lives in the window section, under its night, not in
// the issues list.
func ManifestIssueNight(i *model.Issue, cfg *config.MaintenanceWindowConfig) string {
	if i == nil || !i.Labels.Has(cfg.ManifestLabel) {
		return ""
	}
	n, _ := cfg.MatchNight(i.Title)
	return n
}

// ---- queued PRs ----

// HasQueuedLabel reports the queued label, whatever the annotation says.
func HasQueuedLabel(pr *model.PR, cfg *config.MaintenanceWindowConfig) bool {
	return pr != nil && pr.Labels.Has(cfg.QueuedLabel)
}

// IsQueued: labelled queued, and the queued plan is still the PR's plan.
// A push after the queueing re-plans it; the label stays, but what's
// queued is dead and the PR needs a fresh approve, so it belongs in the
// approval list.
func IsQueued(pr *model.PR, cfg *config.MaintenanceWindowConfig) bool {
	if !HasQueuedLabel(pr, cfg) {
		return false
	}
	i := InfoOf(pr)
	return i == nil || !i.Stale
}

// Fates of a queued plan.
const (
	FateQueued     = "queued"     // still one of the PR's plans, or it can't be told
	FateReplanned  = "re-planned" // dead, and the live plan has no approve yet
	FateSuperseded = "superseded" // dead, but a newer plan was approved or applied since
)

// QueuedPlanState is what became of the plan a queued PR was queued
// with, read from the comments fetched for it (which may be more than
// the list query holds).
func QueuedPlanState(marker *Marker, comments []model.Comment, pr *model.PR, ta *config.TerraformApproveConfig) string {
	if marker == nil || marker.PlanID == "" {
		return FateQueued
	}
	view := &model.PR{Repo: pr.Repo, Number: pr.Number, HeadRefOid: pr.HeadRefOid, Commits: pr.Commits}
	view.Comments.Nodes = comments
	cands := terraformapprove.ExtractPlanCandidates(view, ta)
	if len(cands) == 0 {
		return FateQueued
	}
	ids := map[string]bool{}
	var live []terraformapprove.Candidate
	for _, c := range cands {
		if c.State == terraformapprove.StateLive || c.State == terraformapprove.StateInPlanSet {
			live = append(live, c)
			ids[c.PlanID] = true
		}
	}
	if ps := terraformapprove.ExtractPlanSet(view); ps != nil {
		ids[ps.SetID] = true
	}
	if ids[marker.PlanID] {
		return FateQueued
	}
	for _, c := range live {
		if c.Approved || c.Applied {
			return FateSuperseded
		}
	}
	return FateReplanned
}

// ---- placement ----

// ref is (repo short name, PR number): the repo is "" for a PR of the
// manifest repo, so a release of another repo with the same number is
// told apart.
type ref struct {
	repo string
	n    int
}

func itemRef(it Item) ref {
	if it.Repo != "" {
		parts := strings.Split(it.Repo, "/")
		return ref{parts[len(parts)-1], it.PR}
	}
	if m := RelIDRE.FindStringSubmatch(it.ID); m != nil {
		n, _ := strconv.Atoi(m[2])
		return ref{m[1], n}
	}
	return ref{"", it.PR}
}

func prRef(pr *model.PR, cfg *config.MaintenanceWindowConfig) ref {
	if pr.Repo.IsZero() || pr.Repo == cfg.ManifestRepo {
		return ref{"", pr.Number}
	}
	return ref{pr.Repo.Name, pr.Number}
}

// WindowItem is (night, item) for the newest manifest listing the PR;
// nil when none does.
func WindowItem(pr *model.PR, manifests map[string]*Manifest, cfg *config.MaintenanceWindowConfig) (string, *Item) {
	want := prRef(pr, cfg)
	nights := make([]string, 0, len(manifests))
	for n := range manifests {
		nights = append(nights, n)
	}
	sort.Strings(nights)
	var bestNight string
	var best *Item
	for _, n := range nights {
		for k := range manifests[n].Items {
			if itemRef(manifests[n].Items[k]) == want {
				bestNight, best = n, &manifests[n].Items[k]
			}
		}
	}
	return bestNight, best
}

// Placement is where a queued PR sits in the section.
type Placement struct {
	Night  string // YYYY-MM-DD: where the item will run as things stand
	Status string
	key    [4]string
}

// nightDate is a night's local midnight.
func nightDate(night string, local *clock.Local) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", night, local.Loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("night %q: want YYYY-MM-DD", night)
	}
	return t, nil
}

func fmtNight(t time.Time) string { return t.Format("2006-01-02") }

// NextQueueNight is the night a queued item joins if approved now:
// tonight until the cutoff, tomorrow night after it.
func NextQueueNight(now time.Time, cfg *config.MaintenanceWindowConfig) string {
	local := cfg.Local()
	lt := local.In(now)
	day := local.Today(now)
	if (clock.HM{H: lt.Hour(), M: lt.Minute()}).Minutes() >= cfg.Cutoff.Minutes() {
		day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, local.Loc)
	}
	return fmtNight(day)
}

// QueueNightPhrase is whose window a plain approve queues for right
// now: "tonight's" until the cutoff, "tomorrow night's" after it.
func QueueNightPhrase(now time.Time, cfg *config.MaintenanceWindowConfig) string {
	if NextQueueNight(now, cfg) == fmtNight(cfg.Local().Today(now)) {
		return "tonight's"
	}
	return "tomorrow night's"
}

// WallToUTC is the instant of a wall clock on a night, in UTC.
func WallToUTC(night string, hm clock.HM, cfg *config.MaintenanceWindowConfig) (time.Time, error) {
	local := cfg.Local()
	day, err := nightDate(night, local)
	if err != nil {
		return time.Time{}, err
	}
	return local.WallToUTC(day, hm), nil
}

// Place is (night, status text, sort key) for a queued PR. The night is
// where the item will run as things stand: the manifest's night while
// it is scheduled there; otherwise its queue night if that night's
// cutoff is still ahead; otherwise (it rolled, or the marker is missing)
// the next night whose cutoff is ahead. An error means malformed marker
// or manifest data.
func Place(pr *model.PR, manifests map[string]*Manifest, now time.Time, cfg *config.MaintenanceWindowConfig) (Placement, error) {
	local := cfg.Local()
	info := InfoOf(pr)
	var marker Marker
	if info != nil && info.Marker != nil {
		marker = *info.Marker
	}
	mNight, item := WindowItem(pr, manifests, cfg)
	night, status, start := "", "queued", ""
	if pr.Labels.Has(cfg.ScheduledLabel) && mNight != "" && item != nil && item.Flag == "scheduled" {
		if _, err := nightDate(mNight, local); err != nil {
			return Placement{}, err
		}
		night = mNight
		start = item.StartUTC
		status = "scheduled"
		if start != "" {
			t, err := clock.ParseUTC(start)
			if err != nil {
				return Placement{}, err
			}
			status = "scheduled " + local.TAt(t)
		}
		if item.Status != "" && item.Status != "scheduled" {
			status = text.Ellip(item.Status, 40)
			if w := waitingRE.FindStringSubmatch(item.Status); w != nil {
				status = "awaiting " + cfg.ReleaseCommand
				if t, err := clock.ParseUTC(w[1]); err == nil {
					status += ", " + local.TAt(t)
				}
			}
		}
	} else {
		if marker.Night != "" {
			cutoff, err := WallToUTC(marker.Night, cfg.Cutoff, cfg)
			if err != nil {
				return Placement{}, err
			}
			if now.Before(cutoff) {
				night = marker.Night
			}
		}
		if night == "" {
			night = NextQueueNight(now, cfg)
		}
		if pr.Labels.Has(cfg.RolledLabel) {
			why := ""
			if item != nil {
				why = strings.Join(item.Reasons, "; ")
				if why == "" {
					why = item.Status
				}
			}
			status = "rolled"
			if why != "" {
				status = "rolled: " + why
			}
		}
	}
	st := start
	if st == "" {
		st = "9"
	}
	return Placement{Night: night, Status: status, key: [4]string{night, st, marker.QueuedAt, fmt.Sprintf("%010d", pr.Number)}}, nil
}

// NightLabel is a night sub-header: "window 2026-10-21 23:00 ET, tonight".
func NightLabel(night string, now time.Time, cfg *config.MaintenanceWindowConfig) string {
	local := cfg.Local()
	rel := night
	if day, err := nightDate(night, local); err == nil {
		today := local.Today(now)
		days := int((day.Sub(today).Hours() + 12) / 24)
		switch days {
		case 0:
			rel = "tonight"
		case 1:
			rel = "tomorrow night"
		default:
			rel = day.Format("Mon")
		}
	}
	return fmt.Sprintf("window %s %s, %s", night, local.T(cfg.Start), rel)
}

// QueueRows builds the "queued for the maintenance window" section: a
// header, then per-night sub-headers (next window first, Meta[NightRow]
// the night), each with its open manifest issue as an "issue" row
// (Meta["label"] "manifest <phase>", Meta[NightRow]) and then its
// "queued" rows (and the "release" rows of queued releases). Each PR's
// Info gains Status and Night. A manifest whose night has no queued PR
// left still gets its night when showAlone(issue) says so (the list
// filter); nil means yes.
func QueueRows(prs, releases []*model.PR, manifests map[string]*Manifest, now time.Time, manifestIssues []*model.Issue,
	showAlone func(*model.Issue) bool, cfg *config.MaintenanceWindowConfig) []workflow.Row {
	type placed struct {
		p    Placement
		row  workflow.Row
		rank int
	}
	var all []placed
	place := func(pr *model.PR, kind string, rank int) {
		p, err := Place(pr, manifests, now, cfg)
		if err != nil {
			// malformed marker / manifest data: one bad item degrades to
			// a placeholder instead of crashing every refresh
			n := NextQueueNight(now, cfg)
			p = Placement{Night: n, Status: "queued (?)", key: [4]string{n, "9", "", fmt.Sprintf("%010d", pr.Number)}}
		}
		info := ensureInfo(pr)
		info.Status, info.Night = p.Status, p.Night
		all = append(all, placed{p, workflow.Row{Kind: kind, Item: pr}, rank})
	}
	for _, pr := range prs {
		place(pr, "queued", 0)
	}
	for _, pr := range releases {
		place(pr, "release", 1)
	}
	byNight := map[string]*model.Issue{} // two open for one night: the lowest number wins
	for _, i := range manifestIssues {
		n := ManifestIssueNight(i, cfg)
		if n == "" {
			continue
		}
		if cur, ok := byNight[n]; !ok || i.Number < cur.Number {
			byNight[n] = i
		}
	}
	withPRs := map[string]bool{}
	for _, p := range all {
		withPRs[p.p.Night] = true
	}
	var alone []string
	for n, i := range byNight {
		if !withPRs[n] && (showAlone == nil || showAlone(i)) {
			alone = append(alone, n)
		}
	}
	if len(all) == 0 && len(alone) == 0 {
		return nil
	}
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].p.key != all[b].p.key {
			return keyLess(all[a].p.key, all[b].p.key)
		}
		return all[a].rank < all[b].rank
	})
	rows := []workflow.Row{{Kind: "header", Item: fmt.Sprintf("queued for the maintenance window (%d)", len(all))}}
	groups := map[string][]workflow.Row{}
	for _, p := range all {
		groups[p.p.Night] = append(groups[p.p.Night], p.row)
	}
	for _, n := range alone {
		groups[n] = nil
	}
	nights := make([]string, 0, len(groups))
	for n := range groups {
		nights = append(nights, n)
	}
	sort.Strings(nights)
	for _, n := range nights {
		rows = append(rows, workflow.Row{Kind: "header", Item: "  " + NightLabel(n, now, cfg), Meta: map[string]any{workflow.NightRow: n}})
		if issue := byNight[n]; issue != nil {
			label := "manifest"
			if man := manifests[n]; man != nil && man.Phase != "" {
				label += " " + man.Phase
			}
			rows = append(rows, workflow.Row{Kind: "issue", Item: issue,
				Meta: map[string]any{"label": text.Ellip(label, 16), workflow.NightRow: n}})
		}
		rows = append(rows, groups[n]...)
	}
	return rows
}

func keyLess(a, b [4]string) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
