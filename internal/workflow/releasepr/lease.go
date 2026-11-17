package releasepr

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/text"
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

// leaseRE finds the machine blocks in a lease issue's body:
//
//	<!-- release-lease:begin
//	{"leases": [{"repo": "web-portal", "holder": "carol", "since": "…", "until": "…"}]}
//	release-lease:end -->
var leaseRE = regexp.MustCompile(`(?s)<!-- release-lease:begin\s*(\{.*?\})\s*release-lease:end\s*-->`)

// leaseRepoRE is a repo as an entry may name it, in full; leaseHolderRE
// a holder: a login or a session name, short and on one line, because
// it is printed in a question the reader answers.
var (
	leaseRepoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	leaseHolderRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\[\]-]{0,39}$`)
)

// leaseSkew is how far ahead of this clock a lease may be stamped and
// still count as begun; leaseMax is how long one without an end stands.
const (
	leaseSkew = 2 * time.Minute
	leaseMax  = 24 * time.Hour
)

// The times are read as text and parsed entry by entry: one bad stamp
// must not hide every other lease.
type leaseBlock struct {
	Leases []struct {
		Repo   string `json:"repo"`
		Holder string `json:"holder"`
		Since  string `json:"since"`
		Until  string `json:"until"`
	} `json:"leases"`
}

// ParseLeases reads the live leases of one issue body, keyed by the
// lower-cased "owner/name" (names are case-insensitive), with a note for
// every block or entry it could not use. An entry names its repo bare
// ("web-portal", in owner) or in full, has a holder and a start, and
// has not run out at now: past its "until", or a day old without one.
// The last block in the body is the one read (an example may precede
// it). Of two entries for one repo the one that began last stands: a
// leftover never outlives the lease that replaced it.
func ParseLeases(body, owner string, issue int, now time.Time) (map[string]*Lease, []string) {
	all := leaseRE.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return nil, nil
	}
	var b leaseBlock
	if err := json.Unmarshal([]byte(all[len(all)-1][1]), &b); err != nil {
		return nil, []string{fmt.Sprintf("lease issue #%d: its block is not readable JSON", issue)}
	}
	out := map[string]*Lease{}
	var notes []string
	skip := func(repo, why string) {
		notes = append(notes, fmt.Sprintf("lease issue #%d: the entry for %q is skipped (%s)", issue, text.Ellip(repo, 40), why))
	}
	for _, l := range b.Leases {
		name := strings.TrimSpace(l.Repo)
		if !strings.Contains(name, "/") {
			name = owner + "/" + name
		}
		if !leaseRepoRE.MatchString(name) {
			skip(l.Repo, "not a repo name")
			continue
		}
		holder := strings.TrimSpace(l.Holder)
		if !leaseHolderRE.MatchString(holder) {
			skip(l.Repo, "no usable holder")
			continue
		}
		since, err := time.Parse(time.RFC3339, strings.TrimSpace(l.Since))
		if err != nil {
			skip(l.Repo, "bad since")
			continue
		}
		var until time.Time
		if u := strings.TrimSpace(l.Until); u != "" {
			if until, err = time.Parse(time.RFC3339, u); err != nil {
				skip(l.Repo, "bad until")
				continue
			}
		} else {
			until = since.Add(leaseMax)
		}
		if since.After(now.Add(leaseSkew)) || !until.After(now) {
			continue // not begun, or run out: not an error
		}
		key := strings.ToLower(name)
		if cur, has := out[key]; !has || since.After(cur.Since) {
			out[key] = &Lease{Holder: holder, Since: since, Until: until, Issue: issue}
		}
	}
	return out, notes
}

// leases reads the live leases from the open issues carrying
// lease_label. No label configured, or a failed read, is no leases: the
// row then says nothing about one (the error is the caller's to warn).
func (w *Workflow) leases(ctx context.Context, gh ghapi.Client, now time.Time) (map[string]*Lease, []string, error) {
	if w.cfg.LeaseLabel == "" || w.leaseRepo.IsZero() {
		return nil, nil, nil
	}
	issues, err := gh.IssuesWithLabel(ctx, w.leaseRepo, w.cfg.LeaseLabel, 5)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]*Lease{}
	var notes []string
	for _, i := range issues {
		if i == nil || (i.State != "" && i.State != "OPEN") {
			continue
		}
		found, ns := ParseLeases(i.Body, w.leaseRepo.Owner, i.Number, now)
		notes = append(notes, ns...)
		for key, l := range found {
			if cur, has := out[key]; !has || l.Since.After(cur.Since) {
				out[key] = l
			}
		}
	}
	return out, notes, nil
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
