package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testVerbDelete    = "delete"
	testPastDeleted   = "deleted"
	testVerbAbandon   = "abandon"
	testPastAbandoned = "abandoned"
)

// TestHandleDetailFetched_StaleIndexIgnored guards against a stale
// detailFetchedMsg (issued for an index into a since-shrunk m.items, e.g.
// after pruneApplied) panicking with index out of range — every other
// consumer of a stored item index already bounds-checks it.
func TestHandleDetailFetched_StaleIndexIgnored(t *testing.T) {
	t.Parallel()

	items := []Item{
		{
			IDs:       []string{"w"},
			Candidate: classify.Candidate{ChangeID: "w"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "w",
				Reason:        classify.ReasonNoDescription,
			},
		},
	}

	m := newModel(
		t.Context(),
		&jj.Fake{},
		items,
		Action{Verb: testVerbDelete, Past: testPastDeleted},
		noopFetch,
	)

	require.NotPanics(t, func() {
		_, _ = m.handleDetailFetched(detailFetchedMsg{index: 5, content: "stale"})
	})

	assert.Empty(t, m.detailCache, "a stale out-of-range index must not populate detailCache")
}

func noopFetch(context.Context, jj.Runner, classify.Candidate) (string, error) {
	return "context", nil
}

// detailScrollTestModel builds a ready (window-sized) model on screenList
// with m.detail seeded with enough lines to overflow its pane height, so
// scroll keys have visible room to move. The window is sized small (a
// handful of rows) so the detail pane's height is small too, keeping the
// half-page-vs-line-scroll distinction meaningful without needing a huge
// content block.
func detailScrollTestModel(t *testing.T) *model {
	t.Helper()

	items := []Item{
		{
			IDs:       []string{"w"},
			Candidate: classify.Candidate{ChangeID: "w"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "w",
				Reason:        classify.ReasonNoDescription,
			},
		},
	}

	m := newModel(
		t.Context(),
		&jj.Fake{},
		items,
		Action{Verb: testVerbDelete, Past: testPastDeleted},
		noopFetch,
	)

	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})

	lines := make([]string, 0, 100)
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}

	m.detail.SetContent(strings.Join(lines, "\n"))

	return m
}

func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// TestDetailScroll_CtrlJK_ScrollsOneLineAtATime guards ctrl+j/ctrl+k as the
// detail pane's line-scroll keys — chosen because plain arrows/j/k are
// already claimed by m.list's own navigation (handleListNavigation).
func TestDetailScroll_CtrlJK_ScrollsOneLineAtATime(t *testing.T) {
	t.Parallel()

	m := detailScrollTestModel(t)
	require.Equal(t, 0, m.detail.YOffset())

	_, _ = m.handleKey(ctrlKey('j'))
	assert.Equal(t, 1, m.detail.YOffset(), "ctrl+j should scroll down by one line")

	_, _ = m.handleKey(ctrlKey('j'))
	assert.Equal(t, 2, m.detail.YOffset())

	_, _ = m.handleKey(ctrlKey('k'))
	assert.Equal(t, 1, m.detail.YOffset(), "ctrl+k should scroll back up by one line")
}

// TestDetailScroll_CtrlDU_ScrollsHalfPage guards ctrl+d/ctrl+u as the detail
// pane's half-page-scroll keys, matching vim's own convention.
func TestDetailScroll_CtrlDU_ScrollsHalfPage(t *testing.T) {
	t.Parallel()

	m := detailScrollTestModel(t)
	half := m.detail.Height() / 2

	_, _ = m.handleKey(ctrlKey('d'))
	assert.Equal(t, half, m.detail.YOffset(), "ctrl+d should scroll down half a page")

	_, _ = m.handleKey(ctrlKey('u'))
	assert.Equal(t, 0, m.detail.YOffset(), "ctrl+u should scroll back up half a page")
}

// TestDetailScroll_DoesNotMoveListCursorOrTriggerMarkKeys guards the
// precedence handleDetailScrollKey needs over handleListMarkKey/
// handleListNavigation: ctrl+j/k/d/u must never leak through as list
// movement or as a mark key, even though 'd' alone is this model's action
// mark key (testVerbDelete's markKey is "d").
func TestDetailScroll_DoesNotMoveListCursorOrTriggerMarkKeys(t *testing.T) {
	t.Parallel()

	m := detailScrollTestModel(t)
	before := m.list.Index()

	_, _ = m.handleKey(ctrlKey('j'))
	_, _ = m.handleKey(ctrlKey('k'))
	_, _ = m.handleKey(ctrlKey('d'))
	_, _ = m.handleKey(ctrlKey('u'))

	assert.Equal(t, before, m.list.Index(), "detail scroll keys must not move the list cursor")
	assert.Equal(t, decisionPending, m.items[0].decision,
		"ctrl+d must not be treated as the plain 'd' mark key")
}

// TestDetailScroll_ResetsOnCursorMove guards against a scrolled-down detail
// pane leaking its offset into the next-selected item's content, which
// would show that item's detail starting mid-way down instead of at top.
func TestDetailScroll_ResetsOnCursorMove(t *testing.T) {
	t.Parallel()

	items := []Item{
		{
			IDs:       []string{"w"},
			Candidate: classify.Candidate{ChangeID: "w"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "w",
				Reason:        classify.ReasonNoDescription,
			},
		},
		{
			IDs:       []string{"a"},
			Candidate: classify.Candidate{ChangeID: "a"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "a",
				Reason:        classify.ReasonHasDescription,
			},
		},
	}

	m := newModel(
		t.Context(),
		&jj.Fake{},
		items,
		Action{Verb: testVerbDelete, Past: testPastDeleted},
		noopFetch,
	)
	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})

	lines := make([]string, 0, 100)
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}

	m.detail.SetContent(strings.Join(lines, "\n"))

	_, _ = m.handleKey(ctrlKey('j'))
	require.Positive(t, m.detail.YOffset())

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})

	assert.Equal(
		t,
		0,
		m.detail.YOffset(),
		"moving the list cursor should reset the detail pane's scroll",
	)
}

// TestResultFromFinalModel_KeepsResultAlongsideError guards the bug Run
// used to have: discarding an already-accumulated Result whenever the
// session quit with a pending batch error, instead of returning both.
func TestResultFromFinalModel_KeepsResultAlongsideError(t *testing.T) {
	t.Parallel()

	opKey, opOut := jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
		"self.id().short() ++ \"\\n\""), "abc123\n"
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("bookmark", testVerbDelete, "exact:w", "exact:a"): "",
			opKey: opOut,
		},
		Errs: map[string]error{jj.Key(testVerbAbandon, "chain-a"): errors.New("boom")},
	}
	action := Action{
		Verb: testVerbDelete, Past: testPastDeleted, Apply: jj.BookmarkDelete,
		CascadeAction: &Action{Verb: testVerbAbandon, Past: testPastAbandoned, Apply: jj.Abandon},
	}
	items := []Item{
		{
			IDs: []string{
				"w",
			}, CascadeIDs: []string{"chain-w"}, Candidate: classify.Candidate{ChangeID: "w"},
			Legend: classify.LegendEntry{ChangeIDShort: "w", Reason: classify.ReasonNoDescription},
		},
		{
			IDs: []string{
				"a",
			}, CascadeIDs: []string{"chain-a"}, Candidate: classify.Candidate{ChangeID: "a"},
			Legend: classify.LegendEntry{ChangeIDShort: "a", Reason: classify.ReasonHasDescription},
		},
	}

	tm := teatest.NewTestModel(t, newModel(t.Context(), fake, items, action, noopFetch),
		teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})

	tm.Send(tea.KeyPressMsg{Code: 'd'})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: 'a'})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Confirm")
	})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // primary succeeds, cascade fails
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Applied") || strings.Contains(string(bts), "Error")
	})
	tm.Send(tea.KeyPressMsg{Code: 'q'}) // quit while the cascade error is still shown

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))

	result, err := resultFromFinalModel(finalModel)
	require.Error(t, err, "the cascade's error must still be surfaced")
	assert.Equal(t, []string{"abc123"}, result.OpIDs,
		"the primary batch's opID must not be discarded just because a later batch errored")
}
