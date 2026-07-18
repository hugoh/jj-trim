package main

import (
	"context"
	"os"
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
			jj.Key("bookmark", "delete", "exact:feat"): "",
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

	forksKey := jj.Key(
		"log",
		"-r",
		classify.AnonymousForks(),
		"-T",
		classify.TemplateWithDuplicateKey,
		"--no-graph",
	)
	keptKey := jj.Key(
		"log", "-r", classify.KeptHistory(), "-T", classify.TemplateWithDuplicateKey, "--no-graph",
	)
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
