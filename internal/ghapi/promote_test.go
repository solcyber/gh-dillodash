package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

var webRepo = model.Repo{Owner: "acme", Name: "web"}

func prNode(n int) string {
	return fmt.Sprintf(`{"number":%d,"title":"t%d","state":"MERGED","mergeCommit":{"oid":"m%d"},"author":{"login":"bob","__typename":"User"}}`, n, n, n)
}

// TestPromotePRsPages: each search is its own request (side by side);
// a search past its page reads the next pages by cursor up to
// promoteMax, and a note says what is still left out.
func TestPromotePRsPages(t *testing.T) {
	s := newServer(t)
	var mu sync.Mutex
	calls := 0
	seen := map[string]bool{}
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		mu.Lock()
		calls++
		q, _ := req.Variables["q"].(string)
		seen[q] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		page := func(from, to int, next bool) string {
			var nodes []string
			for i := from; i < to; i++ {
				nodes = append(nodes, prNode(i+1))
			}
			return fmt.Sprintf(`{"issueCount":260,"pageInfo":{"hasNextPage":%v,"endCursor":"c%d"},"nodes":[%s]}`, next, to, strings.Join(nodes, ","))
		}
		if !strings.Contains(q, "is:merged base:dev ") {
			io.WriteString(w, `{"data":{"s":{"issueCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}}`)
			return
		}
		from := 0
		if after, ok := req.Variables["after"].(string); ok {
			fmt.Sscanf(after, "c%d", &from)
		}
		io.WriteString(w, `{"data":{"s":`+page(from, from+promoteFirst, true)+`}}`)
	})
	set, err := s.client(t).PromotePRs(context.Background(), webRepo, "dev", "stage", "main", time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// first: two pages of 100 (then promoteMax); the other three: one each
	if len(set.FirstMerged) != promoteMax || calls != 5 {
		t.Fatalf("first %d PRs in %d calls", len(set.FirstMerged), calls)
	}
	for _, want := range []string{"is:pr is:merged base:dev merged:>=2026-10-23", "is:pr is:open base:stage", "is:pr is:merged base:main merged:>=2026-10-23", "is:pr is:merged base:stage merged:>=2026-10-23"} {
		found := false
		for q := range seen {
			found = found || strings.Contains(q, want)
		}
		if !found {
			t.Errorf("no search for %q in %v", want, seen)
		}
	}
	if p := set.FirstMerged[199]; p.Number != 200 || p.Repo != webRepo || p.MergeCommit.Oid != "m200" || p.Author.IsApp() {
		t.Errorf("last PR %+v", p)
	}
	if len(set.Notes) != 1 || !strings.Contains(set.Notes[0], "showing 200 of 260") {
		t.Errorf("notes %v", set.Notes)
	}
}

// TestPromoteWrites: the commit goes through createCommitOnBranch with
// the expected head and base64 contents; a tag is a tag object, then its
// ref; a missing file is "no such file", not an error.
func TestPromoteWrites(t *testing.T) {
	s := newServer(t)
	var mutation gqlReq
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		mutation = req
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"createCommitOnBranch":{"commit":{"oid":"newoid"}}}}`)
	})
	c := s.client(t)
	oid, err := c.CommitOnBranch(context.Background(), webRepo, "promote/1-stage", "head1", "Promote #1", "body",
		[]model.FileAdd{{Path: "a.sql", Base64: "QQ=="}}, []string{"old.txt"})
	if err != nil || oid != "newoid" {
		t.Fatalf("oid %q err %v", oid, err)
	}
	in, _ := json.Marshal(mutation.Variables["input"])
	for _, want := range []string{`"expectedHeadOid":"head1"`, `"branchName":"promote/1-stage"`, `"repositoryNameWithOwner":"acme/web"`,
		`"additions":[{"contents":"QQ==","path":"a.sql"}]`, `"deletions":[{"path":"old.txt"}]`} {
		if !strings.Contains(string(in), want) {
			t.Errorf("input lacks %s: %s", want, in)
		}
	}
	if !strings.HasPrefix(mutation.Query, "mutation") {
		t.Errorf("query %q", mutation.Query)
	}

	s.jsonOK("POST", "/repos/acme/web/git/tags", `{"sha":"tagobj"}`)
	s.jsonOK("POST", "/repos/acme/web/git/refs", `{}`)
	if err := c.CreateTag(context.Background(), webRepo, "go/12", "go", "unit12"); err != nil {
		t.Fatal(err)
	}
	req, ref := s.last()
	if req.URL.Path != "/repos/acme/web/git/refs" {
		t.Errorf("last request %s", req.URL.Path)
	}
	if !strings.Contains(ref, `"ref":"refs/tags/go/12"`) || !strings.Contains(ref, `"sha":"tagobj"`) {
		t.Errorf("ref body %s", ref)
	}

	sha, ok, err := c.BlobAt(context.Background(), webRepo, "db/none.sql", "stage")
	if err != nil || ok || sha != "" {
		t.Errorf("missing file: %q %v %v", sha, ok, err)
	}
	s.jsonOK("GET", "/repos/acme/web/contents/db/a b.sql", `{"type":"file","sha":"blob1"}`)
	if sha, ok, err := c.BlobAt(context.Background(), webRepo, "db/a b.sql", "stage"); err != nil || !ok || sha != "blob1" {
		t.Errorf("file: %q %v %v", sha, ok, err)
	}
}
