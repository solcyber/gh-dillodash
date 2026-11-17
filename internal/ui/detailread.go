package ui

import (
	"fmt"
	"maps"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// Opening an item never waits on GitHub. The detail view switches on the
// keypress: to a full read kept from a moment ago (detailCache) when it
// is as new as the list row, else to the list's own copy of the item
// (detailPartial) while the full read runs beside the UI. Keys that act
// wait for the full read; everything else works at once. Resting the
// cursor on a row reads its detail ahead (prefetch), so most opens are
// complete on the first frame.

const (
	// detailCacheSize is how many full reads are kept.
	detailCacheSize = 32
	// detailRevalidateSecs: a kept read older than this is shown, read
	// again behind it, and the keys that act wait for that read.
	detailRevalidateSecs = 15.0
	// prefetchRest is how long the cursor rests on a row before its
	// detail is read ahead.
	prefetchRest = 250 * time.Millisecond
)

// detailEntry is one kept full read.
type detailEntry struct {
	detail  any     // *model.PR | *model.Issue
	started float64 // when its read set off (fx clock)
	used    int     // LRU stamp
}

// detailCache keeps the last full reads by item (lock held throughout).
type detailCache struct {
	m    map[model.Ident]*detailEntry
	tick int
	// epoch: reads that set off before it are not kept (a write since
	// may have changed what they read)
	epoch float64
	// reread: the read the write itself set off after GitHub accepted it
	// (reloadDetail). It sees the write, so it is kept although the
	// action's end moves the epoch past it.
	reread float64
}

// clear forgets every kept read, and refuses the ones already on their
// way: called when something was written.
func (c *detailCache) clear() {
	c.m = nil
	c.epoch = fx.Now()
	c.reread = 0
}

// settle is clear at an action's end: the action may have written
// without saying so, but the re-read it set off after its last write
// already sees every write, so it stays kept (or is kept when it lands).
func (c *detailCache) settle() {
	var keep map[model.Ident]*detailEntry
	for id, e := range c.m {
		if c.reread != 0 && e.started == c.reread {
			keep = map[model.Ident]*detailEntry{id: e}
		}
	}
	c.m = keep
	c.epoch = fx.Now()
}

func (c *detailCache) get(id model.Ident) (*detailEntry, bool) {
	if c == nil || c.m == nil {
		return nil, false
	}
	e, ok := c.m[id]
	if ok {
		c.tick++
		e.used = c.tick
	}
	return e, ok
}

func (c *detailCache) put(id model.Ident, d any, started float64) {
	if started < c.epoch && (c.reread == 0 || started != c.reread) {
		return // it set off before a write: it may not show it
	}
	if c.m == nil {
		c.m = map[model.Ident]*detailEntry{}
	}
	if e, ok := c.m[id]; ok && e.started > started {
		return // a newer read is already kept
	}
	c.tick++
	c.m[id] = &detailEntry{detail: d, started: started, used: c.tick}
	for len(c.m) > detailCacheSize {
		var oldest model.Ident
		least := -1
		for k, e := range c.m {
			if least < 0 || e.used < least {
				oldest, least = k, e.used
			}
		}
		delete(c.m, oldest)
	}
}

// detailLoadedMsg is a detail read landing on the UI goroutine.
type detailLoadedMsg struct {
	id       model.Ident
	kind     string
	listed   *model.PR
	seq      int  // the open it answers (openSeq); 0 for a prefetch
	pending  bool // the view switches only when it lands (nothing listed to show first)
	prefetch bool
	started  float64
	detail   any
	err      error
}

// prefetchMsg is the cursor's rest timer.
type prefetchMsg struct {
	gen int
	id  model.Ident
}

// identOf is an open item's identity.
func (m *Model) identOf(d any) (model.Ident, bool) {
	switch v := d.(type) {
	case *model.PR:
		return model.Ident{Repo: m.repoOf(v), Number: v.Number}, true
	case *model.Issue:
		return model.Ident{Repo: m.repoOf(v), Number: v.Number}, true
	}
	return model.Ident{}, false
}

// listedCopy is the list's own copy of an item: a row (a workflow's too),
// else the review PRs and issues the last refresh read.
func (m *Model) listedCopy(id model.Ident) (*model.PR, *model.Issue) {
	for _, r := range m.rows {
		switch v := r.Item.(type) {
		case *model.PR:
			if m.repoOf(v) == id.Repo && v.Number == id.Number {
				return v, nil
			}
		case *model.Issue:
			if m.repoOf(v) == id.Repo && v.Number == id.Number {
				return nil, v
			}
		}
	}
	for _, p := range m.allPRs {
		if m.repoOf(p) == id.Repo && p.Number == id.Number {
			return p, nil
		}
	}
	for _, i := range m.issues {
		if m.repoOf(i) == id.Repo && i.Number == id.Number {
			return nil, i
		}
	}
	return nil, nil
}

// keptFresh reports whether a kept read is as new as the list's copy:
// same head, updated no earlier. With no listed copy to compare, only a
// read younger than detailRevalidateSecs counts.
func keptFresh(e *detailEntry, listed *model.PR, listedIssue *model.Issue) bool {
	switch d := e.detail.(type) {
	case *model.PR:
		if listed == nil {
			return fx.Now()-e.started < detailRevalidateSecs
		}
		return d.HeadRefOid == listed.HeadRefOid && !d.UpdatedAt.Before(listed.UpdatedAt)
	case *model.Issue:
		if listedIssue == nil {
			return fx.Now()-e.started < detailRevalidateSecs
		}
		return !d.UpdatedAt.Before(listedIssue.UpdatedAt)
	}
	return false
}

// openItem opens an item on this frame (lock held): a kept full read,
// or the listed copy marked partial while the full read runs, or, with
// neither, a spinner until the read lands. It never blocks the keys.
func (m *Model) openItem(repo model.Repo, number int, kind string, listed *model.PR) {
	id := model.Ident{Repo: repo, Number: number}
	if kind != "issue" {
		kind = "pr"
	}
	var listedIssue *model.Issue
	if listed != nil && (m.repoOf(listed) != repo || listed.Number != number) {
		listed = nil
	}
	if p, i := m.listedCopy(id); kind == "pr" && listed == nil {
		listed = p
	} else if kind == "issue" {
		listedIssue = i
	}
	m.openSeq++
	seq := m.openSeq
	if e, ok := m.dcache.get(id); ok && keptFresh(e, listed, listedIssue) {
		if _, isPR := e.detail.(*model.PR); isPR == (kind == "pr") {
			m.showDetail(e.detail, kind, e.started, listed)
			if fx.Now()-e.started >= detailRevalidateSecs {
				// shown whole, but a comment or a review may have come
				// since: keys that act wait for the read behind it
				m.detailHeld = true
				m.readDetail(id, kind, listed, seq, false)
			}
			return
		}
	}
	switch {
	case kind == "pr" && listed != nil:
		c := *listed
		c.Ext = maps.Clone(listed.Ext) // the detail's own: its edits never touch the row
		c.Repo = repo
		m.showDetail(&c, "pr", fx.Now(), listed)
	case kind == "issue" && listedIssue != nil:
		c := *listedIssue
		c.Repo = repo
		m.showDetail(&c, "issue", fx.Now(), nil)
	default:
		// nothing listed to show first (go to a number, a manifest
		// issue): the view switches when the read lands
		m.Flash(fmt.Sprintf("opening %s...", m.where(repo, number)))
		m.readDetail(id, kind, listed, seq, true)
		return
	}
	m.detailPartial = true
	if m.prefetching && m.prefetchID == id && m.prefetchAt >= m.dcache.epoch {
		// the rest already set this read off: the open takes its answer
		// instead of asking GitHub a second time (unless a write went out
		// since it set off: then it would put the item back as it was)
		m.adoptSeq = seq
		m.detailLoadedAt = m.prefetchAt
		return
	}
	m.readDetail(id, kind, listed, seq, false)
}

// readDetail reads one item on its own goroutine (lock held); the
// answer lands as a detailLoadedMsg.
func (m *Model) readDetail(id model.Ident, kind string, listed *model.PR, seq int, pending bool) float64 {
	m.reading++
	msg := detailLoadedMsg{id: id, kind: kind, listed: listed, seq: seq, pending: pending, prefetch: seq == 0, started: fx.Now()}
	if listed != nil {
		// the worker reads its own copy: the row's may be edited under
		// it (a pending write laid over the list)
		c := *listed
		c.Ext = maps.Clone(listed.Ext)
		listed = &c
	}
	gh, wf, ctx := m.gh, m.wf, m.ctx
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg.detail, msg.err = nil, fmt.Errorf("reading %s failed inside dillodash: %v", id, r)
			}
			m.send(msg)
		}()
		if kind == "issue" {
			iss, err := gh.IssueDetail(ctx, id.Repo, id.Number)
			if err != nil {
				msg.err = err
				return
			}
			iss.Repo = id.Repo
			msg.detail = iss
			return
		}
		pr, err := fetchPRDetail(ctx, gh, wf, id.Repo, id.Number, listed)
		if err != nil {
			msg.err = err
			return
		}
		msg.detail = pr
	}()
	return msg.started
}

// applyDetailLoaded lands a detail read (lock held): it is kept either
// way, and shown only when it answers the latest open of an item that
// is still open, and no newer read of it was shown.
func (m *Model) applyDetailLoaded(msg detailLoadedMsg) {
	m.reading = max(0, m.reading-1)
	if msg.prefetch {
		m.prefetching = false
		if m.adoptSeq != 0 && m.adoptSeq == m.openSeq && msg.id == m.prefetchID {
			msg.seq, msg.prefetch = m.adoptSeq, false // the open's answer now
		}
		m.adoptSeq = 0
	}
	if msg.err == nil && msg.detail != nil {
		m.dcache.put(msg.id, msg.detail, msg.started)
	}
	if msg.prefetch || msg.seq != m.openSeq {
		return
	}
	if msg.err != nil {
		if msg.pending || m.detailPartial {
			// the partial view stays (r reads it again); the error says why
			m.fail(fmt.Sprintf("couldn't read %s: %s", m.where(msg.id.Repo, msg.id.Number), msg.err))
		}
		return
	}
	if msg.pending {
		m.showDetail(msg.detail, msg.kind, msg.started, msg.listed)
		return
	}
	if m.detail != nil && sameItem(msg.detail, m.detail) && msg.started >= m.detailLoadedAt {
		// the scroll stays where the reader took it
		m.detail = msg.detail
		m.detailLoadedAt = msg.started
		m.detailPartial = false
		m.detailHeld = false
		m.detailVer++
	}
}

// heldForRead is the notice for a key that acts while the open detail
// is the list's copy ("" when it is not).
func (m *Model) heldForRead() string {
	if !(m.detailPartial || m.detailHeld) || m.detail == nil || m.view == ViewList || m.view == ViewFortress {
		return ""
	}
	return fmt.Sprintf("still reading %s — press it again in a moment", m.detailRef())
}

// schedulePrefetch arms the rest timer when the list cursor lands on a
// new item (lock held); nil when nothing changed.
func (m *Model) schedulePrefetch() tea.Cmd {
	if m.view != ViewList || !m.loaded {
		return nil
	}
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	id, ok := m.identOf(row.Item)
	if !ok || id == m.prefetchFor {
		return nil
	}
	m.prefetchFor = id
	m.prefetchGen++
	gen := m.prefetchGen
	return tea.Tick(prefetchRest, func(time.Time) tea.Msg { return prefetchMsg{gen: gen, id: id} })
}

// prefetch reads the rested-on item ahead (lock held): one read at a
// time, never while GitHub holds us back, never for an item kept fresh.
func (m *Model) prefetch(msg prefetchMsg) {
	if msg.gen != m.prefetchGen || m.view != ViewList {
		return
	}
	if m.prefetching || m.backoff.RateLimitWait(clock.Now()) > 0 {
		m.prefetchFor = model.Ident{} // try again when the cursor next settles
		return
	}
	listed, listedIssue := m.listedCopy(msg.id)
	kind := "pr"
	switch {
	case listed != nil:
	case listedIssue != nil:
		kind = "issue"
	default:
		return
	}
	if e, ok := m.dcache.get(msg.id); ok && keptFresh(e, listed, listedIssue) && fx.Now()-e.started < detailRevalidateSecs {
		return
	}
	m.prefetching = true
	m.prefetchID, m.prefetchAt = msg.id, fx.Now()
	m.readDetail(msg.id, kind, listed, 0, false)
}
