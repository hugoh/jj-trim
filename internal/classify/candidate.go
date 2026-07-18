package classify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// templateFields is the shared prefix of every -T template below, built
// around jj's own json() template function rather than a hand-rolled
// delimiter scheme (jj already solved the escaping problem for arbitrary
// commit-message content).
const templateFields = `"{\"change_id\":" ++ json(self.change_id()) ++ ` +
	`",\"change_id_short\":" ++ json(self.change_id().shortest()) ++ ` +
	`",\"description\":" ++ json(self.description()) ++ ` +
	`",\"local_bookmarks\":" ++ json(self.local_bookmarks().map(|b| b.name())) ++ ` +
	`",\"commit_timestamp\":" ++ json(self.committer().timestamp()) ++ ` +
	`",\"files_changed\":" ++ json(self.diff().files().len()) ++ ` +
	`",\"lines_added\":" ++ json(self.diff().stat().total_added()) ++ ` +
	`",\"lines_removed\":" ++ json(self.diff().stat().total_removed()) ++ ` +
	`",\"has_tracked_remote\":" ++ json(self.local_bookmarks().filter(|b| b.tracked()).len() > 0)`

// Template is the -T template passed to `jj log`/`jj show` to get one JSON
// object per line — used everywhere except the two GitCommitDuplicates
// queries in run.go's classifyForks, which need TemplateWithDuplicateKey
// instead (see that constant's doc comment for why the extra fields aren't
// included here).
const Template = templateFields + ` ++ "}\n"`

// TemplateWithDuplicateKey adds ParentChangeIDs/DiffHash on top of
// Template — the fields GitCommitDuplicates keys on. Kept as a separate
// template rather than folded into Template: hash(self.diff().git())
// serializes and hashes the commit's full diff, real work that every
// other Template query (bookmarks classification, most of commits') has
// no use for, so it's requested only where it's actually read (run.go's
// classifyForks).
const TemplateWithDuplicateKey = templateFields +
	` ++ ",\"parent_change_ids\":" ++ json(self.parents().map(|p| p.change_id())) ++ ` +
	`",\"diff_hash\":" ++ json(hash(self.diff().git())) ++ "}\n"`

// shortestIDPrefix mirrors jj's ShortestIdPrefix JSON shape ({"prefix":
// ..., "rest": ...}) — the same split jj log itself highlights the
// prefix/rest of when rendering a change id.
type shortestIDPrefix struct {
	Prefix string `json:"prefix"`
	Rest   string `json:"rest"`
}

// Candidate is a single commit or bookmark surfaced by one of the
// classification revsets, parsed from jj's own JSON template output.
type Candidate struct {
	ChangeID        string           `json:"change_id"`
	ChangeIDShort   shortestIDPrefix `json:"change_id_short"`
	Description     string           `json:"description"`
	LocalBookmarks  []string         `json:"local_bookmarks"`
	CommitTimestamp time.Time        `json:"commit_timestamp"`
	// FilesChanged/LinesAdded/LinesRemoved describe the commit's own diff
	// against its parent(s) — not a cumulative diff against trunk, since
	// a stacked anonymous fork only ever surfaces its head commit as a
	// candidate (see AnonymousForks), so per-commit is the diff that
	// actually corresponds to what's being decided on.
	FilesChanged int `json:"files_changed"`
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
	// HasTrackedRemote is true if any local bookmark on this commit tracks
	// a remote counterpart — used by Stale to distinguish forgotten local
	// scratch work from a bookmark still tracking an open PR.
	HasTrackedRemote bool `json:"has_tracked_remote"`
	// ParentChangeIDs and DiffHash together identify a commit's content
	// relative to its parent(s) — two commits with the same parents and the
	// same DiffHash have byte-identical diffs. Used by GitCommitDuplicates
	// to find an anonymous fork whose content is already preserved verbatim
	// in a kept sibling (the classic artifact of running `git commit`
	// directly in a colocated repo instead of `jj commit`/`jj describe`).
	ParentChangeIDs []string `json:"parent_change_ids"`
	DiffHash        string   `json:"diff_hash"`
}

// ShortChangeID returns the shortest-unique change id prefix jj would
// highlight in `jj log`'s own rendering, so the legend built from this
// data lines up with the change ids shown in the preview graph.
func (c Candidate) ShortChangeID() string {
	return c.ChangeIDShort.Prefix + c.ChangeIDShort.Rest
}

// HasDescription reports whether the candidate has a non-empty commit
// description.
func (c Candidate) HasDescription() bool {
	return strings.TrimSpace(c.Description) != ""
}

// ProbablyMerged reports whether c's own description appears verbatim
// somewhere in trunkHistory — the common signature of a GitHub
// squash-merge, which preserves original commit messages in the merge
// commit body even though the merged commit is never a graph-ancestor of
// trunk. A candidate with no description can't be matched this way.
func (c Candidate) ProbablyMerged(trunkHistory string) bool {
	d := strings.TrimSpace(c.Description)

	return d != "" && strings.Contains(trunkHistory, d)
}

// Stale reports whether c hasn't been touched in over after and has no
// bookmark tracking a remote — i.e. was likely forgotten local scratch
// work, not an open PR still under review.
func (c Candidate) Stale(after time.Duration, now time.Time) bool {
	return now.Sub(c.CommitTimestamp) > after && !c.HasTrackedRemote
}

// hoursPerDay is used by Age to convert d.Hours() into a whole day count —
// named rather than a bare 24 literal.
const hoursPerDay = 24

// Age renders how long ago c was committed, relative to now, as a short
// human-readable string ("53 min old", "2 hours old", "3 days old") — the
// largest whole unit that fits, picked the same way Stale's own threshold
// reasoning does (minutes/hours/days, not a raw duration string).
func (c Candidate) Age(now time.Time) string {
	d := now.Sub(c.CommitTimestamp)

	switch {
	case d < time.Hour:
		return pluralize(int(d.Minutes()), "min") + " old"
	case d < hoursPerDay*time.Hour:
		return pluralize(int(d.Hours()), "hour") + " old"
	default:
		return pluralize(int(d.Hours()/hoursPerDay), "day") + " old"
	}
}

// pluralize renders "1 min"/"2 mins" — an "s" appended for anything but 1.
func pluralize(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}

	return fmt.Sprintf("%d %ss", n, unit)
}

// DuplicateKey returns c's content identity relative to its parent(s): two
// candidates with equal DuplicateKeys have byte-identical diffs against the
// same parent set (order of ParentChangeIDs doesn't matter — merge-commit
// parent lists are sorted before joining). See GitCommitDuplicates.
func (c Candidate) DuplicateKey() string {
	parents := append([]string(nil), c.ParentChangeIDs...)
	sort.Strings(parents)

	return strings.Join(parents, ",") + "|" + c.DiffHash
}

// DiffStatSummary renders a short git-style diffstat line, e.g.
// "3 files changed, +45/-2".
func (c Candidate) DiffStatSummary() string {
	if c.FilesChanged == 0 {
		return "no changes"
	}

	files := "files"
	if c.FilesChanged == 1 {
		files = "file"
	}

	return fmt.Sprintf(
		"%d %s changed, +%d/-%d",
		c.FilesChanged,
		files,
		c.LinesAdded,
		c.LinesRemoved,
	)
}

// maxCandidateLineSize raises bufio.Scanner's default 64KiB max token size
// (see the scanner.Buffer call in ParseCandidates) — Template/
// TemplateWithDuplicateKey embed a commit's full description into its
// line, so an unusually long one (a large pasted changelog, a verbose
// generated commit message) could otherwise exceed 64KiB and fail the
// whole bookmarks/commits invocation with bufio.ErrTooLong over a single
// candidate. initialScanBufferSize is the scanner's starting buffer —
// bufio.Scanner's own previous default.
const (
	maxCandidateLineSize  = 8 * 1024 * 1024
	initialScanBufferSize = 64 * 1024
)

// ParseCandidates parses jj's JSONL output (one Candidate per line,
// produced by Template) into a slice of Candidate.
func ParseCandidates(jsonl string) ([]Candidate, error) {
	var candidates []Candidate

	scanner := bufio.NewScanner(strings.NewReader(jsonl))
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxCandidateLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var c Candidate
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("parsing candidate line %q: %w", line, err)
		}

		candidates = append(candidates, c)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning candidate output: %w", err)
	}

	return candidates, nil
}

// SortOldestFirst sorts candidates by commit timestamp ascending, per
// DESIGN.md's review-flow ordering (oldest, least-likely-active forks
// first).
func SortOldestFirst(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CommitTimestamp.Before(candidates[j].CommitTimestamp)
	})
}
