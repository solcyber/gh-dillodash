package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
)

type stub struct {
	Base
	name  string
	order int
	kinds map[string]RowKind
	keys  []Key
	label string
	state State
}

func (s *stub) Name() string                 { return s.name }
func (s *stub) Order() int                   { return s.order }
func (s *stub) RowKinds() map[string]RowKind { return s.kinds }
func (s *stub) Keys() []Key                  { return s.keys }
func (s *stub) Classify(pr *model.PR, _ *model.PR, _ string, _ bool) *Classification {
	if s.label == "" {
		return nil
	}
	return &Classification{Label: s.label, Style: "chip.await", NeedsYou: true}
}
func (s *stub) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (State, []string, error) {
	return s.state, []string{s.name + " warn"}, nil
}
func (s *stub) Sections(App, []string, string) []Row {
	return []Row{{Kind: "header", Item: s.name}}
}
func (s *stub) LegendRows() [][]text.Seg { return [][]text.Seg{{text.S(s.name, "text")}} }

func TestSetOrderAndMerge(t *testing.T) {
	a := &stub{name: "b", order: 30, keys: []Key{{View: "list", Ch: "w", Label: "window"}}}
	b := &stub{name: "a", order: 20, label: "applied, needs review", kinds: map[string]RowKind{"queued": {}}}
	s, err := NewSet(nil, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); strings.Join(got, ",") != "a,b" {
		t.Errorf("order = %v", got)
	}
	if c := s.Classify(&model.PR{}, nil, "alice", false); c == nil || c.Label != "applied, needs review" {
		t.Errorf("classify = %+v", c)
	}
	if secs := s.Sections(nil, nil, ""); len(secs) != 2 || secs[0].Item != "a" {
		t.Errorf("sections = %+v", secs)
	}
	if k, ok := s.Key("list", "w"); !ok || k.Label != "window" {
		t.Errorf("key = %+v %v", k, ok)
	}
	if _, ok := s.Key("detail", "w"); ok {
		t.Error("w should not be bound in the detail view")
	}
	if s.Get("a") != b || s.Get("zzz") != nil {
		t.Error("Get")
	}
	states, warns, err := s.RefreshAll(context.Background(), nil, nil, nil, nil)
	if len(states) != 2 || len(warns) != 2 || warns[0] != "a warn" || err != nil {
		t.Errorf("refresh: %v %v %v", states, warns, err)
	}
	if rows := s.LegendRows(); len(rows) != 2 {
		t.Errorf("legend rows = %d", len(rows))
	}
}

func TestSetRejectsConflicts(t *testing.T) {
	if _, err := NewSet(nil, &stub{name: "x", kinds: map[string]RowKind{"pr": {}}}); err == nil {
		t.Error("core row kind should be rejected")
	}
	if _, err := NewSet(nil, &stub{name: "x", kinds: map[string]RowKind{"k": {}}}, &stub{name: "y", kinds: map[string]RowKind{"k": {}}}); err == nil {
		t.Error("duplicate row kind should be rejected")
	}
	if _, err := NewSet(nil, &stub{name: "x", keys: []Key{{View: "detail", Ch: "a"}}}); err == nil {
		t.Error("core key should be rejected")
	}
	if _, err := NewSet(nil, &stub{name: "x", keys: []Key{{View: "list", Ch: "w"}}}, &stub{name: "y", keys: []Key{{View: "list", Ch: "w"}}}); err == nil {
		t.Error("duplicate key should be rejected")
	}
}

func TestEmptySet(t *testing.T) {
	var s *Set
	if !s.Empty() || s.Claims(Row{}) || s.Classify(nil, nil, "", false) != nil || s.MergeBlock(nil, "") != "" {
		t.Error("nil set should be inert")
	}
	e, _ := NewSet(nil)
	if !e.Empty() || len(e.Keys("list")) != 0 || len(e.RowKinds()) != 0 {
		t.Error("empty set should be inert")
	}
}

type ciState struct{ prs []*model.PR }

func (s *ciState) CIPRs() []*model.PR { return s.prs }

type deployStub struct{ stub }

func (deployStub) ProvidesDeploys() bool { return true }

func TestCIPRsAndProvidesDeploys(t *testing.T) {
	a := &stub{name: "a"}
	b := &stub{name: "b", state: &ciState{prs: []*model.PR{{Number: 1}, {Number: 2}}}}
	s, err := NewSet(nil, a, b)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]State{"a": nil, "b": b.state, "zzz": (*ciState)(nil)}
	if got := s.CIPRs(states); len(got) != 2 {
		t.Errorf("CIPRs = %d", len(got))
	}
	var none *Set
	if none.CIPRs(states) != nil || none.ProvidesDeploys() {
		t.Error("a nil set contributes nothing")
	}
	if s.ProvidesDeploys() {
		t.Error("plain stubs provide no deploys")
	}
	d, err := NewSet(nil, &deployStub{stub{name: "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.ProvidesDeploys() {
		t.Error("a DeployProvider turns the D key on")
	}
}

// pollStub is a workflow with something in flight.
type pollStub struct {
	stub
	live    bool
	every   time.Duration
	patch   any
	err     error
	polls   int
	applied []any
}

func (p *pollStub) PollEvery() time.Duration { return p.every }
func (p *pollStub) Live(App) bool            { return p.live }
func (p *pollStub) Poll(context.Context, ghapi.Client) (any, error) {
	p.polls++
	return p.patch, p.err
}
func (p *pollStub) ApplyPoll(_ App, patch any) { p.applied = append(p.applied, patch) }

// TestPollersAggregate: the set polls at the shortest live cadence, 0
// when nothing is live; PollAll stops at the first rate limit and still
// returns the patches before it; ApplyPolls hands each its own.
func TestPollersAggregate(t *testing.T) {
	a := &pollStub{stub: stub{name: "a", order: 10}, live: true, every: 30 * time.Second, patch: "pa"}
	b := &pollStub{stub: stub{name: "b", order: 20}, live: false, every: 5 * time.Second, patch: "pb"}
	c := &stub{name: "c", order: 30}
	s, err := NewSet(nil, a, b, c)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.PollEvery(nil); d != 30*time.Second {
		t.Errorf("only live pollers count: %v", d)
	}
	b.live = true
	if d := s.PollEvery(nil); d != 5*time.Second {
		t.Errorf("shortest live cadence: %v", d)
	}
	a.live, b.live = false, false
	if d := s.PollEvery(nil); d != 0 {
		t.Errorf("idle: %v", d)
	}
	patches, err := s.PollAll(context.Background(), nil)
	if err != nil || len(patches) != 2 || patches["a"] != "pa" || patches["b"] != "pb" || a.polls != 1 || b.polls != 1 {
		t.Fatalf("PollAll: %v %v", patches, err)
	}
	s.ApplyPolls(nil, patches)
	if len(a.applied) != 1 || a.applied[0] != "pa" || len(b.applied) != 1 || b.applied[0] != "pb" {
		t.Errorf("applied: %v %v", a.applied, b.applied)
	}
	a.err = &ghapi.Error{Status: 403, Message: "rate limited", RetryAt: time.Now().Add(time.Minute)}
	patches, err = s.PollAll(context.Background(), nil)
	if !ghapi.IsRateLimit(err) || len(patches) != 1 || patches["a"] != "pa" || b.polls != 1 {
		t.Errorf("a rate limit stops the loop with a's patch kept: %v %v b.polls=%d", patches, err, b.polls)
	}
	if s.LiveRow(nil, Row{Kind: "x"}) {
		t.Error("an unknown kind is not live")
	}
	live := &stub{name: "d", order: 40, kinds: map[string]RowKind{"hot": {Live: func(App, Row) bool { return true }}}}
	s, _ = NewSet(nil, live)
	if !s.LiveRow(nil, Row{Kind: "hot"}) {
		t.Error("RowKind.Live")
	}
}
