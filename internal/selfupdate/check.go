package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/ghapi"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Source is what the check needs of GitHub.
type Source interface {
	LatestRelease(ctx context.Context, repo model.Repo, etag string) (*ghapi.Release, string, error)
}

// cache is the last answer, kept on disk so a conditional request can
// send its ETag, and so an offline start still knows the latest release.
type cache struct {
	ETag      string         `json:"etag,omitempty"`
	Release   *ghapi.Release `json:"release,omitempty"`
	CheckedAt time.Time      `json:"checked_at,omitzero"`
	Err       string         `json:"err,omitempty"`
	ErrAt     time.Time      `json:"err_at,omitzero"`
}

// MinInterval is how soon after an answer another check reuses it
// instead of asking: `config check` right after the dashboard started,
// or two sessions opened together, ask once.
const MinInterval = time.Minute

// Checker reads the latest release, conditionally (the cached ETag), and
// remembers the answer in Path.
type Checker struct {
	Src  Source
	Path string // the cache file; "" keeps nothing
	Now  func() time.Time
	Find FindOptions

	mu   sync.Mutex
	last *ghapi.Release // the release the latest Check read, cache or not
}

// CachePath is the cache file under $XDG_CACHE_HOME (else ~/.cache).
func CachePath(getenv func(string) string, home string) string {
	if d := getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "dillodash", "latest-release.json")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "dillodash", "latest-release.json")
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) load() cache {
	var k cache
	if c.Path == "" {
		return k
	}
	if raw, err := os.ReadFile(c.Path); err == nil {
		_ = json.Unmarshal(raw, &k) // a broken cache is an empty one
	}
	return k
}

func (c *Checker) save(k cache) {
	if c.Path == "" {
		return
	}
	if raw, err := json.Marshal(k); err == nil {
		_ = config.WriteFileAtomic(c.Path, raw, 0o600) // a cache that can't be written costs one request
	}
}

// Check reads the latest release and returns the status. force skips
// the MinInterval reuse. A failed check is quiet: it keeps the last
// answer (however old) and records the failure for `config check`.
func (c *Checker) Check(ctx context.Context, force bool) Status {
	k := c.load()
	now := c.now()
	fresh := k.Release != nil && now.Sub(k.CheckedAt) < MinInterval && now.Sub(k.CheckedAt) >= 0
	if c.Src != nil && (force || !fresh) {
		etag := k.ETag
		if k.Release == nil {
			etag = ""
		}
		rel, tag, err := c.Src.LatestRelease(ctx, Repo, etag)
		switch {
		case errors.Is(err, ghapi.ErrNotModified):
			k.CheckedAt, k.Err = now, ""
		case err != nil:
			k.Err, k.ErrAt = err.Error(), now
		case rel == nil || rel.Draft || rel.Prerelease || !Comparable(rel.Tag):
			// never a candidate: keep what we had
			k.Err, k.ErrAt = "the latest release is not a stable version", now
		default:
			k.Release, k.ETag, k.CheckedAt, k.Err = rel, tag, now, ""
		}
		c.save(k)
	}
	c.mu.Lock()
	c.last = k.Release
	c.mu.Unlock()
	st := Status{Running: c.Find.Running, CheckedAt: k.CheckedAt, Err: k.Err, Self: Self(c.Find)}
	if k.Release != nil {
		st.Latest, st.Notes, st.URL = k.Release.Tag, k.Release.Body, k.Release.HTMLURL
	}
	return st
}

// Release is the cached latest release (nil when none was ever read).
// It is the one the latest Check read (an install never re-reads a
// cache file another process may have changed, or one that could not
// be written), else the cache's.
func (c *Checker) Release() *ghapi.Release {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	if last != nil {
		return last
	}
	return c.load().Release
}
