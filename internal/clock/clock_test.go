package clock

import (
	"testing"
	"time"
)

func utc(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
}

func ny(t *testing.T, label string) *Local {
	t.Helper()
	l, err := NewLocal("America/New_York", label)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseUTC(t *testing.T) {
	got, err := ParseUTC("2026-10-21T15:00:00Z")
	if err != nil || !got.Equal(utc(2026, 10, 21, 15, 0)) || got.Location() != time.UTC {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = ParseUTC("2026-10-21T11:00:00-04:00")
	if err != nil || !got.Equal(utc(2026, 10, 21, 15, 0)) || got.Location() != time.UTC {
		t.Fatalf("offset: got %v, %v", got, err)
	}
	for _, bad := range []string{"", "2026-10-21", "yesterday", "2026-10-21 15:00:00"} {
		if _, err := ParseUTC(bad); err == nil {
			t.Errorf("ParseUTC(%q): want error", bad)
		}
	}
}

func TestParseHM(t *testing.T) {
	good := map[string]HM{"23:00": {23, 0}, "00:00": {0, 0}, "9:05": {9, 5}, " 23:28 ": {23, 28}}
	for in, want := range good {
		got, err := ParseHM(in)
		if err != nil || got != want {
			t.Errorf("ParseHM(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "24:00", "23:60", "2300", "23:0", "123:00", "-1:00", "ab:cd", "23:00:00"} {
		if _, err := ParseHM(bad); err == nil {
			t.Errorf("ParseHM(%q): want error", bad)
		}
	}
	if s := (HM{23, 0}).String(); s != "23:00" {
		t.Errorf("String = %q", s)
	}
	if s := (HM{9, 5}).String(); s != "09:05" {
		t.Errorf("String = %q", s)
	}
	if !(HM{21, 0}).Before(HM{22, 59}) || (HM{23, 0}).Before(HM{23, 0}) {
		t.Error("Before")
	}
}

func TestNewLocalUnknown(t *testing.T) {
	if _, err := NewLocal("Mars/Olympus_Mons", ""); err == nil {
		t.Fatal("want error for an unknown zone")
	}
}

// 2026-03-08 02:00 EST is the spring-forward in New York; 2026-11-01 02:00
// EDT the fall-back.
func TestWallToUTCAcrossDST(t *testing.T) {
	l := ny(t, "")
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, l.Loc) }
	cases := []struct {
		day  time.Time
		hm   HM
		want time.Time
	}{
		// the night before the change: still EST (-5)
		{day(2026, 3, 7), HM{23, 0}, utc(2026, 3, 8, 4, 0)},
		// the night after: EDT (-4)
		{day(2026, 3, 8), HM{23, 0}, utc(2026, 3, 9, 3, 0)},
		// inside the gap: 02:30 does not exist; Go applies the offset in force
		// after the transition (EDT), so it is 06:30Z (01:30 EST / 02:30 EDT)
		{day(2026, 3, 8), HM{2, 30}, utc(2026, 3, 8, 6, 30)},
		// fall-back night: before (EDT) and after (EST)
		{day(2026, 10, 31), HM{23, 0}, utc(2026, 11, 1, 3, 0)},
		{day(2026, 11, 1), HM{23, 0}, utc(2026, 11, 2, 4, 0)},
		// the cutoff, same-day
		{day(2026, 3, 8), HM{21, 0}, utc(2026, 3, 9, 1, 0)},
	}
	for _, c := range cases {
		got := l.WallToUTC(c.day, c.hm)
		if !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("WallToUTC(%s, %s) = %v; want %v", c.day.Format("2006-01-02"), c.hm, got, c.want)
		}
	}
}

func TestToday(t *testing.T) {
	l := ny(t, "")
	// 03:30Z on March 9 is still March 8 in New York (EDT, -4)
	got := l.Today(utc(2026, 3, 9, 3, 30))
	want := time.Date(2026, 3, 8, 0, 0, 0, 0, l.Loc)
	if !got.Equal(want) || got.Location() != l.Loc {
		t.Fatalf("Today = %v; want %v", got, want)
	}
	// and WallToUTC on that date composes
	if w := l.WallToUTC(got, HM{23, 0}); !w.Equal(utc(2026, 3, 9, 3, 0)) {
		t.Fatalf("compose = %v", w)
	}
}

func TestLabels(t *testing.T) {
	derived := ny(t, "")
	fixed := ny(t, "ET")
	jan := utc(2026, 1, 15, 4, 4) // 23:04 EST
	jul := utc(2026, 7, 15, 3, 4) // 23:04 EDT
	if got := derived.TAt(jan); got != "23:04 EST" {
		t.Errorf("TAt jan = %q", got)
	}
	if got := derived.TAt(jul); got != "23:04 EDT" {
		t.Errorf("TAt jul = %q", got)
	}
	if got := fixed.TAt(jan); got != "23:04 ET" {
		t.Errorf("TAt fixed = %q", got)
	}
	if got := fixed.T(HM{23, 0}); got != "23:00 ET" {
		t.Errorf("T = %q", got)
	}
	if got := fixed.Span(HM{23, 0}, HM{23, 28}); got != "23:00-23:28 ET" {
		t.Errorf("Span = %q", got)
	}
	// T and Span derive the label from Now when there is no override
	defer func(f func() time.Time) { Now = f }(Now)
	Now = func() time.Time { return jan }
	if got := derived.T(HM{23, 0}); got != "23:00 EST" {
		t.Errorf("T derived jan = %q", got)
	}
	Now = func() time.Time { return jul }
	if got := derived.Span(HM{23, 0}, HM{23, 28}); got != "23:00-23:28 EDT" {
		t.Errorf("Span derived jul = %q", got)
	}
	if got := derived.In(jul).Hour(); got != 23 {
		t.Errorf("In hour = %d", got)
	}
}

func TestInSpan(t *testing.T) {
	l := ny(t, "ET")
	a, b := HM{23, 0}, HM{23, 28}
	cases := []struct {
		now  time.Time
		want bool
	}{
		{utc(2026, 7, 16, 2, 59), false}, // 22:59 EDT
		{utc(2026, 7, 16, 3, 0), true},   // 23:00 EDT, inclusive start
		{utc(2026, 7, 16, 3, 27), true},  // 23:27
		{utc(2026, 7, 16, 3, 28), false}, // 23:28, exclusive end
		{utc(2026, 1, 16, 4, 10), true},  // 23:10 EST
		{utc(2026, 1, 16, 3, 10), false}, // 22:10 EST
	}
	for _, c := range cases {
		if got := l.InSpan(c.now, a, b); got != c.want {
			t.Errorf("InSpan(%v) = %v; want %v", c.now, got, c.want)
		}
	}
	// same-day only: a span crossing midnight is never satisfied
	if l.InSpan(utc(2026, 7, 16, 3, 30), HM{23, 0}, HM{1, 0}) {
		t.Error("midnight-crossing span should be false")
	}
}

func TestAgo(t *testing.T) {
	now := utc(2026, 10, 21, 15, 0)
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},
		{89 * time.Second, "just now"},
		{90 * time.Second, "1m ago"},
		{42 * time.Minute, "42m ago"},
		{89*time.Minute + 59*time.Second, "89m ago"},
		{90 * time.Minute, "1h ago"},
		{5 * time.Hour, "5h ago"},
		{35*time.Hour + 59*time.Minute, "35h ago"},
		{36 * time.Hour, "1d ago"},
		{3*24*time.Hour + 5*time.Hour, "3d ago"},
		{400 * 24 * time.Hour, "400d ago"},
		{-10 * time.Minute, "just now"}, // clock skew: a future time
	}
	for _, c := range cases {
		if got := Ago(now.Add(-c.d), now); got != c.want {
			t.Errorf("Ago(-%v) = %q; want %q", c.d, got, c.want)
		}
	}
}

func TestFmtTime(t *testing.T) {
	defer func(f func() time.Time) { Now = f }(Now)
	Now = func() time.Time { return utc(2026, 10, 21, 17, 3) }
	if got := FmtTime(utc(2026, 10, 21, 14, 3)); got != "2026-10-21 14:03Z · 3h ago" {
		t.Errorf("FmtTime = %q", got)
	}
	// a non-UTC instant is written in UTC with the Z
	l := ny(t, "")
	if got := FmtTime(time.Date(2026, 10, 21, 10, 3, 0, 0, l.Loc)); got != "2026-10-21 14:03Z · 3h ago" {
		t.Errorf("FmtTime local = %q", got)
	}
	if got := FmtTime(time.Time{}); got != "?" {
		t.Errorf("FmtTime zero = %q", got)
	}
}

func TestShortAge(t *testing.T) {
	now := utc(2026, 10, 21, 15, 0)
	cases := []struct {
		d     time.Duration
		label string
		style string
	}{
		{0, "1m", "age.0"},
		{30 * time.Second, "1m", "age.0"},
		{3 * time.Minute, "3m", "age.0"},
		{59*time.Minute + 59*time.Second, "59m", "age.0"},
		{time.Hour, "1h", "age.1"},
		{3 * time.Hour, "3h", "age.1"},
		{23*time.Hour + 59*time.Minute, "23h", "age.1"},
		{24 * time.Hour, "1d", "age.2"},
		{6*24*time.Hour + 23*time.Hour, "6d", "age.2"},
		{7 * 24 * time.Hour, "7d", "age.3"},
		{59 * 24 * time.Hour, "59d", "age.3"},
		{60 * 24 * time.Hour, "8w", "age.3"},
		{100 * 24 * time.Hour, "14w", "age.3"},
		{364 * 24 * time.Hour, "52w", "age.3"}, // never "100w": the column is three cells
		{400 * 24 * time.Hour, "1y", "age.3"},
		{3 * 365 * 24 * time.Hour, "3y", "age.3"},
		{-time.Hour, "1m", "age.0"}, // future: clamped to 0 seconds
	}
	for _, c := range cases {
		label, style := ShortAge(now.Add(-c.d), now)
		if label != c.label || style != c.style {
			t.Errorf("ShortAge(-%v) = %q,%q; want %q,%q", c.d, label, style, c.label, c.style)
		}
	}
	if label, style := ShortAge(time.Time{}, now); label != "" || style != "age.3" {
		t.Errorf("ShortAge(zero) = %q,%q", label, style)
	}
}
