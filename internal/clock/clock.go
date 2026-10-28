// Package clock is the dashboard's one source of time: Now (the single
// test patch point), ISO/UTC parsing, "HH:MM" wall-clock values, a Local
// zone that turns wall clocks into instants DST-correctly and labels them
// ("23:00 ET"), and the compact age strings the list draws.
//
// The tz database is embedded (time/tzdata) so America/New_York resolves
// on hosts without /usr/share/zoneinfo.
package clock

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed the IANA database for tzdata-less hosts
)

// Now returns the current instant in UTC. Tests replace it.
var Now = func() time.Time { return time.Now().UTC() }

// ParseUTC parses a GitHub-style RFC 3339 timestamp ("2026-10-21T15:00:00Z",
// offsets allowed) and returns the instant in UTC.
func ParseUTC(iso string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(iso))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %q: not an RFC 3339 timestamp", iso)
	}
	return t.UTC(), nil
}

// HM is a wall-clock time of day, "HH:MM".
type HM struct{ H, M int }

// ParseHM parses "HH:MM" (a one-digit hour is accepted): 00:00 - 23:59.
func ParseHM(s string) (HM, error) {
	s = strings.TrimSpace(s)
	bad := func() (HM, error) { return HM{}, fmt.Errorf("parse %q: want HH:MM (00:00-23:59)", s) }
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) < 1 || len(h) > 2 || len(m) != 2 {
		return bad()
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return bad()
	}
	return HM{hh, mm}, nil
}

// String is "HH:MM".
func (h HM) String() string { return fmt.Sprintf("%02d:%02d", h.H, h.M) }

// Minutes is the wall clock as minutes since midnight, for ordering.
func (h HM) Minutes() int { return h.H*60 + h.M }

// Before reports whether h is earlier in the day than o.
func (h HM) Before(o HM) bool { return h.Minutes() < o.Minutes() }

// Local is a time zone plus how it is written: Label overrides the zone
// abbreviation ("ET" instead of EST/EDT). With Label empty the
// abbreviation of the instant being formatted is used, so a January time
// reads "EST" and a July time "EDT".
type Local struct {
	Loc   *time.Location
	Label string
}

// NewLocal loads an IANA zone ("America/New_York"); label may be "".
func NewLocal(tz, label string) (*Local, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(tz))
	if err != nil {
		return nil, fmt.Errorf("timezone %q: unknown IANA zone", tz)
	}
	return &Local{Loc: loc, Label: label}, nil
}

// In is t in the local zone.
func (l *Local) In(t time.Time) time.Time { return t.In(l.Loc) }

// LabelAt is the zone label for an instant: Label when set, else the
// zone abbreviation in force at t (EST or EDT).
func (l *Local) LabelAt(t time.Time) string {
	if l.Label != "" {
		return l.Label
	}
	return t.In(l.Loc).Format("MST")
}

// WallToUTC is the instant at wall clock hm on day's calendar date, in
// UTC. The date is read from day as given (its own location), so pass a
// date built in Loc, e.g. Today's result. DST is handled by the zone: 23:00
// on the Saturday before a spring-forward is five hours behind UTC in New
// York, 23:00 on the Sunday after is four.
func (l *Local) WallToUTC(day time.Time, hm HM) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, hm.H, hm.M, 0, 0, l.Loc).UTC()
}

// Today is now's calendar date in the local zone, as local midnight.
func (l *Local) Today(now time.Time) time.Time {
	y, m, d := now.In(l.Loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, l.Loc)
}

// T writes a wall clock with the zone label: "23:00 ET". Without a Label
// override the abbreviation in force at Now() is used.
func (l *Local) T(hm HM) string { return hm.String() + " " + l.LabelAt(Now()) }

// TAt writes an instant's local wall clock with its label: "23:04 ET".
func (l *Local) TAt(t time.Time) string {
	lt := t.In(l.Loc)
	return fmt.Sprintf("%02d:%02d %s", lt.Hour(), lt.Minute(), l.LabelAt(t))
}

// Span writes a same-day window: "23:00-23:28 ET".
func (l *Local) Span(a, b HM) string {
	return a.String() + "-" + b.String() + " " + l.LabelAt(Now())
}

// InSpan reports whether now's local wall clock falls in [a, b), compared
// as times of day on the same calendar day. A span that crosses midnight
// (b before a) is not supported and is always false.
func (l *Local) InSpan(now time.Time, a, b HM) bool {
	lt := now.In(l.Loc)
	cur := HM{lt.Hour(), lt.Minute()}.Minutes()
	return a.Minutes() <= cur && cur < b.Minutes()
}

// Ago is the compact age of an instant: "just now", "42m ago", "5h ago",
// "3d ago". (A port of pr-dash's ago.)
func Ago(t, now time.Time) string {
	secs := now.Sub(t).Seconds()
	switch {
	case secs < 90:
		return "just now"
	case secs < 90*60:
		return fmt.Sprintf("%dm ago", int(math.Floor(secs/60)))
	case secs < 36*3600:
		return fmt.Sprintf("%dh ago", int(math.Floor(secs/3600)))
	default:
		return fmt.Sprintf("%dd ago", int(math.Floor(secs/86400)))
	}
}

// FmtTime writes a GitHub UTC timestamp as "2026-10-21 14:03Z · 3h ago"
// (age relative to Now). The Z stays: stripped, it reads as local time
// next to a header clock that IS local. A zero time is "?".
func FmtTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04Z") + " · " + Ago(t, Now())
}

// ShortAge is ("3h", style) for an instant, shaded by freshness the way
// lsd shades dates: the last hour age.0 (bright), the last day age.1
// (blue), the last week age.2 (gray), older age.3 (dim). Minutes under an
// hour, hours under a day, days under sixty, then weeks. A zero time is
// ("", "age.3").
func ShortAge(t time.Time, now time.Time) (label, style string) {
	if t.IsZero() {
		return "", "age.3"
	}
	secs := math.Max(0, now.Sub(t).Seconds())
	steps := []struct {
		limit float64
		unit  string
		div   float64
		style string
	}{
		{3600, "m", 60, "age.0"},
		{86400, "h", 3600, "age.1"},
		{7 * 86400, "d", 86400, "age.2"},
		{60 * 86400, "d", 86400, "age.3"},
		{math.Inf(1), "w", 7 * 86400, "age.3"},
	}
	for _, s := range steps {
		if secs < s.limit {
			n := int(math.Floor(secs / s.div))
			if n < 1 {
				n = 1
			}
			return fmt.Sprintf("%d%s", n, s.unit), s.style
		}
	}
	return "", "age.3"
}
