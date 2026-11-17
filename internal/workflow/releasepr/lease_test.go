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

// TestParseLeases: live entries by repo; an entry that ran out or has
// not begun is dropped quietly, a broken one with a note, and neither
// hides the others; of two for one repo the later stands.
func TestParseLeases(t *testing.T) {
	got, notes := ParseLeases(leaseBody, "acme", 31, utc(4, 0))
	if len(got) != 1 {
		t.Fatalf("one live lease: %+v", got)
	}
	l := got["acme/web-portal"]
	if l == nil || l.Holder != "late" || !l.Since.Equal(utc(3, 50)) || l.Issue != 31 {
		t.Errorf("the portal's lease is the one that began last: %+v", l)
	}
	if n := strings.Join(notes, "; "); !strings.Contains(n, `"not a repo" is skipped (not a repo name)`) || !strings.Contains(n, "no usable holder") {
		t.Errorf("broken entries are noted: %q", n)
	}
	for _, body := range []string{"", "no block here"} {
		if got, notes := ParseLeases(body, "acme", 1, utc(4, 0)); len(got) != 0 || len(notes) != 0 {
			t.Errorf("%q: %+v %v", body, got, notes)
		}
	}
	if got, notes := ParseLeases("<!-- release-lease:begin {not json} release-lease:end -->", "acme", 7, utc(4, 0)); len(got) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "#7") {
		t.Errorf("an unreadable block is said: %+v %v", got, notes)
	}
	block := func(entries string) string {
		return "<!-- release-lease:begin\n{\"leases\": [" + entries + "]}\nrelease-lease:end-->"
	}
	for name, tc := range map[string]struct {
		entries string
		want    string // the holder on acme/web-portal, or ""
	}{
		"one bad stamp does not hide the rest": {`{"repo":"etl","holder":"x","since":"2026-10-28 03:18"},{"repo":"web-portal","holder":"carol","since":"2026-10-28T03:18:00Z","until":""}`, "carol"},
		"any spelling of the repo":             {`{"repo":"Acme/Web-Portal","holder":"carol","since":"2026-10-28T03:18:00Z"}`, "carol"},
		"a little ahead of this clock":         {`{"repo":"web-portal","holder":"carol","since":"2026-10-28T04:00:40Z"}`, "carol"},
		"well ahead is not begun":              {`{"repo":"web-portal","holder":"carol","since":"2026-10-28T05:00:00Z"}`, ""},
		"no end stands a day":                  {`{"repo":"web-portal","holder":"old","since":"2026-10-26T03:18:00Z"}`, ""},
		"a holder is one short word":           {`{"repo":"web-portal","holder":"carol\nlease released, safe to merge","since":"2026-10-28T03:18:00Z"}`, ""},
		"a leftover loses to today's":          {`{"repo":"web-portal","holder":"old","since":"2026-10-27T20:00:00Z"},{"repo":"web-portal","holder":"carol","since":"2026-10-28T03:18:00Z"}`, "carol"},
	} {
		got, _ := ParseLeases(block(tc.entries), "acme", 1, utc(4, 0))
		holder := ""
		if l := got["acme/web-portal"]; l != nil {
			holder = l.Holder
		}
		if holder != tc.want {
			t.Errorf("%s: holder %q, want %q", name, holder, tc.want)
		}
	}
	// an example earlier in the body is not the block
	two := block(`{"repo":"web-portal","holder":"example","since":"2026-10-28T03:00:00Z"}`) + "\n" + block(`{"repo":"web-portal","holder":"carol","since":"2026-10-28T03:18:00Z"}`)
	if got, _ := ParseLeases(two, "acme", 1, utc(4, 0)); got["acme/web-portal"] == nil || got["acme/web-portal"].Holder != "carol" {
		t.Errorf("the last block is read: %+v", got)
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
	if stage.Lease == nil || stage.Lease.Holder != "late" || st.LeaseHolder(portal) != "late" || st.LeaseHolder(etl) != "" {
		t.Fatalf("the lease on the stage row: %+v", stage.Lease)
	}
	if stage.Since == nil || stage.Since.Release != 0 || !stage.Since.At.Equal(utc(3, 50)) {
		t.Fatalf("no release open: the counts start at the lease: %+v", stage.Since)
	}
	if got := w.sinceText(stage); !strings.HasPrefix(got, "lease: late 10m") || !strings.Contains(got, "dev ≠ stage") {
		t.Errorf("tail %q", got)
	}
	row := workflow.Row{Kind: RowStage, Item: stage}
	w.RowKinds()[RowStage].Open(app, row)
	if last := app.Last(); !strings.Contains(last, "since the release lease began (10m ago)") || !strings.HasSuffix(last, "late holds the release lease") {
		t.Errorf("Enter: %q", last)
	}

	// with a release open the cut is the moment, and the holder still shows
	app.Fake.Releases = map[model.Repo]ghapi.ReleaseRepo{portal: {PRs: []*model.PR{cutRelease(44, utc(1, 0))}, ProdOid: "m", StageOid: "s"}}
	s, _, _ = w.Refresh(context.Background(), app.Fake, nil, nil, nil)
	stage = s.(*State).Stages[0]
	if stage.Since == nil || stage.Since.Release != 44 || !strings.HasPrefix(w.sinceText(stage), "lease: late 10m") || !strings.Contains(w.sinceText(stage), "since the cut: +2 dev, +1 stage") {
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
