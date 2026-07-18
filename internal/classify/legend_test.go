package classify_test

import (
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildLegend(t *testing.T) {
	t.Parallel()

	jsonl := candidateLine(
		t,
		`"w1"`,
		`{"prefix":"w","rest":""}`,
		`""`,
		`["tags"]`,
		`"2026-01-01T00:00:00Z"`,
	) +
		candidateLine(
			t,
			`"abc"`,
			`{"prefix":"a","rest":"bc"}`,
			`""`,
			`[]`,
			`"2026-01-01T00:00:00Z"`,
		)

	candidates, err := classify.ParseCandidates(jsonl)
	require.NoError(t, err)

	entries := classify.BuildLegend(candidates, classify.ReasonMerged)
	assert.Equal(t, []classify.LegendEntry{
		{ChangeIDShort: "w", Bookmarks: []string{"tags"}, Reason: classify.ReasonMerged},
		{ChangeIDShort: "abc", Bookmarks: []string{}, Reason: classify.ReasonMerged},
	}, entries)
}

func TestLegendEntry_String(t *testing.T) {
	t.Parallel()

	t.Run("with bookmarks", func(t *testing.T) {
		t.Parallel()

		e := classify.LegendEntry{
			ChangeIDShort: "tun",
			Bookmarks:     []string{"release"},
			Reason:        classify.ReasonMerged,
		}
		assert.Equal(t, "release (tun)  [H] merged into trunk", e.String())
	})

	t.Run("multiple bookmarks", func(t *testing.T) {
		t.Parallel()

		e := classify.LegendEntry{
			ChangeIDShort: "tun",
			Bookmarks:     []string{"release", "old-release"},
			Reason:        classify.ReasonMerged,
		}
		assert.Equal(t, "release, old-release (tun)  [H] merged into trunk", e.String())
	})

	t.Run("no bookmarks", func(t *testing.T) {
		t.Parallel()

		e := classify.LegendEntry{ChangeIDShort: "w", Reason: classify.ReasonNoDescription}
		assert.Equal(t, "w  [M] anonymous fork, no description", e.String())
	})

	t.Run("with diffstat", func(t *testing.T) {
		t.Parallel()

		e := classify.LegendEntry{
			ChangeIDShort: "w",
			Reason:        classify.ReasonNoDescription,
			DiffStat:      "3 files changed, +45/-2",
		}
		assert.Equal(
			t,
			"w  [M] anonymous fork, no description — 3 files changed, +45/-2",
			e.String(),
		)
	})
}
