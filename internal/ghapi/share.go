package ghapi

import (
	"context"
	"net/http"
	"sync"

	"golang.org/x/sync/singleflight"
)

// maxInFlight caps the requests one client has open at once, whoever
// sends them: the refresh runs its reads side by side, and GitHub's
// secondary limits frown on many concurrent requests from one token.
const maxInFlight = 8

// etagMax bounds the conditional-GET cache; past it the oldest half
// goes (a refresh reads well under a hundred distinct URLs).
const etagMax = 512

// etagEntry is a GET's last 200: its validator and body.
type etagEntry struct {
	etag string
	body []byte
	seq  uint64
}

// etagCache remembers the ETag and body of each REST GET, so the next
// read of the same URL asks If-None-Match: a 304 costs no rate limit
// and carries no body.
type etagCache struct {
	mu  sync.Mutex
	m   map[string]etagEntry
	seq uint64
}

func (c *etagCache) get(key string) (etagEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

func (c *etagCache) put(key, etag string, body []byte) {
	if etag == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]etagEntry{}
	}
	if len(c.m) >= etagMax {
		// drop the older half: cheap, and a refresh's working set fits
		cut := c.seq - uint64(etagMax/2)
		for k, e := range c.m {
			if e.seq <= cut {
				delete(c.m, k)
			}
		}
	}
	c.seq++
	c.m[key] = etagEntry{etag: etag, body: body, seq: c.seq}
}

// sharedKey is the context key of a Shared.
type sharedKey struct{}

// Shared lets the reads of one refresh share their answers: a REST GET
// of a URL another reader already fetched (or is fetching) in the same
// refresh returns that answer instead of asking again. Two workflows
// reading the same compare and tags then cost one request. It lives
// only as long as the context it rides on, so an action's reads (after
// it changed something) always go to GitHub.
type Shared struct {
	g  singleflight.Group
	mu sync.Mutex
	m  map[string][]byte
}

// WithShared returns ctx carrying a fresh Shared: the refresh worker
// wraps each refresh's context in one.
func WithShared(ctx context.Context) context.Context {
	return context.WithValue(ctx, sharedKey{}, &Shared{m: map[string][]byte{}})
}

func sharedFrom(ctx context.Context) *Shared {
	s, _ := ctx.Value(sharedKey{}).(*Shared)
	return s
}

// getShared is a REST GET's body: from the refresh's Shared when the
// context has one, else fetched (conditionally, through the ETag cache).
func (c *GitHub) getShared(ctx context.Context, rawURL, accept string) ([]byte, error) {
	s := sharedFrom(ctx)
	if s == nil {
		return c.getCached(ctx, rawURL, accept)
	}
	key := accept + " " + rawURL
	s.mu.Lock()
	body, ok := s.m[key]
	s.mu.Unlock()
	if ok {
		return body, nil
	}
	v, err, _ := s.g.Do(key, func() (any, error) {
		b, err := c.getCached(ctx, rawURL, accept)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.m[key] = b
		s.mu.Unlock()
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// getCached is a conditional GET: If-None-Match with the last ETag the
// URL answered, and the remembered body on a 304.
func (c *GitHub) getCached(ctx context.Context, rawURL, accept string) ([]byte, error) {
	key := accept + " " + rawURL
	var hdr map[string]string
	prev, ok := c.etags.get(key)
	if ok {
		hdr = map[string]string{"If-None-Match": prev.etag}
	}
	body, h, err := c.doH(ctx, http.MethodGet, rawURL, nil, accept, hdr)
	if err != nil {
		if e, isE := AsError(err); isE && e.Status == http.StatusNotModified && ok {
			return prev.body, nil
		}
		return nil, err
	}
	if h != nil {
		c.etags.put(key, h.Get("ETag"), body)
	}
	return body, nil
}

// acquire takes one of the client's request slots, or gives up when ctx
// ends first.
func (c *GitHub) acquire(ctx context.Context) error {
	if c.slots == nil {
		return nil
	}
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *GitHub) release() {
	if c.slots != nil {
		<-c.slots
	}
}
