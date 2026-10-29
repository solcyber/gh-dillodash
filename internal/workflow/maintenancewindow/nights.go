package maintenancewindow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
	"github.com/solcyber/gh-dillodash/internal/model"
)

// The window commands the w key posts on the night's manifest issue,
// exactly as a person would; the conductor still judges every one and
// replies on the issue. The rules in CommandCheck mirror it, so a
// command that would bounce says why instead of posting:
//
//	attend   from a team member before the cutoff; the freeze turns those
//	         into the night's attendees
//	release  from an attendee, counted from release_from; each NOT
//	         revertible item waits for it until its release_by_utc (on an
//	         older manifest, its slot)
//	cancel   until the item starts (or while it waits for a release)
const (
	CmdAttend  = "attend"
	CmdRelease = "release"
	CmdCancel  = "cancel"
)

// DonePhases are the manifest phases after which nothing can be posted.
var DonePhases = map[string]bool{"ran": true, "skipped": true, "closed": true}

// Reply states a conductor reply puts on a command.
const (
	StatePosted      = "posted" // no reply yet
	StateAttending   = "attending"
	StateLate        = "late"
	StateNotMember   = "not a team member"
	StateNotAttendee = "not an attendee"
	StateCheckedIn   = "checked in"
	StateEarly       = "early"
)

// reply is one of the conductor's reply shapes: the login it names, the
// command it answers ("" = the one the person posted last) and the state.
type reply struct {
	re    *regexp.Regexp
	cmd   string
	state string
}

// replies are the conductor's replies, built from the configured
// command names.
func replies(cfg *config.MaintenanceWindowConfig) []reply {
	attend, release := regexp.QuoteMeta(cfg.AttendCommand), regexp.QuoteMeta(cfg.ReleaseCommand)
	return []reply{
		{regexp.MustCompile(`^@([\w.-]+) is attending the night of`), CmdAttend, StateAttending},
		{regexp.MustCompile("^`" + attend + "` from @([\\w.-]+) is after the cutoff"), CmdAttend, StateLate},
		{regexp.MustCompile(`^@([\w.-]+): only members of @\S+ can run window commands`), "", StateNotMember},
		{regexp.MustCompile("^@([\\w.-]+): `" + release + "` counts only from someone who posted"), CmdRelease, StateNotAttendee},
		{regexp.MustCompile(`^Checked in: @([\w.-]+)\.`), CmdRelease, StateCheckedIn},
	}
}

// releaseEarlyRE is the conductor's "`/release` counts from ..." reply,
// which names nobody.
func releaseEarlyRE(cfg *config.MaintenanceWindowConfig) *regexp.Regexp {
	return regexp.MustCompile("^`" + regexp.QuoteMeta(cfg.ReleaseCommand) + "` counts from ")
}

// CheckIn is one "Checked in" reply.
type CheckIn struct {
	Login string
	At    time.Time
}

// Mine is what the viewer's own commands came to: "" (not posted),
// posted, or the reply's state.
type Mine struct {
	Attend  string
	Release string
}

// Night is a night's attendance, from its manifest issue and comments.
type Night struct {
	Issue     int    // the manifest issue number
	Phase     string // open / frozen / running / ran / skipped / closed
	Items     []Item // the manifest's items (none until the freeze)
	Attendees []string
	CheckedIn []CheckIn
	Mine      Mine
}

// Live reports the night is not over.
func (n *Night) Live() bool { return n != nil && !DonePhases[n.Phase] }

// WindowNights is {night: attendance} from the open manifest issues and
// their comments, for the viewer me. Attendees are the frozen list once
// the freeze has run; before it, the logins the conductor confirmed. Two
// open issues for one night: the lowest number wins.
func WindowNights(issues []*model.Issue, me string, cfg *config.MaintenanceWindowConfig) map[string]*Night {
	me = strings.ToLower(me)
	reps := replies(cfg)
	earlyRE := releaseEarlyRE(cfg)
	out := map[string]*Night{}
	sorted := append([]*model.Issue(nil), issues...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Number > sorted[b].Number })
	for _, i := range sorted {
		man, _ := parseManifest(i, cfg)
		if man == nil {
			man = &Manifest{}
		}
		night := man.Night
		if night == "" {
			night, _ = cfg.MatchNight(i.Title)
		}
		if night == "" {
			continue
		}
		var confirmed []string
		var checked []CheckIn
		var mine Mine
		myLast, lastReleaseBy := "", ""
		nodes := append([]model.Comment(nil), i.Comments.Nodes...)
		sort.SliceStable(nodes, func(a, b int) bool { return nodes[a].CreatedAt.Before(nodes[b].CreatedAt) })
		for _, c := range nodes {
			who := strings.ToLower(c.Author.Login)
			body := strings.TrimSpace(c.Body)
			if !isBot(who, cfg.ConductorBot) {
				for _, cmd := range []struct{ name, text string }{{CmdAttend, cfg.AttendCommand}, {CmdRelease, cfg.ReleaseCommand}} {
					if strings.HasPrefix(body, cmd.text) {
						if cmd.name == CmdRelease {
							lastReleaseBy = who
						}
						if who == me {
							mine.set(cmd.name, StatePosted)
							myLast = cmd.name
						}
					}
				}
				continue
			}
			if earlyRE.MatchString(body) {
				if me != "" && lastReleaseBy == me {
					mine.Release = StateEarly
				}
				continue
			}
			for _, r := range reps {
				mm := r.re.FindStringSubmatch(body)
				if mm == nil {
					continue
				}
				login := strings.ToLower(mm[1])
				switch r.state {
				case StateAttending:
					confirmed = append(confirmed, login)
				case StateCheckedIn:
					checked = append(checked, CheckIn{login, c.CreatedAt})
				}
				cmd := r.cmd
				if cmd == "" {
					cmd = myLast
				}
				if login == me && cmd != "" {
					mine.set(cmd, r.state)
				}
				break
			}
		}
		phase := man.Phase
		if phase == "" {
			phase = "open"
		}
		var attendees []string
		if phase != "open" && man.Attendees != nil {
			attendees = man.Attendees
		} else {
			attendees = confirmed
		}
		out[night] = &Night{Issue: i.Number, Phase: phase, Items: man.Items,
			Attendees: uniqLower(attendees), CheckedIn: checked, Mine: mine}
	}
	return out
}

func (m *Mine) set(cmd, state string) {
	if cmd == CmdAttend {
		m.Attend = state
	} else {
		m.Release = state
	}
}

// Get is the state of a command.
func (m Mine) Get(cmd string) string {
	if cmd == CmdAttend {
		return m.Attend
	}
	return m.Release
}

func uniqLower(ss []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ss {
		s = strings.ToLower(s)
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func (n *Night) attending(me string) bool {
	for _, a := range n.Attendees {
		if a == me {
			return true
		}
	}
	return false
}

func (n *Night) checkedIn(me string) bool {
	for _, c := range n.CheckedIn {
		if c.Login == me {
			return true
		}
	}
	return false
}

// ReleaseSpan is (from, until): a release counts from release_from on
// the night, and until is when the first scheduled NOT revertible item
// stops waiting for one and rolls (its release_by_utc, or its slot on an
// older manifest). until is zero while no such item is scheduled.
func ReleaseSpan(info *Night, night string, cfg *config.MaintenanceWindowConfig) (from, until time.Time) {
	from, _ = WallToUTC(night, cfg.ReleaseFrom, cfg)
	if info == nil {
		return from, until
	}
	for _, it := range info.Items {
		if it.Flag != "scheduled" || it.Revertible == nil || *it.Revertible {
			continue
		}
		iso := it.ReleaseByUTC
		if iso == "" {
			iso = it.StartUTC
		}
		if iso == "" {
			continue
		}
		t, err := clock.ParseUTC(iso)
		if err != nil {
			continue
		}
		if until.IsZero() || t.Before(until) {
			until = t
		}
	}
	return from, until
}

// span writes a duration as "2h03m".
func span(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
}

// CommandCheck is (ok, why) for posting cmd (attend, release or cancel)
// on night's manifest at now: the conductor's rules, so the menu can say
// why a command would bounce. item is the manifest item of the PR a
// cancel names, when the freeze has written one.
func CommandCheck(cmd string, info *Night, night, me string, now time.Time, item *Item, cfg *config.MaintenanceWindowConfig) (bool, string) {
	if info == nil {
		return false, fmt.Sprintf("no manifest issue for the night of %s yet", night)
	}
	local := cfg.Local()
	me = strings.ToLower(me)
	phase := info.Phase
	if phase == "" {
		phase = "open"
	}
	if DonePhases[phase] {
		return false, fmt.Sprintf("the night of %s is over (%s)", night, phase)
	}
	switch cmd {
	case CmdAttend:
		cutoff, err := WallToUTC(night, cfg.Cutoff, cfg)
		if err != nil {
			return false, err.Error()
		}
		mine := info.Mine.Attend
		if info.attending(me) || mine == StateAttending {
			return false, fmt.Sprintf("you're already attending the night of %s", night)
		}
		if mine == StatePosted {
			return false, fmt.Sprintf("your %s is posted; the conductor replies within a minute (r refreshes)", cfg.AttendCommand)
		}
		if !now.Before(cutoff) || phase != "open" {
			return false, fmt.Sprintf("past the %s cutoff: an %s no longer counts for the night of %s", local.T(cfg.Cutoff), cfg.AttendCommand, night)
		}
		return true, fmt.Sprintf("you'll be there at %s (counts until %s, %s from now)", local.T(cfg.Start), local.T(cfg.Cutoff), span(cutoff.Sub(now)))
	case CmdRelease:
		start, until := ReleaseSpan(info, night, cfg)
		end, err := WallToUTC(night, cfg.End, cfg)
		if err != nil {
			return false, err.Error()
		}
		if info.checkedIn(me) {
			return false, fmt.Sprintf("you already checked in for the night of %s", night)
		}
		if !info.attending(me) {
			who := mentions(info.Attendees)
			if who == "" {
				who = "nobody"
			}
			return false, fmt.Sprintf("only someone who posted %s before %s can release (attending: %s)", cfg.AttendCommand, local.T(cfg.Cutoff), who)
		}
		if now.Before(start) {
			return false, fmt.Sprintf("too early: a %s counts from %s (%s from now)", cfg.ReleaseCommand, local.T(cfg.ReleaseFrom), span(start.Sub(now)))
		}
		if !now.Before(end) {
			return false, fmt.Sprintf("the window closed at %s", local.T(cfg.End))
		}
		if !until.IsZero() && !now.Before(until) {
			return true, fmt.Sprintf("checks you in; the first not-revertible item stopped waiting at %s", local.TAt(until))
		}
		why := "checks you in now"
		if !until.IsZero() {
			why += fmt.Sprintf("; not-revertible items wait for it until %s", local.TAt(until))
		}
		return true, why
	case CmdCancel:
		if item != nil && item.StartUTC != "" {
			started := false
			if t, err := clock.ParseUTC(item.StartUTC); err == nil {
				started = !now.Before(t)
			}
			waiting := strings.HasPrefix(item.Status, "waiting for "+cfg.ReleaseCommand)
			if started && !waiting {
				return false, fmt.Sprintf("#%d has already started", item.PR)
			}
		}
		return true, "takes it out of this window; it needs a fresh approve to queue again"
	}
	return false, fmt.Sprintf("unknown window command %q", cmd)
}

func mentions(logins []string) string {
	out := make([]string, 0, len(logins))
	for _, l := range logins {
		out = append(out, "@"+l)
	}
	return strings.Join(out, ", ")
}

// AttendanceText is one line for the w menu's title: the phase, who
// attends and who checked in.
func AttendanceText(info *Night, cfg *config.MaintenanceWindowConfig) string {
	local := cfg.Local()
	phase := info.Phase
	if phase == "" {
		phase = "open"
	}
	att := mentions(info.Attendees)
	if att == "" {
		att = "nobody"
	}
	out := fmt.Sprintf("%s; attending: %s", phase, att)
	if len(info.CheckedIn) > 0 {
		var parts []string
		for _, c := range info.CheckedIn {
			s := "@" + c.Login
			if !c.At.IsZero() {
				s += " " + local.TAt(c.At)
			}
			parts = append(parts, s)
		}
		out += "; checked in: " + strings.Join(parts, ", ")
	}
	return out
}

// InReleaseWindow reports whether now is inside the release span
// [release_from, end) of the window's day: when a release workflow may
// merge at its slot.
func InReleaseWindow(now time.Time, cfg *config.MaintenanceWindowConfig) bool {
	return cfg.Local().InSpan(now, cfg.ReleaseFrom, cfg.End)
}

// Countdown is "starts in 2h03m" for a night whose start is ahead, else "".
func Countdown(night string, now time.Time, cfg *config.MaintenanceWindowConfig) string {
	start, err := WallToUTC(night, cfg.Start, cfg)
	if err != nil || !start.After(now) {
		return ""
	}
	return "starts in " + span(start.Sub(now))
}
