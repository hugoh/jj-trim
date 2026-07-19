package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/hugoh/jj-trim/internal/tty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestTerminationSignals_IncludesSIGTERM guards against Run's
// signal.NotifyContext only reacting to Ctrl-C: without SIGTERM registered,
// a process manager or `kill` sends the TUI straight to os.Exit without
// running Bubbletea's terminal-restore cleanup, leaving the user's terminal
// in raw/alt-screen mode.
func TestTerminationSignals_IncludesSIGTERM(t *testing.T) {
	t.Parallel()

	assert.Contains(t, terminationSignals, os.Interrupt)
	assert.Contains(t, terminationSignals, syscall.SIGTERM)
}

// TestApplyMerged_UndoMessage_UsesOpRevert guards against a real bug found
// this session: the undo instructions used to say "jj op undo", a command
// that doesn't exist as a jj subcommand (only the top-level `jj undo`,
// which takes no operation id). The correct command is `jj op revert
// <opid>`.
func TestApplyMerged_UndoMessage_UsesOpRevert(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("bookmark", "delete", `exact:"feat"`): "",
			jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
				"self.id().short() ++ \"\\n\""): "abc123\n",
		},
	}

	var out strings.Builder

	require.NoError(t, applyMerged(context.Background(), fake, &out, []string{bookmarkFeat}))

	assert.Contains(t, out.String(), "jj op revert abc123")
	assert.NotContains(t, out.String(), "jj op undo")
}

func TestApplyMerged_NoNames_ReportsNothingToDelete(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}

	var out strings.Builder

	require.NoError(t, applyMerged(context.Background(), fake, &out, nil))
	assert.Contains(t, out.String(), "No bookmarks to delete.")
	assert.Empty(t, fake.Calls, "no jj calls should run when there's nothing to delete")
}

func TestPrintReviewResult_NothingApplied(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	printReviewResult(&out, review.Result{})

	assert.Contains(t, out.String(), "Nothing done.")
}

func TestPrintReviewResult_UsesOpRevert_OneLinePerOp(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	printReviewResult(&out, review.Result{
		Applied: []classify.Candidate{{ChangeID: "a"}, {ChangeID: "b"}},
		OpIDs:   []string{"op1", "op2"},
	})

	text := out.String()
	assert.Contains(t, text, "2 item(s) processed.")
	assert.Contains(t, text, "jj op revert op1")
	assert.Contains(t, text, "jj op revert op2")
	assert.NotContains(t, text, "jj op undo")
}

// forkParent is a shared parent change id used across
// TestClassifyForks_SplitsGitCommitDuplicateFromOtherBuckets's fixtures.
const forkParent = "trunk"

const (
	bookmarkFeat            = "feat"
	bookmarkFix             = "fix"
	bookmarkReleaseV2       = "release/v2"
	bookmarkReleaseV2Hotfix = "release/v2/hotfix"
	globReleaseStar         = "release/*"
	globReleaseWip          = "release/[wip]"
	selfChangeID            = "self"

	bookmarkNameProbably = "probably"
	bookmarkNameStale    = "stale"
	showOutputC1         = "show output\n"
	showC1               = "show\n"
	noCandidatesOutput   = "(no candidates)\n"
)

// forkCandidateJSON builds one classify.Template-shaped JSONL line for
// tests that need to control parent_change_ids/diff_hash directly (the
// fields classify.GitCommitDuplicates keys on) without a real jj binary.
func forkCandidateJSON(
	t *testing.T,
	changeID, description string,
	parents []string,
	diffHash string,
) string {
	t.Helper()

	parentsJSON := "[]"
	if len(parents) > 0 {
		parentsJSON = `["` + strings.Join(parents, `","`) + `"]`
	}

	return `{"change_id":"` + changeID + `","change_id_short":{"prefix":"` + changeID +
		`","rest":""},"description":"` + description +
		`","local_bookmarks":[],"commit_timestamp":"2026-01-01T00:00:00Z",` +
		`"files_changed":1,"lines_added":1,"lines_removed":0,"has_tracked_remote":false,` +
		`"parent_change_ids":` + parentsJSON + `,"diff_hash":"` + diffHash + `"}` + "\n"
}

// forksLogKey and keptLogKey are the jj.Fake keys for the two `jj log`
// calls classify.GitCommitDuplicates's callers make: one over
// classify.AnonymousForks(), one over classify.KeptHistory(), both templated
// with classify.TemplateWithDuplicateKey.
func forksLogKey() string {
	return jj.Key(
		"log",
		"-r",
		classify.AnonymousForks(),
		"-T",
		classify.TemplateWithDuplicateKey,
		"--no-graph",
	)
}

func keptLogKey() string {
	return jj.Key(
		"log", "-r", classify.KeptHistory(), "-T", classify.TemplateWithDuplicateKey, "--no-graph",
	)
}

// TestClassifyForks_SplitsGitCommitDuplicateFromOtherBuckets guards the
// run.go wiring around classify.GitCommitDuplicates: a fork whose
// (parents, diff hash) matches something in KeptHistory() must land in the
// duplicate bucket, ordered ahead of and separate from the ordinary
// no-description/has-description buckets, which must be unaffected.
func TestClassifyForks_SplitsGitCommitDuplicateFromOtherBuckets(t *testing.T) {
	t.Parallel()

	orphan := forkCandidateJSON(t, "orphan", "", []string{forkParent}, "same-hash")
	kept := forkCandidateJSON(t, "kept", "raw git commit", []string{forkParent}, "same-hash")
	noDesc := forkCandidateJSON(t, "nodesc", "", []string{forkParent}, "hash-nodesc")
	hasDesc := forkCandidateJSON(
		t,
		"hasdesc",
		"half-finished idea",
		[]string{forkParent},
		"hash-hasdesc",
	)

	forksKey := forksLogKey()
	keptKey := keptLogKey()
	fake := &jj.Fake{
		Stdout: map[string]string{
			forksKey: orphan + noDesc + hasDesc,
			keptKey:  kept,
		},
	}

	buckets, err := classifyForks(context.Background(), fake, false)
	require.NoError(t, err)
	require.Len(t, buckets, 3)

	duplicates := bucketByReason(buckets, classify.ReasonGitCommitDuplicate)
	require.Len(t, duplicates, 1)
	assert.Equal(t, "orphan", duplicates[0].ChangeID)

	noDescription := bucketByReason(buckets, classify.ReasonNoDescription)
	require.Len(t, noDescription, 1)
	assert.Equal(t, "nodesc", noDescription[0].ChangeID)

	hasDescription := bucketByReason(buckets, classify.ReasonHasDescription)
	require.Len(t, hasDescription, 1)
	assert.Equal(t, "hasdesc", hasDescription[0].ChangeID)
}

// TestFilterProtected_HierarchicalGlob_MatchesAcrossSlashes guards a real
// defect found in review: anyBookmarkProtected used to be built on
// path.Match, which treats "/" as a path separator "*" never crosses.
// Bookmark names routinely contain "/" with no path-segment semantics
// (e.g. bookmarkReleaseV2Hotfix), so a --protected pattern like globReleaseStar
// must protect it too, not stop matching at the first "/".
func TestFilterProtected_HierarchicalGlob_MatchesAcrossSlashes(t *testing.T) {
	t.Parallel()

	candidates := []classify.Candidate{
		{ChangeID: "a", LocalBookmarks: []string{bookmarkReleaseV2Hotfix}},
		{ChangeID: "b", LocalBookmarks: []string{"feature/x"}},
	}

	kept := filterProtected(candidates, []string{globReleaseStar})

	require.Len(t, kept, 1, "only the unprotected bookmark must survive filtering")
	assert.Equal(t, "b", kept[0].ChangeID)
}

// TestFilterProtected_LiteralMetacharacters_MatchLiterally guards a real
// defect found in review: anyBookmarkProtected used to discard path.Match's
// error entirely (`ok, _ := path.Match(...)`), so a --protected pattern
// path.Match treated as an invalid glob (e.g. an unterminated "["
// character class) silently protected nothing instead of failing the
// command — a safety gap on a destructive-by-default code path. The fix
// only treats "*" as a wildcard (see globToRegexp) — every other
// character, including "[", is matched literally, so there's no bracket-
// expression syntax left to be malformed, and a bookmark name containing
// one of these characters can still be protected by an exact pattern
// without ever failing to compile.
func TestFilterProtected_LiteralMetacharacters_MatchLiterally(t *testing.T) {
	t.Parallel()

	candidates := []classify.Candidate{{ChangeID: "a", LocalBookmarks: []string{globReleaseWip}}}

	kept := filterProtected(candidates, []string{globReleaseWip})
	assert.Empty(
		t,
		kept,
		"a pattern containing glob/regex metacharacters must still match its exact bookmark name literally",
	)
}

// TestFilterProtected_NoGlobs_ReturnsCandidatesUnchanged guards the
// len(globs) == 0 fast path (no --protected flag given at all).
func TestFilterProtected_NoGlobs_ReturnsCandidatesUnchanged(t *testing.T) {
	t.Parallel()

	candidates := []classify.Candidate{{ChangeID: "a"}}

	kept := filterProtected(candidates, nil)
	assert.Equal(t, candidates, kept)
}

func TestBookmarkLegend_SetsAgeAsDiffStat(t *testing.T) {
	t.Parallel()

	now := time.Now()
	candidates := []classify.Candidate{
		{ChangeID: "a", CommitTimestamp: now.Add(-90 * time.Minute)},
		{ChangeID: "b", CommitTimestamp: now.Add(-3 * 24 * time.Hour)},
	}

	entries := bookmarkLegend(candidates, classify.ReasonMerged)

	require.Len(t, entries, 2)
	assert.Equal(t, "1 hour old", entries[0].DiffStat)
	assert.Equal(t, "3 days old", entries[1].DiffStat)
}

// TestRunTui_NonInteractiveStdin_FailsFast guards the same invariant
// `bookmarks review`/`commits review` already had (see
// reviewCandidates/tty.Require): bare `jj-trim` piped into a script by
// mistake must fail immediately with a clear error, not hang waiting for
// input a non-TTY stdin can never deliver.
func TestRunTui_NonInteractiveStdin_FailsFast(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}

	err := runTui(context.Background(), fake, strings.NewReader(""), &strings.Builder{})
	require.ErrorIs(t, err, tty.ErrNotInteractive)
	assert.Empty(t, fake.Calls, "must fail before issuing any jj calls")
}

// TestRunTui_NonTTYFile_FailsFast covers the tty.Require path specifically
// (as opposed to the "not even an *os.File" path exercised above): a real
// *os.File that isn't a terminal (e.g. /dev/null) must still fail fast.
func TestRunTui_NonTTYFile_FailsFast(t *testing.T) {
	t.Parallel()

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)

	defer func() { _ = devNull.Close() }()

	fake := &jj.Fake{}

	err = runTui(context.Background(), fake, devNull, &strings.Builder{})
	require.ErrorIs(t, err, tty.ErrNotInteractive)
	assert.Empty(t, fake.Calls, "must fail before issuing any jj calls")
}

func TestBookmarksBrowseSession_NoCandidates_EmptySessionNoError(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", classify.MergedBookmarks(trunk), "-T", classify.Template, "--no-graph"):   "",
			jj.Key("log", "-r", classify.UnmergedBookmarks(trunk), "-T", classify.Template, "--no-graph"): "",
			jj.Key("log", "-r", "::("+trunk+")", "--no-graph", "-T",
				"description ++ \"\\n---\\n\""): "",
		},
	}

	sess, err := bookmarksBrowseSession(context.Background(), fake, trimconfig.Config{Trunk: trunk})
	require.NoError(t, err)
	assert.Empty(t, sess.Items)
	assert.Equal(t, verbDelete, sess.Action.Verb)
	require.NotNil(t, sess.Action.CascadeAction)
	assert.Equal(t, verbAbandon, sess.Action.CascadeAction.Verb)
}

func TestCommitsBrowseSession_NoCandidates_EmptySessionNoError(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", classify.AnonymousForks(), "-T", classify.TemplateWithDuplicateKey, "--no-graph"): "",
			jj.Key("log", "-r", classify.KeptHistory(), "-T", classify.TemplateWithDuplicateKey, "--no-graph"):    "",
		},
	}

	sess, err := commitsBrowseSession(context.Background(), fake, trimconfig.Config{})
	require.NoError(t, err)
	assert.Empty(t, sess.Items)
	assert.Equal(t, verbAbandon, sess.Action.Verb)
	assert.Nil(t, sess.Action.CascadeAction)
}

func TestBookmarkNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		candidates []classify.Candidate
		want       []string
	}{
		{
			name:       "empty",
			candidates: nil,
			want:       []string{},
		},
		{
			name: "single candidate with one bookmark",
			candidates: []classify.Candidate{
				{ChangeID: "a", LocalBookmarks: []string{bookmarkFeat}},
			},
			want: []string{bookmarkFeat},
		},
		{
			name: "multiple bookmarks across candidates",
			candidates: []classify.Candidate{
				{ChangeID: "a", LocalBookmarks: []string{bookmarkFeat, bookmarkFix}},
				{ChangeID: "b", LocalBookmarks: []string{"chore"}},
			},
			want: []string{bookmarkFeat, bookmarkFix, "chore"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bookmarkNames(tt.candidates)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCombineForks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		buckets []forkBucket
		want    []classify.Candidate
	}{
		{
			name:    "empty buckets",
			buckets: nil,
			want:    []classify.Candidate{},
		},
		{
			name: "single bucket",
			buckets: []forkBucket{
				{candidates: []classify.Candidate{{ChangeID: "a"}, {ChangeID: "b"}}},
			},
			want: []classify.Candidate{{ChangeID: "a"}, {ChangeID: "b"}},
		},
		{
			name: "multiple buckets preserve order",
			buckets: []forkBucket{
				{candidates: []classify.Candidate{{ChangeID: "a"}}},
				{candidates: []classify.Candidate{{ChangeID: "b"}, {ChangeID: "c"}}},
			},
			want: []classify.Candidate{{ChangeID: "a"}, {ChangeID: "b"}, {ChangeID: "c"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := combineForks(tt.buckets)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBucketByReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		buckets []forkBucket
		reason  classify.Reason
		want    []classify.Candidate
	}{
		{
			name:    "empty buckets",
			buckets: nil,
			reason:  classify.ReasonNoDescription,
			want:    nil,
		},
		{
			name: "reason not found",
			buckets: []forkBucket{
				{reason: classify.ReasonHasDescription},
			},
			reason: classify.ReasonNoDescription,
			want:   nil,
		},
		{
			name: "returns first match",
			buckets: []forkBucket{
				{
					reason:     classify.ReasonNoDescription,
					candidates: []classify.Candidate{{ChangeID: "a"}},
				},
				{
					reason:     classify.ReasonNoDescription,
					candidates: []classify.Candidate{{ChangeID: "b"}},
				},
			},
			reason: classify.ReasonNoDescription,
			want:   []classify.Candidate{{ChangeID: "a"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bucketByReason(tt.buckets, tt.reason)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSummarizeBookmarks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		names          []string
		heuristicCount int
		contains       []string
	}{
		{
			name:           "no bookmarks",
			names:          nil,
			heuristicCount: 0,
			contains:       []string{"No bookmarks to delete"},
		},
		{
			name:           "has bookmarks to delete",
			names:          []string{bookmarkFeat, bookmarkFix},
			heuristicCount: 0,
			contains:       []string{"Would delete 2 bookmark(s)", "[feat fix]", "bookmarks apply"},
		},
		{
			name:           "has bookmarks and heuristic candidates",
			names:          []string{bookmarkFeat},
			heuristicCount: 3,
			contains: []string{
				"Would delete 1 bookmark(s)",
				"3 more bookmark(s) look",
				"bookmarks review",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out strings.Builder
			summarizeBookmarks(&out, tt.names, tt.heuristicCount)

			for _, s := range tt.contains {
				assert.Contains(t, out.String(), s)
			}
		})
	}
}

func TestSummarizeForks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		count    int
		contains []string
	}{
		{
			name:     "no forks",
			count:    0,
			contains: []string{"No anonymous commits found"},
		},
		{
			name:     "some forks found",
			count:    5,
			contains: []string{"5 anonymous commit(s) found", "commits review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out strings.Builder
			summarizeForks(&out, tt.count)

			for _, s := range tt.contains {
				assert.Contains(t, out.String(), s)
			}
		})
	}
}

func TestGlobToRegexp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		glob    string
		match   []string
		noMatch []string
	}{
		{
			name:    "literal string",
			glob:    bookmarkReleaseV2,
			match:   []string{bookmarkReleaseV2},
			noMatch: []string{bookmarkReleaseV2Hotfix, "release/v3"},
		},
		{
			name:    "star crosses slashes",
			glob:    globReleaseStar,
			match:   []string{bookmarkReleaseV2, bookmarkReleaseV2Hotfix, "release/foo"},
			noMatch: []string{"release", "releasex"},
		},
		{
			name:    "leading star",
			glob:    "*fix",
			match:   []string{"hotfix", "-fix", bookmarkFix},
			noMatch: []string{"hotfixer"},
		},
		{
			name:    "bracket metacharacters match literally",
			glob:    globReleaseWip,
			match:   []string{globReleaseWip},
			noMatch: []string{"release/wip", "release/w", "release/[wipx]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			re := globToRegexp(tt.glob)

			for _, s := range tt.match {
				assert.True(t, re.MatchString(s), "%s must match %q", tt.glob, s)
			}

			for _, s := range tt.noMatch {
				assert.False(t, re.MatchString(s), "%s must not match %q", tt.glob, s)
			}
		})
	}
}

func TestAnyBookmarkProtected(t *testing.T) {
	t.Parallel()

	patterns := compileProtectedGlobs([]string{globReleaseStar, "hotfix-*"})

	tests := []struct {
		name      string
		bookmarks []string
		want      bool
	}{
		{
			name:      "empty patterns",
			bookmarks: []string{bookmarkFeat},
			want:      false,
		},
		{
			name:      "matches first pattern",
			bookmarks: []string{bookmarkReleaseV2, bookmarkFeat},
			want:      true,
		},
		{
			name:      "matches second pattern",
			bookmarks: []string{bookmarkFeat, "hotfix-1"},
			want:      true,
		},
		{
			name:      "no match",
			bookmarks: []string{bookmarkFeat, bookmarkFix},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := anyBookmarkProtected(tt.bookmarks, patterns)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompileProtectedGlobs_EmptyInput(t *testing.T) {
	t.Parallel()

	patterns := compileProtectedGlobs(nil)
	assert.Empty(t, patterns)

	patterns = compileProtectedGlobs([]string{})
	assert.Empty(t, patterns)
}

func TestForkKeepRevset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		trunk  string
		forks  []classify.Candidate
		selfID string
		expect string
	}{
		{
			name:   "no other forks",
			trunk:  defaultTrunkRevset,
			forks:  []classify.Candidate{{ChangeID: selfChangeID}},
			selfID: selfChangeID,
			expect: "::(trunk()) | ::(bookmarks()) | ::@",
		},
		{
			name:  "excludes self from others",
			trunk: "trunk()",
			forks: []classify.Candidate{
				{ChangeID: selfChangeID},
				{ChangeID: "other"},
			},
			selfID: selfChangeID,
			expect: "::(trunk()) | ::(bookmarks()) | ::@ | ::(change_id(other))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := forkKeepRevset(tt.trunk, tt.forks, tt.selfID)
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestForksPreviewRevset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		buckets           []forkBucket
		noDescriptionOnly bool
		contains          []string
		notContains       []string
	}{
		{
			name:              "no duplicates",
			buckets:           nil,
			noDescriptionOnly: false,
			contains:          []string{classify.AnonymousForks()},
		},
		{
			name: "includes duplicate change ids",
			buckets: []forkBucket{
				{
					reason:     classify.ReasonGitCommitDuplicate,
					candidates: []classify.Candidate{{ChangeID: "dup"}},
				},
			},
			noDescriptionOnly: false,
			contains:          []string{classify.AnonymousForks(), "dup"},
		},
		{
			name:              "uses no-description-only base",
			buckets:           nil,
			noDescriptionOnly: true,
			contains:          []string{classify.AnonymousForksNoDescription()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := forksPreviewRevset(tt.buckets, tt.noDescriptionOnly)

			for _, s := range tt.contains {
				assert.Contains(t, got, s)
			}

			for _, s := range tt.notContains {
				assert.NotContains(t, got, s)
			}
		})
	}
}

func TestAgeAndDiffStat(t *testing.T) {
	t.Parallel()

	c := classify.Candidate{
		FilesChanged:    3,
		LinesAdded:      45,
		LinesRemoved:    2,
		CommitTimestamp: time.Now().Add(-2 * time.Hour),
	}

	got := ageAndDiffStat(c)
	assert.Contains(t, got, "2 hours old")
	assert.Contains(t, got, "3 files changed, +45/-2")
}

// bookmarkCandidateJSON builds one classify.Template-shaped JSONL line for
// bookmarks-side tests that need to control local_bookmarks/
// commit_timestamp/has_tracked_remote directly (the fields
// heuristicBookmarks' ProbablyMerged/Stale branches key on) without a real
// jj binary. Mirrors forkCandidateJSON's role for the commits side.
func bookmarkCandidateJSON(
	t *testing.T,
	changeID, description string,
	localBookmarks []string,
	commitTimestamp time.Time,
) string {
	t.Helper()

	bookmarksJSON := "[]"
	if len(localBookmarks) > 0 {
		bookmarksJSON = `["` + strings.Join(localBookmarks, `","`) + `"]`
	}

	return `{"change_id":"` + changeID + `","change_id_short":{"prefix":"` + changeID +
		`","rest":""},"description":"` + description +
		`","local_bookmarks":` + bookmarksJSON +
		`,"commit_timestamp":"` + commitTimestamp.UTC().Format(time.RFC3339) + `",` +
		`"files_changed":1,"lines_added":1,"lines_removed":0,"has_tracked_remote":false}` + "\n"
}

// newTempJJRepo creates a throwaway jj repo (no git remote) for the Run/run
// tests below that exercise dispatch through a real jj.ExecRunner: unlike
// every other function in this package, Run and run build their own runner
// internally rather than accepting an injected jj.Runner, so they can't be
// driven by jj.Fake — a real (but tiny, network-free) repo is the only way
// to exercise their success paths.
func newTempJJRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "jj", "git", "init")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "jj git init: %s", out)

	return dir
}

func TestRun_BadFlag_ReturnsExitUsage(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	code := Run(testVersion, []string{"--not-a-real-flag"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitUsage, code)
	assert.NotEmpty(t, stderr.String())
}

func TestRun_UnknownSubcommand_ReturnsExitUsage(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	code := Run(testVersion, []string{"frobnicate"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitUsage, code)
	assert.NotEmpty(t, stderr.String())
}

// TestRun_VersionFlag_PrintsVersionAndExitsOK guards Run's exitEarly path
// (kong.Exit's hook firing on --version before Parse ever reaches run()).
func TestRun_VersionFlag_PrintsVersionAndExitsOK(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	code := Run("1.2.3", []string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitOK, code)
	assert.Contains(t, stdout.String(), "1.2.3")
}

// TestRun_FetchFailure_ReturnsExitRuntime guards Run's exitRuntime path: a
// real (but instantly-failing, no network needed) jj invocation against a
// nonexistent repository directory.
func TestRun_FetchFailure_ReturnsExitRuntime(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder

	args := []string{
		"-R", "/definitely/does/not/exist/jj-trim-test-repo",
		"--fetch", cmdGroupBookmarks, cmdLeafPreview,
	}
	code := Run(testVersion, args, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitRuntime, code)
	assert.Contains(t, stderr.String(), "error:")
}

func TestRun_BookmarksPreview_RealRepo_ReturnsExitOK(t *testing.T) {
	t.Parallel()

	dir := newTempJJRepo(t)

	var stdout, stderr strings.Builder

	args := []string{"-R", dir, cmdGroupBookmarks, cmdLeafPreview}
	code := Run(testVersion, args, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Empty(t, stderr.String())
}

func TestRun_CommitsPreview_RealRepo_ReturnsExitOK(t *testing.T) {
	t.Parallel()

	dir := newTempJJRepo(t)

	var stdout, stderr strings.Builder

	args := []string{"-R", dir, cmdGroupCommits, cmdLeafPreview}
	code := Run(testVersion, args, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Empty(t, stderr.String())
}

func TestRunDispatch_UnknownCommandNoSpace_ReturnsError(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), CLI{}, "bogus", strings.NewReader(""), &strings.Builder{})
	assert.ErrorIs(t, err, errUnknownCommand)
}

func TestRunDispatch_UnknownGroup_ReturnsError(t *testing.T) {
	t.Parallel()

	err := run(
		context.Background(),
		CLI{},
		"frobnicate preview",
		strings.NewReader(""),
		&strings.Builder{},
	)
	assert.ErrorIs(t, err, errUnknownCommand)
}

func TestRunDispatch_Tui_NonInteractive_FailsFast(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), CLI{}, cmdTui, strings.NewReader(""), &strings.Builder{})
	require.ErrorIs(t, err, tty.ErrNotInteractive)
	assert.Contains(t, err.Error(), "checking tui prerequisites")
}

// TestRunDispatch_FetchError_RealJJ_WrapsError guards run's cli.Fetch
// error-wrapping branch. It uses a real jj.ExecRunner (run builds its own,
// non-injectable) against a nonexistent directory, which fails instantly
// with no network access needed.
func TestRunDispatch_FetchError_RealJJ_WrapsError(t *testing.T) {
	t.Parallel()

	cli := CLI{Fetch: true, Repository: "/definitely/does/not/exist/jj-trim-test-repo"}
	err := run(
		context.Background(),
		cli,
		cmdGroupBookmarks+" "+cmdLeafPreview,
		strings.NewReader(""),
		&strings.Builder{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj git fetch")
}

func TestRunDispatch_BookmarksPreview_RealRepo_Succeeds(t *testing.T) {
	t.Parallel()

	dir := newTempJJRepo(t)
	cli := CLI{Repository: dir}

	var out strings.Builder

	err := run(
		context.Background(),
		cli,
		cmdGroupBookmarks+" "+cmdLeafPreview,
		strings.NewReader(""),
		&out,
	)
	require.NoError(t, err)
}

func TestRunDispatch_CommitsPreview_RealRepo_Succeeds(t *testing.T) {
	t.Parallel()

	dir := newTempJJRepo(t)
	cli := CLI{Repository: dir}

	var out strings.Builder

	err := run(
		context.Background(),
		cli,
		cmdGroupCommits+" "+cmdLeafPreview,
		strings.NewReader(""),
		&out,
	)
	require.NoError(t, err)
}

func TestMergedBookmarks(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	key := jj.Key(
		"log",
		"-r",
		classify.MergedBookmarks(trunk),
		"-T",
		classify.Template,
		"--no-graph",
	)

	t.Run("query error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{key: errors.New("boom")}}

		_, err := mergedBookmarks(context.Background(), fake, trunk, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "querying merged bookmarks")
	})

	t.Run("parse error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{key: "not json\n"}}

		_, err := mergedBookmarks(context.Background(), fake, trunk, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing merged bookmarks")
	})

	t.Run("filters protected", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		fake := &jj.Fake{Stdout: map[string]string{
			key: bookmarkCandidateJSON(t, "a", "", []string{bookmarkReleaseV2}, now) +
				bookmarkCandidateJSON(t, "b", "", []string{bookmarkFeat}, now),
		}}

		got, err := mergedBookmarks(context.Background(), fake, trunk, []string{globReleaseStar})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "b", got[0].ChangeID)
	})
}

func TestHeuristicBookmarks(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	now := time.Now()

	unmergedKey := jj.Key(
		"log",
		"-r",
		classify.UnmergedBookmarks(trunk),
		"-T",
		classify.Template,
		"--no-graph",
	)
	trunkHistoryKey := jj.Key(
		"log",
		"-r",
		"::("+trunk+")",
		"--no-graph",
		"-T",
		"description ++ \"\\n---\\n\"",
	)

	t.Run("query error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{unmergedKey: errors.New("boom")}}

		_, _, err := heuristicBookmarks(context.Background(), fake, trunk, nil, defaultStaleAfter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "querying unmerged bookmarks")
	})

	t.Run("parse error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{unmergedKey: "not json\n"}}

		_, _, err := heuristicBookmarks(context.Background(), fake, trunk, nil, defaultStaleAfter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing unmerged bookmarks")
	})

	t.Run("trunk history error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				unmergedKey: bookmarkCandidateJSON(t, "a", "", []string{bookmarkFeat}, now),
			},
			Errs: map[string]error{trunkHistoryKey: errors.New("boom")},
		}

		_, _, err := heuristicBookmarks(context.Background(), fake, trunk, nil, defaultStaleAfter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching trunk history")
	})

	t.Run("classifies probably-merged, stale, and excludes fresh/protected", func(t *testing.T) {
		t.Parallel()

		probablyMergedJSON := bookmarkCandidateJSON(
			t,
			"p1",
			"squashed pr",
			[]string{bookmarkNameProbably},
			now.Add(-time.Hour),
		)
		staleJSON := bookmarkCandidateJSON(
			t,
			"s1",
			"",
			[]string{bookmarkNameStale},
			now.Add(-200*24*time.Hour),
		)
		freshJSON := bookmarkCandidateJSON(t, "f1", "", []string{"fresh"}, now)
		protectedStaleJSON := bookmarkCandidateJSON(
			t, "r1", "", []string{bookmarkReleaseV2}, now.Add(-200*24*time.Hour),
		)

		fake := &jj.Fake{
			Stdout: map[string]string{
				unmergedKey:     probablyMergedJSON + staleJSON + freshJSON + protectedStaleJSON,
				trunkHistoryKey: "squashed pr\n---\n",
			},
		}

		probablyMerged, stale, err := heuristicBookmarks(
			context.Background(), fake, trunk, []string{globReleaseStar}, defaultStaleAfter,
		)
		require.NoError(t, err)
		require.Len(t, probablyMerged, 1)
		assert.Equal(t, "p1", probablyMerged[0].ChangeID)
		require.Len(t, stale, 1)
		assert.Equal(t, "s1", stale[0].ChangeID)
	})
}

func TestBookmarksExceptSelf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		candidate classify.Candidate
		expect    string
	}{
		{
			name:      "no bookmarks",
			candidate: classify.Candidate{},
			expect:    "bookmarks()",
		},
		{
			name:      "one bookmark",
			candidate: classify.Candidate{LocalBookmarks: []string{bookmarkFeat}},
			expect:    `bookmarks() ~ bookmarks(exact:"feat")`,
		},
		{
			name:      "multiple bookmarks",
			candidate: classify.Candidate{LocalBookmarks: []string{bookmarkFeat, bookmarkFix}},
			expect:    `bookmarks() ~ bookmarks(exact:"feat") ~ bookmarks(exact:"fix")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expect, bookmarksExceptSelf(tt.candidate))
		})
	}
}

func TestBookmarkItems(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	candidates := []classify.Candidate{
		{ChangeID: "a", LocalBookmarks: []string{bookmarkFeat}},
	}

	items := bookmarkItems(candidates, classify.ReasonMerged, trunk)
	require.Len(t, items, 1)
	assert.Equal(t, []string{bookmarkFeat}, items[0].IDs)
	assert.Equal(t, candidates[0], items[0].Candidate)
	require.Len(t, items[0].CascadeIDs, 1)
	assert.Contains(t, items[0].CascadeIDs[0], "a")
	assert.Equal(t, classify.ReasonMerged, items[0].Legend.Reason)
}

func TestNewBookmarkContext(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	c := classify.Candidate{ChangeID: "c1", LocalBookmarks: []string{bookmarkFeat}}
	keep := classify.KeepRevset(trunk, bookmarksExceptSelf(c))
	chainKey := jj.Key("log", "-r", classify.PrivateChainRevset(c.ChangeID, keep), "-T",
		`self.change_id().shortest() ++ "\n"`, "--no-graph")

	t.Run("show error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{jj.Key("show", "c1"): errors.New("boom")}}

		fetch := newBookmarkContext(trunk)
		_, err := fetch(context.Background(), fake, classify.Candidate{ChangeID: "c1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "showing candidate")
	})

	t.Run("chain query error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{jj.Key("show", "c1"): showOutputC1},
			Errs:   map[string]error{chainKey: errors.New("boom")},
		}

		fetch := newBookmarkContext(trunk)
		_, err := fetch(context.Background(), fake, c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking private chain")
	})

	t.Run("empty chain reports no private commits", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{
			jj.Key("show", "c1"): showOutputC1,
			chainKey:             "",
		}}

		fetch := newBookmarkContext(trunk)
		got, err := fetch(context.Background(), fake, c)
		require.NoError(t, err)
		assert.Contains(t, got, "no private commits to abandon")
	})

	t.Run("non-empty chain reports cascade", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{
			jj.Key("show", "c1"): showOutputC1,
			chainKey:             "c2\n",
		}}

		fetch := newBookmarkContext(trunk)
		got, err := fetch(context.Background(), fake, c)
		require.NoError(t, err)
		assert.Contains(t, got, "Cascade would additionally abandon")
		assert.Contains(t, got, "c2")
	})
}

func TestNewForkContext(t *testing.T) {
	t.Parallel()

	trunk := commitsTrunk

	descKeyFor := func(c classify.Candidate) string {
		return jj.Key("log", "-r", classify.DescendantsRevset(c.ChangeID), "-T",
			`self.change_id().shortest() ++ "\n"`, "--no-graph")
	}
	chainKeyFor := func(c classify.Candidate, forks []classify.Candidate) string {
		keep := forkKeepRevset(trunk, forks, c.ChangeID)

		return jj.Key("log", "-r", classify.PrivateChainRevset(c.ChangeID, keep), "-T",
			`self.change_id().shortest() ++ "\n"`, "--no-graph")
	}

	t.Run("show error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{jj.Key("show", "c1"): errors.New("boom")}}

		fetch := newForkContext(trunk, nil)
		_, err := fetch(context.Background(), fake, classify.Candidate{ChangeID: "c1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "showing candidate")
	})

	t.Run("descendants query error", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{ChangeID: "c1"}
		fake := &jj.Fake{
			Stdout: map[string]string{jj.Key("show", "c1"): showC1},
			Errs:   map[string]error{descKeyFor(c): errors.New("boom")},
		}

		fetch := newForkContext(trunk, nil)
		_, err := fetch(context.Background(), fake, c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking descendants")
	})

	t.Run("chain query error", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{ChangeID: "c1"}
		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("show", "c1"): showC1,
				descKeyFor(c):        "",
			},
			Errs: map[string]error{chainKeyFor(c, nil): errors.New("boom")},
		}

		fetch := newForkContext(trunk, nil)
		_, err := fetch(context.Background(), fake, c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking private chain")
	})

	t.Run("descendants and private chain both present", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{ChangeID: "c1"}
		fake := &jj.Fake{Stdout: map[string]string{
			jj.Key("show", "c1"): showC1,
			descKeyFor(c):        "d1\n",
			chainKeyFor(c, nil):  "c1\n",
		}}

		fetch := newForkContext(trunk, nil)
		got, err := fetch(context.Background(), fake, c)
		require.NoError(t, err)
		assert.Contains(t, got, "Descendants that will be rebased")
		assert.Contains(t, got, "d1")
		assert.Contains(t, got, "Will abandon:")
		assert.Contains(t, got, "c1")
	})

	t.Run("no descendants", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{ChangeID: "c1"}
		fake := &jj.Fake{Stdout: map[string]string{
			jj.Key("show", "c1"): showC1,
			descKeyFor(c):        "",
			chainKeyFor(c, nil):  "",
		}}

		fetch := newForkContext(trunk, nil)
		got, err := fetch(context.Background(), fake, c)
		require.NoError(t, err)
		assert.NotContains(t, got, "Descendants that will be rebased")
		assert.Contains(t, got, "Will abandon:")
	})
}

func TestForkItemsForReason(t *testing.T) {
	t.Parallel()

	now := time.Now()
	older := classify.Candidate{ChangeID: "old", CommitTimestamp: now.Add(-48 * time.Hour)}
	newer := classify.Candidate{ChangeID: "new", CommitTimestamp: now.Add(-1 * time.Hour)}

	items := forkItemsForReason(
		[]classify.Candidate{newer, older},
		classify.ReasonNoDescription,
		[]classify.Candidate{newer, older},
		defaultTrunkRevset,
	)

	require.Len(t, items, 2)
	assert.Equal(t, "old", items[0].Candidate.ChangeID, "oldest must come first")
	assert.Equal(t, "new", items[1].Candidate.ChangeID)
	assert.Equal(t, classify.ReasonNoDescription, items[0].Legend.Reason)
	assert.Contains(t, items[0].Legend.DiffStat, "old")
	assert.Empty(t, items[0].CascadeIDs, "commits review has no cascade action")
}

func TestBuildForksLegend(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, buildForksLegend(nil))
	})

	t.Run("stamps age per bucket, preserving order", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		buckets := []forkBucket{
			{
				reason: classify.ReasonGitCommitDuplicate,
				candidates: []classify.Candidate{
					{ChangeID: "d1", CommitTimestamp: now.Add(-time.Hour)},
				},
			},
			{
				reason: classify.ReasonNoDescription,
				candidates: []classify.Candidate{
					{ChangeID: "n1", CommitTimestamp: now.Add(-2 * time.Hour)},
				},
			},
		}

		legend := buildForksLegend(buckets)
		require.Len(t, legend, 2)
		assert.Equal(t, classify.ReasonGitCommitDuplicate, legend[0].Reason)
		assert.Contains(t, legend[0].DiffStat, "hour")
		assert.Equal(t, classify.ReasonNoDescription, legend[1].Reason)
	})
}

func TestDeleteBookmarkBatch(t *testing.T) {
	t.Parallel()

	deleteKey := jj.Key("bookmark", "delete", `exact:"feat"`)
	opLogKey := jj.Key(
		"op",
		"log",
		"--no-graph",
		"--limit",
		"1",
		"-T",
		"self.id().short() ++ \"\\n\"",
	)

	t.Run("delete error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{deleteKey: errors.New("boom")}}

		_, err := deleteBookmarkBatch(context.Background(), fake, []string{bookmarkFeat})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleting merged bookmarks")
	})

	t.Run("op id lookup error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{deleteKey: ""},
			Errs:   map[string]error{opLogKey: errors.New("boom")},
		}

		_, err := deleteBookmarkBatch(context.Background(), fake, []string{bookmarkFeat})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "looking up resulting op id")
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{
			deleteKey: "",
			opLogKey:  "op1\n",
		}}

		opID, err := deleteBookmarkBatch(context.Background(), fake, []string{bookmarkFeat})
		require.NoError(t, err)
		assert.Equal(t, "op1", opID)
	})
}

func TestRequireTerminal_StdinNotFile_ReturnsErrNotInteractive(t *testing.T) {
	t.Parallel()

	err := requireTerminal(strings.NewReader(""), &strings.Builder{})
	require.ErrorIs(t, err, tty.ErrNotInteractive)
}

func TestRequireTerminal_StdoutNotFile_ReturnsErrNotInteractive(t *testing.T) {
	t.Parallel()

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)

	defer func() { _ = devNull.Close() }()

	err = requireTerminal(devNull, &strings.Builder{})
	require.ErrorIs(t, err, tty.ErrNotInteractive)
}

// TestRequireTerminal_BothTerminals_ReturnsNil covers requireTerminal's
// success path by swapping tty.IsTerminal, the same technique internal/tty
// and internal/spin's own tests use — there's no portable way to fake "is a
// real terminal" for an arbitrary *os.File without an actual pty. Not run
// in parallel with other tests: it mutates shared package state in
// internal/tty.
func TestRequireTerminal_BothTerminals_ReturnsNil(t *testing.T) {
	original := tty.IsTerminal
	tty.IsTerminal = func(*os.File) bool { return true }

	t.Cleanup(func() { tty.IsTerminal = original })

	r, w, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = r.Close(); _ = w.Close() }()

	assert.NoError(t, requireTerminal(r, w))
}

// TestRequireTerminal_TtyRequireError_Wrapped covers the branch where stdin
// and stdout are both *os.File (so requireTerminal's own type assertions
// pass) but tty.Require itself rejects one of them — same global-var-swap
// technique and non-parallel caveat as
// TestRequireTerminal_BothTerminals_ReturnsNil above.
func TestRequireTerminal_TtyRequireError_Wrapped(t *testing.T) {
	original := tty.IsTerminal
	tty.IsTerminal = func(*os.File) bool { return false }

	t.Cleanup(func() { tty.IsTerminal = original })

	r, w, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = r.Close(); _ = w.Close() }()

	err = requireTerminal(r, w)
	require.ErrorIs(t, err, tty.ErrNotInteractive)
	assert.Contains(t, err.Error(), "checking tty")
}

func TestReviewCandidates_NonInteractive_FailsFast(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}
	action := review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon}

	err := reviewCandidates(
		context.Background(),
		fake,
		strings.NewReader(""),
		&strings.Builder{},
		nil,
		action,
		nil,
	)
	require.ErrorIs(t, err, tty.ErrNotInteractive)
	assert.Empty(t, fake.Calls, "must fail before issuing any jj calls")
}

func TestRunBookmarks(t *testing.T) {
	t.Parallel()

	trunk := defaultTrunkRevset
	now := time.Now()

	mergedJSON := bookmarkCandidateJSON(
		t,
		"m1",
		"",
		[]string{bookmarkFeat},
		now.Add(-time.Hour),
	)
	probablyJSON := bookmarkCandidateJSON(
		t,
		"p1",
		"squashed pr",
		[]string{bookmarkNameProbably},
		now.Add(-2*time.Hour),
	)
	staleJSON := bookmarkCandidateJSON(
		t,
		"s1",
		"",
		[]string{bookmarkNameStale},
		now.Add(-200*24*time.Hour),
	)

	probablyMerged := []classify.Candidate{
		{ChangeID: "p1", LocalBookmarks: []string{bookmarkNameProbably}},
	}
	stale := []classify.Candidate{{ChangeID: "s1", LocalBookmarks: []string{bookmarkNameStale}}}
	previewRevset := bookmarksPreviewRevset(trunk, probablyMerged, stale)

	newFixture := func() *jj.Fake {
		return &jj.Fake{Stdout: map[string]string{
			jj.Key("log", "-r", classify.MergedBookmarks(trunk), "-T", classify.Template, "--no-graph"): mergedJSON,
			jj.Key(
				"log", "-r", classify.UnmergedBookmarks(trunk), "-T", classify.Template, "--no-graph",
			): probablyJSON + staleJSON,
			jj.Key("log", "-r", "::("+trunk+")", "--no-graph", "-T", "description ++ \"\\n---\\n\""): "squashed pr\n---\n",
			jj.Key("log", "-r", previewRevset, "--no-pager", "--color=never"):                        "@ preview\n",
		}}
	}

	t.Run("preview action summarizes without deleting", func(t *testing.T) {
		t.Parallel()

		fake := newFixture()

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			BookmarksCmd{},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Would delete 1 bookmark(s)")
		assert.Contains(t, out.String(), "2 more bookmark(s) look")

		for _, c := range fake.Calls {
			if len(c.Args) >= 2 {
				assert.False(
					t,
					c.Args[0] == "bookmark" && c.Args[1] == "delete",
					"preview must never delete",
				)
			}
		}
	})

	t.Run("apply action deletes merged bucket only", func(t *testing.T) {
		t.Parallel()

		fake := newFixture()
		fake.Stdout[jj.Key("bookmark", "delete", `exact:"feat"`)] = ""
		fake.Stdout[jj.Key("op", "log", "--no-graph", "--limit", "1", "-T", "self.id().short() ++ \"\\n\"")] = "op1\n"

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			BookmarksCmd{},
			cmdLeafApply,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Deleted 1 bookmark(s)")
		assert.Contains(t, out.String(), "jj op revert op1")
	})

	t.Run("review action requires a terminal", func(t *testing.T) {
		t.Parallel()

		fake := newFixture()

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			BookmarksCmd{},
			cmdLeafReview,
			strings.NewReader(""),
			&out,
		)
		require.ErrorIs(t, err, tty.ErrNotInteractive)
	})

	t.Run("unrecognized action falls back to preview", func(t *testing.T) {
		t.Parallel()

		fake := newFixture()

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			BookmarksCmd{},
			"bogus",
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Would delete 1 bookmark(s)")
	})

	t.Run("classification error propagates", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{
			jj.Key("log", "-r", classify.MergedBookmarks(trunk), "-T", classify.Template, "--no-graph"): errors.New(
				"boom",
			),
		}}

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			BookmarksCmd{},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "classifying bookmarks")
	})

	t.Run("custom stale-after and trunk flags used", func(t *testing.T) {
		t.Parallel()

		customTrunk := "custom_trunk()"
		staleAfter := 10 * 24 * time.Hour
		fake := &jj.Fake{Stdout: map[string]string{
			jj.Key("log", "-r", classify.MergedBookmarks(customTrunk), "-T", classify.Template, "--no-graph"):   "",
			jj.Key("log", "-r", classify.UnmergedBookmarks(customTrunk), "-T", classify.Template, "--no-graph"): "",
			jj.Key("log", "-r", "::("+customTrunk+")", "--no-graph", "-T", "description ++ \"\\n---\\n\""):      "",
			jj.Key(
				"log", "-r", classify.MergedBookmarks(customTrunk), "--no-pager", "--color=never",
			): noCandidatesOutput,
		}}
		cmd := BookmarksCmd{Trunk: customTrunk, StaleAfter: &staleAfter}

		var out strings.Builder

		err := runBookmarks(
			context.Background(),
			fake,
			cmd,
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
	})
}

func TestRunCommits(t *testing.T) {
	t.Parallel()

	forksKey := forksLogKey()
	keptKey := keptLogKey()

	duplicateBuckets := []forkBucket{
		{
			reason:     classify.ReasonGitCommitDuplicate,
			candidates: []classify.Candidate{{ChangeID: "orphan"}},
		},
	}

	orphanKeptFake := func(t *testing.T) *jj.Fake {
		t.Helper()

		orphan := forkCandidateJSON(t, "orphan", "", []string{forkParent}, "same-hash")
		kept := forkCandidateJSON(t, "kept", "raw git commit", []string{forkParent}, "same-hash")
		previewRevset := forksPreviewRevset(duplicateBuckets, false)

		return &jj.Fake{Stdout: map[string]string{
			forksKey: orphan,
			keptKey:  kept,
			jj.Key("log", "-r", previewRevset, "--no-pager", "--color=never"): "@ orphan\n",
		}}
	}

	t.Run("preview action summarizes without abandoning", func(t *testing.T) {
		t.Parallel()

		fake := orphanKeptFake(t)

		var out strings.Builder

		err := runCommits(
			context.Background(),
			fake,
			CommitsCmd{},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "1 anonymous commit(s) found")
		assert.Contains(t, out.String(), "commits review")
	})

	t.Run("no candidates reports none found", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{
			forksKey: "",
			keptKey:  "",
			jj.Key("log", "-r", classify.AnonymousForks(), "--no-pager", "--color=never"): noCandidatesOutput,
		}}

		var out strings.Builder

		err := runCommits(
			context.Background(),
			fake,
			CommitsCmd{},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "No anonymous commits found.")
	})

	t.Run("review action requires a terminal", func(t *testing.T) {
		t.Parallel()

		fake := orphanKeptFake(t)

		var out strings.Builder

		err := runCommits(
			context.Background(),
			fake,
			CommitsCmd{},
			cmdLeafReview,
			strings.NewReader(""),
			&out,
		)
		require.ErrorIs(t, err, tty.ErrNotInteractive)
	})

	t.Run("classification error propagates", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Errs: map[string]error{forksKey: errors.New("boom")}}

		var out strings.Builder

		err := runCommits(
			context.Background(),
			fake,
			CommitsCmd{},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "classifying commits")
	})

	t.Run("no-description-only narrows preview base", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{Stdout: map[string]string{
			forksKey: "",
			keptKey:  "",
			jj.Key("log", "-r", classify.AnonymousForksNoDescription(), "--no-pager", "--color=never"): noCandidatesOutput,
		}}

		var out strings.Builder

		err := runCommits(
			context.Background(),
			fake,
			CommitsCmd{NoDescriptionOnly: true},
			cmdLeafPreview,
			strings.NewReader(""),
			&out,
		)
		require.NoError(t, err)
	})
}

func TestBookmarksPreviewRevset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		trunk  string
		sets   [][]classify.Candidate
		expect string
	}{
		{
			name:   "no heuristic candidates",
			trunk:  defaultTrunkRevset,
			sets:   nil,
			expect: classify.MergedBookmarks("trunk()"),
		},
		{
			name:  "includes heuristic bookmark names",
			trunk: "trunk()",
			sets: [][]classify.Candidate{
				{{LocalBookmarks: []string{"maybe-stale"}}},
			},
			expect: classify.MergedBookmarks("trunk()") + ` | bookmarks(exact:"maybe-stale")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bookmarksPreviewRevset(tt.trunk, tt.sets...)
			assert.Equal(t, tt.expect, got)
		})
	}
}
