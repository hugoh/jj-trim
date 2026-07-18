package classify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// candidateLine builds one JSONL line matching classify.Template's shape,
// keeping fixture literals below the linter's line-length limit.
func candidateLine(t *testing.T,
	changeID, changeIDShort, description, localBookmarks, commitTimestamp string,
) string {
	t.Helper()

	return candidateLineWithDiffStat(t, changeID, changeIDShort, description, localBookmarks,
		commitTimestamp, "0", "0", "0")
}

// candidateLineWithDiffStat is candidateLine plus the files_changed/
// lines_added/lines_removed fields, for tests that exercise diffstats.
func candidateLineWithDiffStat(t *testing.T,
	changeID, changeIDShort, description, localBookmarks, commitTimestamp string,
	filesChanged, linesAdded, linesRemoved string,
) string {
	t.Helper()

	return `{"change_id":` + changeID +
		`,"change_id_short":` + changeIDShort +
		`,"description":` + description +
		`,"local_bookmarks":` + localBookmarks +
		`,"commit_timestamp":` + commitTimestamp +
		`,"files_changed":` + filesChanged +
		`,"lines_added":` + linesAdded +
		`,"lines_removed":` + linesRemoved + "}\n"
}

func TestParseCandidates(t *testing.T) {
	t.Parallel()

	jsonl := candidateLineWithDiffStat(
		t,
		`"wktszynyokvykyzoqmnkxkmutsvsvuzt"`,
		`{"prefix":"w","rest":""}`,
		`"feature\n"`,
		`["main","merged"]`,
		`"2026-07-01T14:44:48-05:00"`,
		"3",
		"45",
		"2",
	) +
		candidateLine(
			t,
			`"abc"`,
			`{"prefix":"a","rest":"bc"}`,
			`""`,
			`[]`,
			`"2026-06-01T00:00:00-05:00"`,
		)

	candidates, err := classify.ParseCandidates(jsonl)
	require.NoError(t, err)
	require.Len(t, candidates, 2)

	assert.Equal(t, "wktszynyokvykyzoqmnkxkmutsvsvuzt", candidates[0].ChangeID)
	assert.Equal(t, "w", candidates[0].ShortChangeID())
	assert.True(t, candidates[0].HasDescription())
	assert.Equal(t, []string{"main", "merged"}, candidates[0].LocalBookmarks)
	assert.Equal(t, 3, candidates[0].FilesChanged)
	assert.Equal(t, 45, candidates[0].LinesAdded)
	assert.Equal(t, 2, candidates[0].LinesRemoved)
	assert.Equal(t, "3 files changed, +45/-2", candidates[0].DiffStatSummary())

	assert.Equal(t, "abc", candidates[1].ChangeID)
	assert.Equal(t, "abc", candidates[1].ShortChangeID())
	assert.False(t, candidates[1].HasDescription())
	assert.Equal(t, "no changes", candidates[1].DiffStatSummary())
}

func TestDiffStatSummary_SingleFile(t *testing.T) {
	t.Parallel()

	c := classify.Candidate{FilesChanged: 1, LinesAdded: 5, LinesRemoved: 0}
	assert.Equal(t, "1 file changed, +5/-0", c.DiffStatSummary())
}

func TestParseCandidates_EmptyLinesIgnored(t *testing.T) {
	t.Parallel()

	candidates, err := classify.ParseCandidates("\n\n")
	require.NoError(t, err)
	assert.Empty(t, candidates)
}

func TestParseCandidates_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := classify.ParseCandidates("not json\n")
	require.Error(t, err)
}

// TestParseCandidates_LongDescription_DoesNotFailOnBufioDefaultLimit
// guards a real bug found in review: ParseCandidates used to scan with
// bufio.Scanner's default 64KiB max token size, uncontrolled by a
// scanner.Buffer call. Template/TemplateWithDuplicateKey embed the full
// commit description into a candidate's line, so an unusually long one
// (simulated here well past 64KiB) used to fail the whole invocation with
// bufio.ErrTooLong instead of parsing successfully.
func TestParseCandidates_LongDescription_DoesNotFailOnBufioDefaultLimit(t *testing.T) {
	t.Parallel()

	longDescription := strings.Repeat("a", 100*1024) // 100KiB, past the old 64KiB default

	jsonl := candidateLine(
		t,
		`"w"`,
		`{"prefix":"w","rest":""}`,
		`"`+longDescription+`"`,
		`[]`,
		`"2026-07-01T14:44:48-05:00"`,
	)

	candidates, err := classify.ParseCandidates(jsonl)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Len(t, candidates[0].Description, 100*1024)
}

func TestSortOldestFirst(t *testing.T) {
	t.Parallel()

	newer := classify.Candidate{
		ChangeID:        "new",
		CommitTimestamp: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	older := classify.Candidate{
		ChangeID:        "old",
		CommitTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	candidates := []classify.Candidate{newer, older}
	classify.SortOldestFirst(candidates)

	assert.Equal(t, "old", candidates[0].ChangeID)
	assert.Equal(t, "new", candidates[1].ChangeID)
}

func TestProbablyMerged(t *testing.T) {
	t.Parallel()

	trunkHistory := "root\n---\nMerge pull request #41 from hugoh/build-cpd\n\n" +
		"build: moved from jscpd to jscpd v5's cpd\n---\n"

	t.Run("description found in trunk history", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{Description: "build: moved from jscpd to jscpd v5's cpd\n"}
		assert.True(t, c.ProbablyMerged(trunkHistory))
	})

	t.Run("description not found", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{Description: "totally unrelated work\n"}
		assert.False(t, c.ProbablyMerged(trunkHistory))
	})

	t.Run("empty description never matches", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{Description: ""}
		assert.False(t, c.ProbablyMerged(trunkHistory))
	})
}

func TestStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	after := 90 * 24 * time.Hour

	t.Run("old and no tracked remote is stale", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{
			CommitTimestamp:  now.Add(-100 * 24 * time.Hour),
			HasTrackedRemote: false,
		}
		assert.True(t, c.Stale(after, now))
	})

	t.Run("old but tracks a remote is not stale", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{
			CommitTimestamp:  now.Add(-100 * 24 * time.Hour),
			HasTrackedRemote: true,
		}
		assert.False(t, c.Stale(after, now))
	})

	t.Run("recent is not stale", func(t *testing.T) {
		t.Parallel()

		c := classify.Candidate{
			CommitTimestamp:  now.Add(-10 * 24 * time.Hour),
			HasTrackedRemote: false,
		}
		assert.False(t, c.Stale(after, now))
	})
}

func TestAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"one minute", 1 * time.Minute, "1 min old"},
		{"53 minutes", 53 * time.Minute, "53 mins old"},
		{"one hour", 1 * time.Hour, "1 hour old"},
		{"2 hours", 2 * time.Hour, "2 hours old"},
		{"23 hours", 23 * time.Hour, "23 hours old"},
		{"one day", 24 * time.Hour, "1 day old"},
		{"3 days", 3 * 24 * time.Hour, "3 days old"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := classify.Candidate{CommitTimestamp: now.Add(-tt.ago)}
			assert.Equal(t, tt.want, c.Age(now))
		})
	}
}
