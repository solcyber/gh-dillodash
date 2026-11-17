package releasepr

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Lease says someone is running a release of a repo: who, since when,
// and until when the claim stands. It is read from an issue and shown;
// it blocks nothing.
type Lease struct {
	Holder string
	Since  time.Time
	Until  time.Time
	Issue  int // the issue it was read from
}

// leaseRE finds the machine block in a lease issue's body:
//
//	<!-- release-lease:begin
//	{"leases": [{"repo": "web-portal", "holder": "carol", "since": "…", "until": "…"}]}
//	release-lease:end -->
var leaseRE = regexp.MustCompile(`(?s)<!-- release-lease:begin\s*(\{.*?\})\s*release-lease:end -->`)

// leaseRepoRE is a repo as an entry may name it, in full.
var leaseRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type leaseBlock struct {
	Leases []struct {
		Repo   string    `json:"repo"`
		Holder string    `json:"holder"`
		Since  time.Time `json:"since"`
		Until  time.Time `json:"until"`
	} `json:"leases"`
}

// ParseLeases reads the live leases of one issue body, by repo: an entry
// names its repo bare ("web-portal", in owner) or in full, has a holder
// and a start, and has not run out at now (no end means it stands).
func ParseLeases(body, owner string, issue int, now time.Time) map[model.Repo]*Lease {
	m := leaseRE.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var b leaseBlock
	if json.Unmarshal([]byte(m[1]), &b) != nil {
		return nil
	}
	out := map[model.Repo]*Lease{}
	for _, l := range b.Leases {
		holder := strings.TrimSpace(l.Holder)
		if holder == "" || l.Since.IsZero() || l.Since.After(now) || (!l.Until.IsZero() && !l.Until.After(now)) {
			continue
		}
		name := strings.TrimSpace(l.Repo)
		if !strings.Contains(name, "/") {
			name = owner + "/" + name
		}
		repo, ok := model.ParseRepo(name)
		if !ok || !leaseRepoRE.MatchString(name) {
			continue
		}
		// two entries for one repo: the one that began first is the release
		if cur, has := out[repo]; !has || l.Since.Before(cur.Since) {
			out[repo] = &Lease{Holder: holder, Since: l.Since, Until: l.Until, Issue: issue}
		}
	}
	return out
}

// leases reads the live leases from the open issues carrying
// lease_label. No label configured, or a failed read, is no leases: the
// row then says nothing about one (the error is the caller's to warn).
func (w *Workflow) leases(ctx context.Context, gh ghapi.Client, now time.Time) (map[model.Repo]*Lease, error) {
	if w.cfg.LeaseLabel == "" || w.leaseRepo.IsZero() {
		return nil, nil
	}
	issues, err := gh.IssuesWithLabel(ctx, w.leaseRepo, w.cfg.LeaseLabel, 5)
	if err != nil {
		return nil, err
	}
	out := map[model.Repo]*Lease{}
	for _, i := range issues {
		if i == nil || (i.State != "" && i.State != "OPEN") {
			continue
		}
		for repo, l := range ParseLeases(i.Body, w.leaseRepo.Owner, i.Number, now) {
			if cur, has := out[repo]; !has || l.Since.Before(cur.Since) {
				out[repo] = l
			}
		}
	}
	return out, nil
}

// LeaseOf is the live lease on repo in the last applied state, for the
// other workflows that write below production (the promote steps).
func (s *State) LeaseOf(repo model.Repo) *Lease {
	if s == nil {
		return nil
	}
	for _, st := range s.Stages {
		if st != nil && st.Repo == repo {
			return st.Lease
		}
	}
	return nil
}

// LeaseHolder is LeaseOf's holder, or "".
func (s *State) LeaseHolder(repo model.Repo) string {
	if l := s.LeaseOf(repo); l != nil {
		return l.Holder
	}
	return ""
}
