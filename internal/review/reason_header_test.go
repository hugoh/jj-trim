package review

import (
	"context"
	"strings"
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonHeader(t *testing.T) {
	t.Parallel()

	got := reasonHeader(classify.ReasonMerged)
	info := classify.Describe(classify.ReasonMerged)

	assert.Contains(t, got, "[H]")
	assert.Contains(t, got, info.Short)
	assert.Contains(t, got, info.Long)
}

// TestHandleDetailFetched_PrependsReasonHeader guards the detail pane's "why
// was this flagged" text: it must lead whatever the item's ContextFetcher
// fetched (e.g. `jj show`), not replace or follow it, for both the success
// and error paths.
func TestHandleDetailFetched_PrependsReasonHeader(t *testing.T) {
	t.Parallel()

	fetch := func(context.Context, jj.Runner, classify.Candidate) (string, error) {
		return "jj show output", nil
	}

	m := newModel(t.Context(), &jj.Fake{}, []Item{wItem()},
		Action{Verb: "delete", Past: "deleted", Apply: jj.BookmarkDelete}, fetch)

	updated, _ := m.handleDetailFetched(detailFetchedMsg{index: 0, content: "jj show output"})
	mm, ok := updated.(*model)
	require.True(t, ok)

	got := mm.detailCache[0]
	info := classify.Describe(classify.ReasonNoDescription)

	assert.Contains(t, got, info.Short)
	assert.Contains(t, got, info.Long)
	assert.Contains(t, got, "jj show output")
	assert.Less(t, strings.Index(got, info.Long), strings.Index(got, "jj show output"),
		"reason header must lead the fetched context, not follow it")
}

func TestHandleDetailFetched_ErrorPath_StillShowsReasonHeader(t *testing.T) {
	t.Parallel()

	fetch := func(context.Context, jj.Runner, classify.Candidate) (string, error) {
		return "", assert.AnError
	}

	items := []Item{itemWithReason("w", classify.ReasonHasDescription)}

	m := newModel(t.Context(), &jj.Fake{}, items,
		Action{Verb: "delete", Past: "deleted", Apply: jj.BookmarkDelete}, fetch)

	updated, _ := m.handleDetailFetched(detailFetchedMsg{index: 0, err: assert.AnError})
	mm, ok := updated.(*model)
	require.True(t, ok)

	got := mm.detailCache[0]
	info := classify.Describe(classify.ReasonHasDescription)

	assert.Contains(t, got, info.Short)
	assert.Contains(t, got, "error fetching context")
}
