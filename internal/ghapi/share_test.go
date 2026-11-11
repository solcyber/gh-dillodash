package ghapi

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSharedDedupesWithinARefresh: readers of one URL inside a Shared
// context cost one request, even side by side; a context without one
// (an action's) always asks again.
func TestSharedDedupesWithinARefresh(t *testing.T) {
	s := newServer(t)
	var hits atomic.Int32
	s.handle("GET", "/repos/acme/web/git/matching-refs/tags/go/", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond) // the readers overlap
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"ref":"refs/tags/go/7"}]`)
	})
	c := s.client(t)
	ctx := WithShared(context.Background())
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tags, err := c.TagRefs(ctx, webRepo, "go/")
			if err != nil || len(tags) != 1 || tags[0] != "go/7" {
				t.Errorf("tags %v err %v", tags, err)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d requests for one URL in one refresh", n)
	}
	if _, err := c.TagRefs(context.Background(), webRepo, "go/"); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("a read outside the refresh was answered from it (%d requests)", n)
	}
}

// TestConditionalGet: the second read of a URL sends If-None-Match with
// the ETag the first answered, and a 304 answers with the remembered
// body; a changed resource (new ETag) replaces it.
func TestConditionalGet(t *testing.T) {
	s := newServer(t)
	var mu sync.Mutex
	etag, body := `"v1"`, `[{"ref":"refs/tags/go/1"}]`
	var sent []string
	s.handle("GET", "/repos/acme/web/git/matching-refs/tags/go/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	c := s.client(t)
	read := func() []string {
		t.Helper()
		tags, err := c.TagRefs(context.Background(), webRepo, "go/")
		if err != nil {
			t.Fatal(err)
		}
		return tags
	}
	if got := read(); len(got) != 1 || got[0] != "go/1" {
		t.Fatalf("first read %v", got)
	}
	if got := read(); len(got) != 1 || got[0] != "go/1" {
		t.Fatalf("304 read %v", got)
	}
	mu.Lock()
	etag, body = `"v2"`, `[{"ref":"refs/tags/go/1"},{"ref":"refs/tags/go/2"}]`
	mu.Unlock()
	if got := read(); len(got) != 2 {
		t.Fatalf("changed read %v", got)
	}
	if len(sent) != 3 || sent[0] != "" || sent[1] != `"v1"` || sent[2] != `"v1"` {
		t.Errorf("If-None-Match sent %q", sent)
	}
}

// TestInFlightCap: however many readers, at most maxInFlight requests
// are open at once.
func TestInFlightCap(t *testing.T) {
	s := newServer(t)
	var open, peak atomic.Int32
	s.handle("GET", "/repos/acme/web/compare/main...stage", func(w http.ResponseWriter, r *http.Request) {
		n := open.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		open.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ahead_by":0}`)
	})
	c := s.client(t)
	var wg sync.WaitGroup
	for range 3 * maxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Compare(context.Background(), webRepo, "main", "stage"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > maxInFlight || p < 2 {
		t.Fatalf("peak in flight %d (cap %d)", p, maxInFlight)
	}
}
