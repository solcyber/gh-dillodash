package releasepr

import (
	"context"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
	"github.com/solcyber/gh-dillodash/internal/workflow"
)

const leaseBody = `Who is cutting which release.

<!-- release-lease:begin
{"leases": [
 {"repo": "web-portal", "holder": "carol", "since": "2026-10-28T03:18:00Z", "until": "2026-10-28T15:18:00Z"},
 {"repo": "acme/etl-jobs", "holder": "dave", "since": "2026-10-28T01:00:00Z", "until": "2026-10-28T02:00:00Z"},
 {"repo": "web-portal", "holder": "late", "since": "2026-10-28T03:50:00Z"},
 {"repo": "not a repo", "holder": "x", "since": "2026-10-28T03:00:00Z"},
 {"repo": "web-portal", "holder": "", "since": "2026-10-28T02:00:00Z"},
 {"repo": "billing", "holder": "erin", "since": "2026-10-28T09:00:00Z"}
]}
release-lease:end -->`

// TestParseLeases: live entries by repo; an entry that ran out, has not
// begun, has no holder or names no repo is dropped; of two for one repo
// the earlier stands.
func TestParseLeases(t *testing.T) {
	got := ParseLeases(leaseBody, "acme", 31, utc(4, 0))
	if len(got) != 1 {
		t.Fatalf("one live lease: %+v", got)
	}
	l := got[portal]
	if l == nil || l.Holder != "carol" || !l.Since.Equal(utc(3, 18)) || l.Issue != 31 {
		t.Errorf("the portal's lease: %+v", l)
	}
	for _, body := range []string{"", "no block here", "<!-- release-lease:begin {not json} release-lease:end -->"} {
		if got := ParseLeases(body, "acme", 1, utc(4, 0)); len(got) != 0 {
			t.Errorf("%q: %+v", body, got)
		}
	}
}

// TestStageRowLease: with lease_label set, the stage row names the
// holder; with no release open yet the counts start at the lease; Enter
// says who holds it; a failed read warns and shows nothing.
func TestStageRowLease(t *testing.T) {
	w, app, _ := sinceWF(t, nil, [2]string{"treeA", "treeB"})
	w.cfg.LeaseLabel = "release-lease"
	w.leaseRepo = infra
	app.Fake.Labelled = map[model.Repo]map[string][]*model.Issue{infra: {"release-lease": {{Repo: infra, Number: 31, State: "OPEN", Body: leaseBody}}}}
	s, _, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	st := s.(*State)
	w.Apply(app, st)
	stage := st.Stages[0]
	if stage.Lease == nil || stage.Lease.Holder != "carol" || st.LeaseHolder(portal) != "carol" || st.LeaseHolder(etl) != "" {
		t.Fatalf("the lease on the stage row: %+v", stage.Lease)
	}
	if stage.Since == nil || stage.Since.Release != 0 || !stage.Since.At.Equal(utc(3, 18)) {
		t.Fatalf("no release open: the counts start at the lease: %+v", stage.Since)
	}
	if got := w.sinceText(stage); !strings.HasPrefix(got, "lease: carol 42m") || !strings.Contains(got, "dev ≠ stage") {
		t.Errorf("tail %q", got)
	}
	row := workflow.Row{Kind: RowStage, Item: stage}
	w.RowKinds()[RowStage].Open(app, row)
	if last := app.Last(); !strings.Contains(last, "since the release lease began (42m ago)") || !strings.HasSuffix(last, "carol holds the release lease") {
		t.Errorf("Enter: %q", last)
	}

	// with a release open the cut is the moment, and the holder still shows
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{cutRelease(44, utc(1, 0))}, ProdOid: "m", StageOid: "s"}}
	s, _, _ = w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	stage = s.(*State).Stages[0]
	if stage.Since == nil || stage.Since.Release != 44 || !strings.HasPrefix(w.sinceText(stage), "lease: carol 42m") || !strings.Contains(w.sinceText(stage), "since the cut: +2 dev, +1 stage") {
		t.Errorf("a cut and a lease: %+v %q", stage.Since, w.sinceText(stage))
	}

	// a failed read: a warning, no lease, and the row stands
	app.Fake.Fail = map[string]error{"labelled": context.DeadlineExceeded}
	s, warns, _ := w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	stage = s.(*State).Stages[0]
	if stage.Lease != nil || !strings.Contains(strings.Join(warns, "; "), "release leases:") {
		t.Errorf("failed read: lease %+v warns %v", stage.Lease, warns)
	}
}
