package classify

import (
	"fmt"
	"strings"
)

// Reason is a stable identifier for why a candidate was surfaced. It is not
// itself display text — see Describe/ReasonInfo for the short/long text and
// confidence attached to each Reason.
type Reason string

// The reasons BuildLegend can attach to a candidate. Adding a new detection
// type means adding a new const here plus a matching entry in reasonInfo —
// see Describe's doc comment.
const (
	ReasonMerged             Reason = "merged"
	ReasonProbablyMerged     Reason = "probably-merged"
	ReasonStale              Reason = "stale"
	ReasonNoDescription      Reason = "no-description"
	ReasonHasDescription     Reason = "has-description"
	ReasonGitCommitDuplicate Reason = "git-commit-duplicate"
)

// Confidence is how sure jj-trim is that a candidate carrying a given Reason
// is safe to delete.
type Confidence int

const (
	// ConfidenceSure means the candidate is provably safe to delete (e.g.
	// a literal ancestor of trunk, or an exact duplicate of kept history).
	ConfidenceSure Confidence = 1
	// ConfidenceReview means there's a decent signal but not proof — worth
	// a human look before deleting.
	ConfidenceReview Confidence = 2
	// ConfidenceGuess means the signal is weak (age, presence of a
	// description) — treat the classification as a guess.
	ConfidenceGuess Confidence = 3
)

// Letter renders a Confidence as the single-letter code shown in the legend
// line and detail-pane header: H(igh)/M(edium)/L(ow) confidence that the
// candidate is safe to delete. Preferred over the raw int for that spot —
// see LegendEntry.String() and reasonHeader — since H/M/L reads at a glance
// without a lookup, unlike a bare 1/2/3.
func (c Confidence) Letter() string {
	switch c {
	case ConfidenceSure:
		return "H"
	case ConfidenceReview:
		return "M"
	case ConfidenceGuess:
		return "L"
	default:
		return "?"
	}
}

// String renders a Confidence as the word shown in --explain output.
func (c Confidence) String() string {
	switch c {
	case ConfidenceSure:
		return "sure"
	case ConfidenceReview:
		return "review"
	case ConfidenceGuess:
		return "guess"
	default:
		return "unknown"
	}
}

// ReasonInfo is a Reason's registered metadata: how confident jj-trim is
// that a candidate with this Reason is safe to delete, a short description
// (the legend-line text, must be distinct per Reason), and a long
// description (the --explain/review-detail-pane text).
type ReasonInfo struct {
	Confidence Confidence
	Short      string
	Long       string
}

// reasonInfo is the single source of truth for every Reason's metadata.
// Every Reason constant above must have an entry here — see Describe.
//
//nolint:gochecknoglobals // effectively constant, a lookup table
var reasonInfo = map[Reason]ReasonInfo{
	ReasonMerged: {
		Confidence: ConfidenceSure,
		Short:      "merged into trunk",
		Long: "This commit is a literal ancestor of trunk() — jj-trim found it " +
			"by walking trunk's own history, so deleting the bookmark cannot lose " +
			"any work that isn't already on trunk.",
	},
	ReasonGitCommitDuplicate: {
		Confidence: ConfidenceSure,
		Short:      "git-bypass duplicate",
		Long: "This fork's parent(s) and full diff exactly match a commit that's " +
			"definitely being kept (on trunk or an ancestor of the working copy) — " +
			"almost always the result of running `git commit` directly in a " +
			"colocated repo, which leaves the old jj commit behind as an orphan.",
	},
	ReasonProbablyMerged: {
		Confidence: ConfidenceReview,
		Short:      "probably merged (message match)",
		Long: "This bookmark's description text appears verbatim in trunk's " +
			"commit-message history — the signature of a squash/rebase merge — " +
			"but jj-trim didn't find the commit itself as a trunk ancestor, so " +
			"it's a pattern match, not a proof.",
	},
	ReasonNoDescription: {
		Confidence: ConfidenceReview,
		Short:      "anonymous fork, no description",
		Long: "An unbookmarked head with no commit message — usually an " +
			"abandoned false start, but jj-trim can't read intent from an empty " +
			"description, so it's flagged for review rather than auto-applied.",
	},
	ReasonStale: {
		Confidence: ConfidenceGuess,
		Short:      "stale, no remote",
		Long: "This bookmark is older than the configured threshold and isn't " +
			"tracking any remote — age and lack of a remote are the only signals " +
			"here, so this is jj-trim's weakest, most heuristic bucket.",
	},
	ReasonHasDescription: {
		Confidence: ConfidenceGuess,
		Short:      "anonymous fork, has description",
		Long: "An unbookmarked head that does have a commit message — it may be " +
			"meaningful in-progress work, so jj-trim can't tell safe-to-delete " +
			"from valuable without a human reading the description/diff.",
	},
}

// Describe returns r's registered metadata. Panics on an unregistered
// Reason — every Reason constant must have a reasonInfo entry, which
// TestReasonRegistryComplete enforces.
func Describe(r Reason) ReasonInfo {
	info, ok := reasonInfo[r]
	if !ok {
		panic("classify: no ReasonInfo registered for Reason " + string(r))
	}

	return info
}

// LegendEntry pairs a candidate's shortest change-id prefix (matching what
// jj log itself would render) with the reason it was classified. Bookmarks
// holds any local bookmark names pointing at the candidate — for the
// `bookmarks` command, that's the actual thing about to be deleted, so the
// legend leads with it rather than the change id alone. DiffStat is an
// optional trailing detail string, appended after an em dash by String():
// the `commits` command sets it to age plus a diffstat summary (see
// Candidate.Age/DiffStatSummary) — a diffstat is the main signal for "is
// this abandoned fork safe to abandon"; `bookmarks` sets it to age alone
// (see run.go's bookmarkLegend/ageAndDiffStat), since a merged bookmark's
// diff isn't itself actionable information the way a fork's is.
type LegendEntry struct {
	ChangeIDShort string
	Bookmarks     []string
	Reason        Reason
	DiffStat      string
}

// BuildLegend maps each candidate to a LegendEntry, in the order given.
// Callers pass one call per classification bucket (merged bookmarks,
// no-description forks, described forks); order determines legend order.
func BuildLegend(candidates []Candidate, reason Reason) []LegendEntry {
	entries := make([]LegendEntry, 0, len(candidates))
	for _, c := range candidates {
		entries = append(entries, LegendEntry{
			ChangeIDShort: c.ShortChangeID(),
			Bookmarks:     c.LocalBookmarks,
			Reason:        reason,
		})
	}

	return entries
}

// String renders a legend entry as a single line. With bookmarks:
// "tags (tun)  [H] merged into trunk". Without (anonymous forks have none):
// "w  [M] anonymous fork, no description — 3 files changed, +45/-2" (DiffStat
// appended when set). The bracketed letter is the Reason's Confidence (see
// Describe) that the candidate is safe to delete — H(igh)/M(edium)/L(ow).
func (e LegendEntry) String() string {
	id := e.ChangeIDShort
	if len(e.Bookmarks) > 0 {
		id = fmt.Sprintf("%s (%s)", strings.Join(e.Bookmarks, ", "), e.ChangeIDShort)
	}

	info := Describe(e.Reason)

	line := fmt.Sprintf("%s  [%s] %s", id, info.Confidence.Letter(), info.Short)
	if e.DiffStat != "" {
		line += " — " + e.DiffStat
	}

	return line
}
