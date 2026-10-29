package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
)

// Set is the enabled workflows, in order; it fans every hook out and owns
// the merge rules (first non-nil wins for classify/approve/merge, concat
// for sections/keys/patterns, duplicates are startup errors).
type Set struct {
	Items []Workflow
	ctx   *Context
	kinds map[string]RowKind
	keys  []Key
}

// Load builds every registered workflow the config enables.
func Load(cfg *config.Config, repos []model.Repo) (*Set, error) {
	ctx := &Context{Config: cfg, Repos: repos, byName: map[string]Workflow{}}
	names := make([]string, 0, len(Registry))
	for n := range Registry {
		names = append(names, n)
	}
	sort.Strings(names)
	var items []Workflow
	// two passes so cross-workflow lookups see every enabled workflow
	for _, n := range names {
		w, err := Registry[n](ctx)
		if err != nil {
			return nil, fmt.Errorf("workflow %s: %w", n, err)
		}
		if w != nil {
			items = append(items, w)
			ctx.byName[w.Name()] = w
		}
	}
	return NewSet(ctx, items...)
}

// NewSet assembles a set from explicit workflows (tests).
func NewSet(ctx *Context, items ...Workflow) (*Set, error) {
	if ctx == nil {
		ctx = &Context{byName: map[string]Workflow{}}
	}
	if ctx.byName == nil {
		ctx.byName = map[string]Workflow{}
	}
	for _, w := range items {
		ctx.byName[w.Name()] = w
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order() < items[j].Order() })
	s := &Set{Items: items, ctx: ctx, kinds: map[string]RowKind{}}
	seenKey := map[string]string{}
	for _, w := range items {
		for k, rk := range w.RowKinds() {
			if _, dup := s.kinds[k]; dup || k == "pr" || k == "issue" || k == "header" {
				return nil, fmt.Errorf("workflow %s: row kind %q already defined", w.Name(), k)
			}
			s.kinds[k] = rk
		}
		for _, k := range w.Keys() {
			id := k.View + ":" + k.Ch
			if owner, dup := seenKey[id]; dup {
				return nil, fmt.Errorf("workflow %s: key %q in %s view already bound by %s", w.Name(), k.Ch, k.View, owner)
			}
			if coreKeys[k.View+":"+k.Ch] {
				return nil, fmt.Errorf("workflow %s: key %q in %s view is a core key", w.Name(), k.Ch, k.View)
			}
			seenKey[id] = w.Name()
			s.keys = append(s.keys, k)
		}
	}
	sort.SliceStable(s.keys, func(i, j int) bool { return s.keys[i].Priority < s.keys[j].Priority })
	return s, nil
}

// coreKeys are bound by the dashboard itself and may not be taken: the
// ui package's key table, view:key (a test keeps the two equal).
var coreKeys = map[string]bool{
	"list:/": true, "list::": true, "list:A": true, "list:D": true, "list:G": true, "list:d": true, "list:g": true, "list:j": true,
	"list:k": true, "list:l": true, "list:o": true, "list:q": true, "list:r": true, "list:y": true,
	"detail:/": true, "detail:C": true, "detail:D": true, "detail:G": true, "detail:M": true, "detail:N": true, "detail:a": true, "detail:c": true,
	"detail:f": true, "detail:g": true, "detail:h": true, "detail:j": true, "detail:k": true, "detail:m": true, "detail:n": true, "detail:o": true,
	"detail:q": true, "detail:r": true, "detail:t": true, "detail:v": true, "detail:y": true,
	"issue:/": true, "issue:C": true, "issue:G": true, "issue:N": true, "issue:c": true, "issue:g": true, "issue:h": true, "issue:j": true,
	"issue:k": true, "issue:m": true, "issue:n": true, "issue:o": true, "issue:q": true, "issue:r": true, "issue:t": true, "issue:y": true,
	"any::": true, "any:?": true, "any:e": true,
}

// CoreKeyIDs lists the view:key pairs the dashboard reserves, sorted;
// the ui package's tests keep it equal to its key table.
func CoreKeyIDs() []string {
	out := make([]string, 0, len(coreKeys))
	for k := range coreKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Get returns an enabled workflow by name, or nil.
func (s *Set) Get(name string) Workflow {
	if s == nil {
		return nil
	}
	return s.ctx.byName[name]
}

// Names lists the enabled workflows.
func (s *Set) Names() []string {
	var out []string
	for _, w := range s.Items {
		out = append(out, w.Name())
	}
	return out
}

// Empty reports no workflows.
func (s *Set) Empty() bool { return s == nil || len(s.Items) == 0 }

// RefreshAll runs every workflow's Refresh; states keyed by name. The
// first rate limit a workflow meets is returned and the workflows after
// it are skipped (they would only hit the same wall); the states
// gathered so far still come back.
func (s *Set) RefreshAll(ctx context.Context, gh ghapi.Client, repos []model.Repo, prs []*model.PR, issues []*model.Issue) (map[string]State, []string, error) {
	states := map[string]State{}
	var warns []string
	if s == nil {
		return states, nil, nil
	}
	for _, w := range s.Items {
		st, ws, err := w.Refresh(ctx, gh, repos, prs, issues)
		states[w.Name()] = st
		warns = append(warns, ws...)
		w.Annotate(prs, issues, st)
		if err != nil {
			return states, warns, err
		}
	}
	return states, warns, nil
}

// CIPRs collects the PRs the workflow states contribute to the core's CI
// pass (CIProvider), in workflow order.
func (s *Set) CIPRs(states map[string]State) []*model.PR {
	if s == nil {
		return nil
	}
	var out []*model.PR
	for _, w := range s.Items {
		if p, ok := states[w.Name()].(CIProvider); ok && p != nil {
			out = append(out, p.CIPRs()...)
		}
	}
	return out
}

// ProvidesDeploys reports whether any workflow lists merged PRs whose
// deployments D approves (the list key bar shows D then).
func (s *Set) ProvidesDeploys() bool {
	if s == nil {
		return false
	}
	for _, w := range s.Items {
		if p, ok := w.(DeployProvider); ok && p.ProvidesDeploys() {
			return true
		}
	}
	return false
}

// PollEvery is the shortest poll cadence among the workflows with
// something live, or 0 when nothing is in flight (no poll goroutine).
func (s *Set) PollEvery(app App) time.Duration {
	if s == nil {
		return 0
	}
	var d time.Duration
	for _, w := range s.Items {
		p, ok := w.(Poller)
		if !ok || !p.Live(app) {
			continue
		}
		if e := p.PollEvery(); e > 0 && (d == 0 || e < d) {
			d = e
		}
	}
	return d
}

// PollAll runs every poller's Poll (worker goroutine); patches keyed by
// name. The first rate limit stops the loop, the patches gathered so far
// still come back.
func (s *Set) PollAll(ctx context.Context, gh ghapi.Client) (map[string]any, error) {
	out := map[string]any{}
	if s == nil {
		return out, nil
	}
	for _, w := range s.Items {
		p, ok := w.(Poller)
		if !ok {
			continue
		}
		patch, err := p.Poll(ctx, gh)
		if patch != nil {
			out[w.Name()] = patch
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// ApplyPolls hands each poller its patch on the UI goroutine.
func (s *Set) ApplyPolls(app App, patches map[string]any) {
	if s == nil {
		return
	}
	for _, w := range s.Items {
		if p, ok := w.(Poller); ok {
			if patch, ok := patches[w.Name()]; ok {
				p.ApplyPoll(app, patch)
			}
		}
	}
}

// LiveRow reports whether the row's kind says it changes between
// refreshes (RowKind.Live).
func (s *Set) LiveRow(app App, row Row) bool {
	if s == nil {
		return false
	}
	rk, ok := s.kinds[row.Kind]
	return ok && rk.Live != nil && rk.Live(app, row)
}

// ApplyAll hands each workflow its state on the UI goroutine.
func (s *Set) ApplyAll(app App, states map[string]State) {
	if s == nil {
		return
	}
	for _, w := range s.Items {
		w.Apply(app, states[w.Name()])
	}
	for _, w := range s.Items {
		w.AfterApply(app)
	}
}

// Claims reports whether any workflow takes the row out of the core
// sections.
func (s *Set) Claims(row Row) bool {
	if s == nil {
		return false
	}
	for _, w := range s.Items {
		if w.Claims(row) {
			return true
		}
	}
	return false
}

// Classify returns the first workflow classification, or nil.
func (s *Set) Classify(pr, listed *model.PR, me string, localApproved bool) *Classification {
	if s == nil {
		return nil
	}
	for _, w := range s.Items {
		if c := w.Classify(pr, listed, me, localApproved); c != nil {
			return c
		}
	}
	return nil
}

// SearchText concatenates every workflow's search text for the row.
func (s *Set) SearchText(row Row) string {
	if s == nil {
		return ""
	}
	var parts []string
	for _, w := range s.Items {
		if t := w.SearchText(row); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// BotLogins merges every workflow's bot logins (lower-cased).
func (s *Set) BotLogins() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, w := range s.Items {
		for _, l := range w.BotLogins() {
			out = append(out, strings.ToLower(l))
		}
	}
	return out
}

// ChipStyles merges chip label -> style maps.
func (s *Set) ChipStyles() map[string]string {
	out := map[string]string{}
	if s == nil {
		return out
	}
	for _, w := range s.Items {
		for k, v := range w.ChipStyles() {
			out[k] = v
		}
	}
	return out
}

// NeedsYou merges the labels counted as "need you".
func (s *Set) NeedsYou() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for _, w := range s.Items {
		for _, l := range w.NeedsYou() {
			out[l] = true
		}
	}
	return out
}

// Sections concatenates every workflow's list sections, in order.
func (s *Set) Sections(app App, terms []string, me string) []Row {
	if s == nil {
		return nil
	}
	var out []Row
	for _, w := range s.Items {
		out = append(out, w.Sections(app, terms, me)...)
	}
	return out
}

// RowKinds is the merged map of workflow row kinds.
func (s *Set) RowKinds() map[string]RowKind {
	if s == nil {
		return map[string]RowKind{}
	}
	return s.kinds
}

// HeaderExtra returns the first workflow's extra for a header row.
func (s *Set) HeaderExtra(app App, row Row, w int) (string, []text.Seg) {
	if s == nil {
		return "", nil
	}
	for _, wf := range s.Items {
		if c, segs := wf.HeaderExtra(app, row, w); c != "" || len(segs) > 0 {
			return c, segs
		}
	}
	return "", nil
}

// SummarySegs concatenates summary chips, a separator between workflows.
func (s *Set) SummarySegs(app App) []text.Seg {
	if s == nil {
		return nil
	}
	var out []text.Seg
	for _, w := range s.Items {
		segs := w.SummarySegs(app)
		if len(segs) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, text.S("  "+glyphs.G("sep")+"  ", "rule"))
		}
		out = append(out, segs...)
	}
	return out
}

// EnrichDetail runs every workflow's detail enrichment.
func (s *Set) EnrichDetail(ctx context.Context, gh ghapi.Client, pr, listed *model.PR) {
	if s == nil {
		return
	}
	for _, w := range s.Items {
		w.EnrichDetail(ctx, gh, pr, listed)
	}
}

// DetailCards concatenates detail cards.
func (s *Set) DetailCards(app App, pr, listed *model.PR, width int) []text.Line {
	if s == nil {
		return nil
	}
	var out []text.Line
	for _, w := range s.Items {
		out = append(out, w.DetailCards(app, pr, listed, width)...)
	}
	return out
}

// Approve lets the first workflow that handles the PR approve it.
func (s *Set) Approve(app App, pr, listed *model.PR) bool {
	if s == nil {
		return false
	}
	for _, w := range s.Items {
		if w.Approve(app, pr, listed) {
			return true
		}
	}
	return false
}

// MergeBlock returns the first reason a merge is refused, or "".
func (s *Set) MergeBlock(pr *model.PR, me string) string {
	if s == nil {
		return ""
	}
	for _, w := range s.Items {
		if r := w.MergeBlock(pr, me); r != "" {
			return r
		}
	}
	return ""
}

// MergeConfirm lets a workflow run the merge confirmation; handled false
// means the core asks its own question.
func (s *Set) MergeConfirm(app App, pr *model.PR) (bool, bool) {
	if s == nil {
		return false, false
	}
	for _, w := range s.Items {
		if ok, handled := w.MergeConfirm(app, pr); handled {
			return ok, true
		}
	}
	return false, false
}

// DeployTarget returns the first workflow's deploy target from the list.
func (s *Set) DeployTarget(app App) *model.PR {
	if s == nil {
		return nil
	}
	for _, w := range s.Items {
		if pr := w.DeployTarget(app); pr != nil {
			return pr
		}
	}
	return nil
}

// DeployRow lets the first workflow that handles the list row approve
// its deployments (RowDeployer); false lets D go on to DeployTarget.
func (s *Set) DeployRow(app App, row Row) bool {
	if s == nil {
		return false
	}
	for _, w := range s.Items {
		if d, ok := w.(RowDeployer); ok && d.DeployRow(app, row) {
			return true
		}
	}
	return false
}

// DeployComment returns the first workflow's deployment comment, or "".
func (s *Set) DeployComment(pr *model.PR, sha string) string {
	if s == nil {
		return ""
	}
	for _, w := range s.Items {
		if c := w.DeployComment(pr, sha); c != "" {
			return c
		}
	}
	return ""
}

// Keys lists the workflow keys for a view ("any" keys included).
func (s *Set) Keys(view string) []Key {
	if s == nil {
		return nil
	}
	var out []Key
	for _, k := range s.keys {
		if k.View == view || k.View == "any" {
			out = append(out, k)
		}
	}
	return out
}

// Key finds a binding for a key in a view.
func (s *Set) Key(view, ch string) (Key, bool) {
	for _, k := range s.Keys(view) {
		if k.Ch == ch {
			return k, true
		}
	}
	return Key{}, false
}

// HelpRows concatenates help rows.
func (s *Set) HelpRows() [][2]string {
	if s == nil {
		return nil
	}
	var out [][2]string
	for _, w := range s.Items {
		out = append(out, w.HelpRows()...)
	}
	return out
}

// LegendRows concatenates legend rows.
func (s *Set) LegendRows() [][]text.Seg {
	if s == nil {
		return nil
	}
	var out [][]text.Seg
	for _, w := range s.Items {
		out = append(out, w.LegendRows()...)
	}
	return out
}

// InlinePatterns concatenates markdown inline patterns.
func (s *Set) InlinePatterns() []InlinePattern {
	if s == nil {
		return nil
	}
	var out []InlinePattern
	for _, w := range s.Items {
		out = append(out, w.InlinePatterns()...)
	}
	return out
}

// LineRenderer returns the first workflow's rendering of a line, or nil.
func (s *Set) LineRenderer(line string, width int) []text.Line {
	if s == nil {
		return nil
	}
	for _, w := range s.Items {
		if ls := w.LineRenderer(line, width); ls != nil {
			return ls
		}
	}
	return nil
}

// CodeLineSegs returns the first workflow's coloring of a code line.
func (s *Set) CodeLineSegs(line, lang string) []text.Seg {
	if s == nil {
		return nil
	}
	for _, w := range s.Items {
		if segs := w.CodeLineSegs(line, lang); segs != nil {
			return segs
		}
	}
	return nil
}

// FortressItems lets every workflow place items on the map.
func (s *Set) FortressItems(app App, add func(item any)) {
	if s == nil {
		return
	}
	for _, w := range s.Items {
		w.FortressItems(app, add)
	}
}

// FortressLaneLabel returns the first workflow's label for a lane.
func (s *Set) FortressLaneLabel(repo model.Repo) string {
	if s == nil {
		return ""
	}
	for _, w := range s.Items {
		if l := w.FortressLaneLabel(repo); l != "" {
			return l
		}
	}
	return ""
}
