package ui

import (
	"fmt"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// budget is the header meter's reading: the tighter of GitHub's REST
// (core) and GraphQL budgets as the last answers reported them, labelled
// "rest" or "gql"; else the GraphQL list's own rateLimit read.
func (m *Model) budget() (*model.RateLimit, string) {
	if m.usage != nil {
		if b, ok := m.usage.Snapshot().Tightest(clock.Now()); ok {
			which := "gql"
			if b.Resource == "core" {
				which = "rest"
			}
			return &model.RateLimit{Limit: b.Limit, Remaining: b.Remaining, ResetAt: b.Reset}, which
		}
	}
	if m.gh == nil {
		return nil, ""
	}
	return m.gh.LastRate(), "gql"
}

// usageLine is the help's footer about what this dashboard has asked of
// GitHub: calls, the free 304s among them, GraphQL points an hour, and
// the source that spent the most ("" before the first request).
func (m *Model) usageLine() string {
	if m.usage == nil {
		return ""
	}
	s := m.usage.Snapshot()
	t := s.Total()
	if t.Calls() == 0 {
		return ""
	}
	hours := max(clock.Now().Sub(s.Since).Hours(), 1.0/60)
	sep := " " + glyphs.G("sep") + " "
	line := fmt.Sprintf("GitHub this session: %s calls (%s free 304s)%s%s gql points/h", commas(t.Calls()), commas(t.REST304), sep, commas(int(float64(t.GQLCost)/hours+0.5)))
	if b, ok := s.Budgets["core"]; ok && clock.Now().Sub(b.At) < time.Hour {
		line += fmt.Sprintf("%srest %s/%s", sep, commas(b.Remaining), commas(b.Limit))
	}
	if src := s.Sources(); len(src) > 0 {
		c := s.By[src[0]]
		line += fmt.Sprintf("%smost: %s (%s calls, %s pts)", sep, src[0], commas(c.Calls()), commas(c.GQLCost))
	}
	return line
}
