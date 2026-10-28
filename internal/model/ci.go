package model

import (
	"strconv"
	"strings"
)

// CI glyphs: + pass, x fail, . queued, ~ running, - skipped, ? unknown.
// CheckRuns (GraphQL) and workflow runs (REST) share the same status/
// conclusion vocabulary, so one mapper serves both.
const (
	CIPass    = '+'
	CIFail    = 'x'
	CIQueued  = '.'
	CIRunning = '~'
	CISkipped = '-'
	CIUnknown = '?'
)

// ciOrder is the glyph order of a compressed cluster ("9+1x2.").
const ciOrder = "+x.~-"

// CITone is how a cluster wants to be drawn: a failing cluster is plain
// (the row-level highlight carries the alarm), an all-green one bold,
// anything in flight dim.
type CITone int

const (
	CIToneNormal CITone = iota
	CIToneBold
	CIToneDim
)

// CICluster summarizes one commit's checks: a glyph per check (or counts
// per glyph once there are more than twelve), a tone, and the names of
// the failed checks.
type CICluster struct {
	Glyphs string
	Tone   CITone
	Failed []string
}

// CIUnknownCluster is the "?" cluster drawn when CI is invisible by
// every route.
func CIUnknownCluster() CICluster {
	return CICluster{Glyphs: string(CIUnknown), Tone: CIToneDim}
}

// CIGlyph maps a check's status/conclusion pair to its glyph.
func CIGlyph(status, conclusion string) byte {
	s := strings.ToUpper(status)
	if s != "" && s != "COMPLETED" {
		if s == "IN_PROGRESS" {
			return CIRunning
		}
		return CIQueued
	}
	switch strings.ToUpper(conclusion) {
	case "SUCCESS":
		return CIPass
	case "NEUTRAL", "SKIPPED":
		return CISkipped
	}
	return CIFail
}

// FinishCluster builds the cluster for a list of per-check glyphs.
func FinishCluster(chars string, failed []string) CICluster {
	if chars == "" {
		return CICluster{Failed: []string{}}
	}
	tone := CIToneDim
	switch {
	case strings.ContainsRune(chars, CIFail):
		tone = CIToneNormal
	case strings.Trim(chars, string(CIPass)) == "":
		tone = CIToneBold
	}
	glyphs := chars
	if len(glyphs) > 12 { // many checks: compress to counts, e.g. 9+1x2.
		var b strings.Builder
		for _, g := range ciOrder {
			if n := strings.Count(chars, string(g)); n > 0 {
				b.WriteString(strconv.Itoa(n))
				b.WriteRune(g)
			}
		}
		glyphs = b.String()
		if len(glyphs) > 12 {
			glyphs = glyphs[:12]
		}
	}
	if failed == nil {
		failed = []string{}
	}
	return CICluster{Glyphs: glyphs, Tone: tone, Failed: failed}
}

// ClusterCI summarizes the head commit's CI checks from the GraphQL
// rollup. ok is false when the token cannot see the data (fine-grained
// PATs can never read check runs) or there is no rollup at all; the
// caller may then fall back to the Actions REST API.
func ClusterCI(pr *PR) (c CICluster, ok bool) {
	head := pr.HeadCommit()
	if head == nil || head.Rollup == nil {
		// a null commit node (the token isn't allowed to see it) and a
		// missing rollup (maybe "no CI") both look alike here; the
		// Actions fallback settles it
		return CICluster{}, false
	}
	var chars []byte
	failed := []string{}
	for _, cx := range head.Rollup.Contexts.Nodes {
		if cx.Typename == "" {
			// the rollup exists but this entry is hidden from the token
			// (fine-grained PATs get CheckRun nodes nulled out, or empty
			// objects with no fields) — a partial cluster would lie
			return CICluster{}, false
		}
		var g byte
		var name string
		if cx.Typename == "CheckRun" {
			g = CIGlyph(cx.Status, cx.Conclusion)
			name = cx.Name
		} else { // StatusContext
			switch cx.State {
			case "SUCCESS":
				g = CIPass
			case "PENDING", "EXPECTED":
				g = CIQueued
			default:
				g = CIFail
			}
			name = cx.Context
		}
		chars = append(chars, g)
		if g == CIFail {
			if name == "" {
				name = "?"
			}
			failed = append(failed, name)
		}
	}
	return FinishCluster(string(chars), failed), true
}

// The generic review-decision chips. Workflows may add their own (an
// apply gate, a requeue) through a hook in the UI; these are the ones
// every PR can wear.
const (
	LabelDraft      = "DRAFT"
	LabelApprovedMe = "approved by me"
	LabelApproved   = "approved"
	LabelChanges    = "changes requested"
	LabelAwaiting   = "awaiting review"
	LabelMerged     = "merged"
)

// ChipForLabel maps a decision label to the style of its chip.
var ChipForLabel = map[string]string{
	LabelAwaiting:   "chip.await",
	LabelApprovedMe: "chip.mine",
	LabelApproved:   "chip.approved",
	LabelMerged:     "chip.approved",
	LabelChanges:    "chip.changes",
	LabelDraft:      "chip.draft",
}

// DecisionLabel is the PR's chip: DRAFT, approved by me (a review of
// mine, or one this session just posted), approved, changes requested,
// else awaiting review.
func DecisionLabel(pr *PR, me string, localApproved bool) string {
	if pr.IsDraft {
		return LabelDraft
	}
	if localApproved {
		return LabelApprovedMe
	}
	if me != "" && pr.MyReviewState(me) == "APPROVED" {
		return LabelApprovedMe
	}
	switch pr.ReviewDecision {
	case "APPROVED":
		return LabelApproved
	case "CHANGES_REQUESTED":
		return LabelChanges
	}
	return LabelAwaiting
}
