package ghapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMergedSince: one query carries both searches and both trees; the
// PRs come back oldest first, the counts are the search's totals, and
// equal trees are the same content.
func TestMergedSince(t *testing.T) {
	s := newServer(t)
	var vars map[string]any
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		vars = req.Variables
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"r0":{"first":{"target":{"tree":{"oid":"t1"}}},"stage":{"target":{"tree":{"oid":"t1"}}}},
			"mFirst":{"issueCount":41,"nodes":[
				{"number":9,"title":"later","mergedAt":"2026-10-23T11:00:00Z","author":{"login":"bob"}},
				{},
				{"number":7,"title":"earlier","mergedAt":"2026-10-23T10:00:00Z","author":{"login":"carol"}}]},
			"mStage":{"issueCount":0,"nodes":[]}}}`)
	})
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	got, err := s.client(t).MergedSince(context.Background(), webRepo, "dev", "stage", at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.First) != 2 || got.First[0].Number != 7 || got.First[1].Number != 9 || got.FirstCount != 41 {
		t.Errorf("first tier: oldest first, the total kept: %+v (%d)", got.First, got.FirstCount)
	}
	if len(got.Stage) != 0 || got.StageCount != 0 || !got.Same() || !got.Known() {
		t.Errorf("stage and trees: %+v", got)
	}
	want := "repo:" + webRepo.String() + " is:pr is:merged base:dev merged:>2026-10-23T09:00:00Z sort:created-asc"
	if vars["qFirst"] != want || vars["firstRef"] != "refs/heads/dev" || vars["stageRef"] != "refs/heads/stage" {
		t.Errorf("variables: %v", vars)
	}

	// a branch that is not there: its tree is unknown, never "the same"
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"r0":{"first":null,"stage":{"target":{"tree":{"oid":"t1"}}}},"mFirst":{"issueCount":0,"nodes":[]},"mStage":{"issueCount":0,"nodes":[]}}}`)
	})
	got, err = s.client(t).MergedSince(context.Background(), webRepo, "dev", "stage", at)
	if err != nil || got.Same() || got.Known() {
		t.Errorf("a missing branch: %+v %v", got, err)
	}

	// a search that did not answer is an error, never "nothing merged"
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"r0":{"first":{"target":{"tree":{"oid":"t1"}}},"stage":{"target":{"tree":{"oid":"t2"}}}},"mFirst":null,"mStage":{"issueCount":0,"nodes":[]}},
			"errors":[{"path":["mFirst"],"message":"the search timed out"}]}`)
	})
	if _, err = s.client(t).MergedSince(context.Background(), webRepo, "dev", "stage", at); err == nil || !strings.Contains(err.Error(), "the search timed out") {
		t.Errorf("a search with no answer: %v", err)
	}
}

// TestBranchRules: the required checks of every ruleset on the branch,
// de-duped, and the strictest review rule.
func TestBranchRules(t *testing.T) {
	s := newServer(t)
	s.handle("GET", "/repos/"+webRepo.String()+"/rules/branches/main", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[
			{"type":"deletion"},
			{"type":"pull_request","parameters":{"required_approving_review_count":0,"require_code_owner_review":true}},
			{"type":"pull_request","parameters":{"required_approving_review_count":1,"require_code_owner_review":false}},
			{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"e2e"}]}},
			{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"}]}}]`)
	})
	got, err := s.client(t).BranchRules(context.Background(), webRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Known || strings.Join(got.RequiredChecks, ",") != "build,e2e" || got.Approvals != 1 || !got.CodeOwner {
		t.Errorf("rules: %+v", got)
	}
}
