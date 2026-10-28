package workflow

import (
	"context"
	"strings"
	"testing"

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
func (s *stub) Refresh(context.Context, ghapi.Client, []model.Repo, []*model.PR, []*model.Issue) (State, []string) {
	return s.state, []string{s.name + " warn"}
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
	states, warns := s.RefreshAll(context.Background(), nil, nil, nil, nil)
	if len(states) != 2 || len(warns) != 2 || warns[0] != "a warn" {
		t.Errorf("refresh: %v %v", states, warns)
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
