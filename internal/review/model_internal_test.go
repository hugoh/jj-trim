package review

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

	testCascadeChainW = "chain-w"
	testOpID          = "abc123\n"
)

// TestHandleDetailFetched_StaleIndexIgnored guards against a stale
// detailFetchedMsg (issued for an index into a since-shrunk m.items, e.g.
// after pruneApplied) panicking with index out of range — every other
// consumer of a stored item index already bounds-checks it.
func TestHandleDetailFetched_StaleIndexIgnored(t *testing.T) {
	t.Parallel()

	m := newSingleItemModel(t, Action{Verb: testVerbDelete, Past: testPastDeleted})

	require.NotPanics(t, func() {
		_, _ = m.handleDetailFetched(detailFetchedMsg{index: 5, content: "stale"})
	})

	assert.Empty(t, m.detailCache, "a stale out-of-range index must not populate detailCache")
}

func noopFetch(context.Context, jj.Runner, classify.Candidate) (string, error) {
	return "context", nil
}

// itemWithReason builds a single Item for changeID, with IDs set to the same
// value, and the given classify.Reason — the shape shared by most of this
// file's test fixtures, which otherwise only differ in changeID/reason.
func itemWithReason(changeID string, reason classify.Reason) Item {
	return Item{
		IDs:       []string{changeID},
		Candidate: classify.Candidate{ChangeID: changeID},
		Legend: classify.LegendEntry{
			ChangeIDShort: changeID,
			Reason:        reason,
		},
	}
}

// wItem is itemWithReason for the "w"/ReasonNoDescription fixture used by
// most single-item tests in this file.
func wItem() Item {
	return itemWithReason("w", classify.ReasonNoDescription)
}

// newSingleItemModel builds a model over a single wItem() with action,
// the shape shared by most of this file's model constructions.
func newSingleItemModel(t *testing.T, action Action) *model {
	t.Helper()

	return newModel(t.Context(), &jj.Fake{}, []Item{wItem()}, action, noopFetch)
}

// newNilItemModel builds an itemless model over r with the shared
// testVerbDelete action — the shape shared by TestRunBatch's subtests and
// TestRunCascadeBatch_*, which only exercise runBatch/runCascadeBatch
// directly and don't need any actual Items.
func newNilItemModel(t *testing.T, r jj.Runner) *model {
	t.Helper()

	return newModel(
		t.Context(), r, nil, Action{Verb: testVerbDelete, Past: testPastDeleted}, noopFetch,
	)
}

// fillLines joins n lines of the form "<prefix> <i>", long enough to
// overflow a small viewport's height for scroll tests.
func fillLines(n int, prefix string) string {
	lines := make([]string, 0, n)
	for i := range n {
		lines = append(lines, fmt.Sprintf("%s %d", prefix, i))
	}

	return strings.Join(lines, "\n")
}

// TestReviewItem_FilterValue guards list.Item's FilterValue implementation:
// it must delegate to the Legend's own String rendering, so filtering (if
// ever re-enabled) matches what's actually shown on screen.
func TestReviewItem_FilterValue(t *testing.T) {
	t.Parallel()

	ri := reviewItem{Item: wItem()}

	assert.Equal(t, ri.Legend.String(), ri.FilterValue())
}

// detailScrollTestModel builds a ready (window-sized) model on screenList
// with m.detail seeded with enough lines to overflow its pane height, so
// scroll keys have visible room to move. The window is sized small (a
// handful of rows) so the detail pane's height is small too, keeping the
// half-page-vs-line-scroll distinction meaningful without needing a huge
// content block.
func detailScrollTestModel(t *testing.T) *model {
	t.Helper()

	m := newSingleItemModel(t, Action{Verb: testVerbDelete, Past: testPastDeleted})

	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.detail.SetContent(fillLines(100, "line"))

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

	items := []Item{wItem(), itemWithReason("a", classify.ReasonHasDescription)}

	m := newModel(
		t.Context(),
		&jj.Fake{},
		items,
		Action{Verb: testVerbDelete, Past: testPastDeleted},
		noopFetch,
	)
	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.detail.SetContent(fillLines(100, "line"))

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

// TestHandleBackgroundColor_UpdatesThemeAndRebuildsDelegate guards that
// learning the terminal's actual background (Init's tea.RequestBackgroundColor)
// both updates hasDarkBG and rebuilds the list delegate with it, so item
// rendering picks up the adaptive colors rather than staying stuck on the
// default-true assumption.
func TestHandleBackgroundColor_UpdatesThemeAndRebuildsDelegate(t *testing.T) {
	t.Parallel()

	m := newSingleItemModel(t, deleteWithCascade())
	require.True(t, m.hasDarkBG, "model must default to dark before learning the real theme")

	_, cmd := m.handleBackgroundColor(tea.BackgroundColorMsg{Color: color.White})

	assert.Nil(t, cmd)
	assert.False(t, m.hasDarkBG, "white is a light background")
}

// deleteWithCascade is a delete Action with an abandon CascadeAction, shared
// by this file's tests that need a non-empty cascadeLetter.
func deleteWithCascade() Action {
	return Action{
		Verb: testVerbDelete, Past: testPastDeleted, Apply: jj.BookmarkDelete,
		CascadeAction: &Action{Verb: testVerbAbandon, Past: testPastAbandoned, Apply: jj.Abandon},
	}
}

// appliedScreenModel builds a model already sitting on screenApplied after a
// successful batch that produced opIDs — the state showingOpLog() (and so
// handleAppliedKey's pager-forwarding branch) requires.
func appliedScreenModel(t *testing.T) *model {
	t.Helper()

	m := newSingleItemModel(t, Action{Verb: testVerbDelete, Past: testPastDeleted})
	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})

	m.screen = screenApplied
	m.lastBatch = appliedMsg{result: Result{OpIDs: []string{"abc123"}}}
	m.opLog.SetContent(fillLines(50, "op log line"))

	return m
}

// TestHandleAppliedKey_ForwardsScrollKeysToOpLogPager guards handleAppliedKey's
// showingOpLog branch: with a pager on screen, a non-dismiss key (anything
// but enter/esc) must be forwarded to the op log viewport as scroll input
// instead of dismissing the popup.
func TestHandleAppliedKey_ForwardsScrollKeysToOpLogPager(t *testing.T) {
	t.Parallel()

	m := appliedScreenModel(t)
	require.True(t, m.showingOpLog())
	require.Equal(t, 0, m.opLog.YOffset())

	next, _ := m.handleAppliedKey(tea.KeyPressMsg{Code: tea.KeyDown})
	out, ok := next.(*model)
	require.True(t, ok)

	assert.Equal(t, screenApplied, out.screen, "a scroll key must not dismiss the popup")
	assert.Positive(t, out.opLog.YOffset(), "the key must have scrolled the op log pager")
}

// TestHandleAppliedKey_EnterDismissesEvenWithPagerShowing guards the other
// half of handleAppliedKey's branch: enter/esc dismiss the popup even while
// the pager is showing, rather than being forwarded to it.
func TestHandleAppliedKey_EnterDismissesEvenWithPagerShowing(t *testing.T) {
	t.Parallel()

	m := appliedScreenModel(t)
	require.True(t, m.showingOpLog())

	next, _ := m.handleAppliedKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	out, ok := next.(*model)
	require.True(t, ok)

	assert.Equal(
		t,
		screenList,
		out.screen,
		"enter must dismiss the popup even with a pager showing",
	)
}

// TestRunBatch covers runBatch's three outcomes: a no-op for empty ids, a
// wrapped error when Action.Apply itself fails, and a wrapped error when the
// resulting-opID lookup fails after Apply otherwise succeeded.
func TestRunBatch(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	opKey := jj.Key("op", "log", "--no-graph", "--limit", "1", "-T", "self.id().short() ++ \"\\n\"")

	t.Run("empty ids is a no-op", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		m := newNilItemModel(t, &jj.Fake{})
		applyCalled := false
		action := Action{
			Verb: testVerbDelete, Past: testPastDeleted,
			Apply: func(context.Context, jj.Runner, []string) error {
				applyCalled = true

				return nil
			},
		}

		opID, err := m.runBatch(action, nil)

		require.NoError(t, err)
		assert.Empty(opID)
		assert.False(applyCalled, "Apply must not run for an empty ids batch")
	})

	t.Run("apply error is wrapped", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		m := newNilItemModel(t, &jj.Fake{})
		action := Action{
			Verb: testVerbDelete, Past: testPastDeleted,
			Apply: func(context.Context, jj.Runner, []string) error { return boom },
		}

		opID, err := m.runBatch(action, []string{"w"})

		require.ErrorIs(t, err, boom)
		assert.Empty(opID)
	})

	t.Run("opID lookup error after a successful apply is wrapped", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		fake := &jj.Fake{Errs: map[string]error{opKey: boom}}
		m := newNilItemModel(t, fake)
		action := Action{
			Verb: testVerbDelete, Past: testPastDeleted,
			Apply: func(context.Context, jj.Runner, []string) error { return nil },
		}

		opID, err := m.runBatch(action, []string{"w"})

		require.ErrorIs(t, err, boom)
		assert.Empty(opID)
	})
}

// TestRunCascadeBatch_BeforeLookupError_IsWrapped guards runCascadeBatch's
// first jj.LastOpID call (the "before" snapshot): if that lookup itself
// fails, the error must surface immediately, before action.Apply is ever
// called.
func TestRunCascadeBatch_BeforeLookupError_IsWrapped(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
				"self.id().short() ++ \"\\n\""): boom,
		},
	}
	m := newNilItemModel(t, fake)

	applyCalled := false
	action := Action{
		Verb: testVerbAbandon, Past: testPastAbandoned,
		Apply: func(context.Context, jj.Runner, []string) error {
			applyCalled = true

			return nil
		},
	}

	opID, err := m.runCascadeBatch(action, []string{testCascadeChainW})

	require.ErrorIs(t, err, boom)
	assert.Empty(t, opID)
	assert.False(t, applyCalled, "Apply must not run if the before-snapshot lookup fails")
}

// TestRunCascadeBatch_ApplyError_IsWrapped guards runCascadeBatch's own
// action.Apply failure path, once the before-snapshot succeeded.
func TestRunCascadeBatch_ApplyError_IsWrapped(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	opKey := jj.Key("op", "log", "--no-graph", "--limit", "1", "-T", "self.id().short() ++ \"\\n\"")
	fake := &jj.Fake{Stdout: map[string]string{opKey: testOpID}}
	m := newNilItemModel(t, fake)

	action := Action{
		Verb: testVerbAbandon, Past: testPastAbandoned,
		Apply: func(context.Context, jj.Runner, []string) error { return boom },
	}

	opID, err := m.runCascadeBatch(action, []string{testCascadeChainW})

	require.ErrorIs(t, err, boom)
	assert.Empty(t, opID)
}

// TestRunCascadeBatch_AfterLookupError_IsWrapped guards runCascadeBatch's
// second jj.LastOpID call (the "after" snapshot, taken once action.Apply has
// otherwise succeeded): if that lookup fails, the error must surface rather
// than the batch being reported as a silent no-op.
func TestRunCascadeBatch_AfterLookupError_IsWrapped(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	opKey := jj.Key("op", "log", "--no-graph", "--limit", "1", "-T", "self.id().short() ++ \"\\n\"")
	fake := &jj.Fake{Stdout: map[string]string{opKey: testOpID}}
	m := newNilItemModel(t, &countingAfterErrorFake{Fake: fake, errAfter: 1, err: boom})

	action := Action{
		Verb: testVerbAbandon, Past: testPastAbandoned,
		Apply: func(context.Context, jj.Runner, []string) error { return nil },
	}

	opID, err := m.runCascadeBatch(action, []string{testCascadeChainW})

	require.ErrorIs(t, err, boom)
	assert.Empty(t, opID)
}

// countingAfterErrorFake wraps a *jj.Fake and, once it has already served
// errAfter successful jj.LastOpID calls, errors every subsequent one — used
// to distinguish runCascadeBatch's "before" (must succeed) and "after" (must
// fail) jj.LastOpID calls, which are otherwise indistinguishable jj.Fake
// canned-response keys.
type countingAfterErrorFake struct {
	*jj.Fake

	errAfter int
	err      error
	opCalls  int
}

func (f *countingAfterErrorFake) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "op" {
		f.opCalls++
		if f.opCalls > f.errAfter {
			return "", f.err
		}
	}

	out, err := f.Fake.Run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("countingAfterErrorFake: %w", err)
	}

	return out, nil
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
			jj.Key("bookmark", testVerbDelete, `exact:"w"`, `exact:"a"`): "",
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
			}, CascadeIDs: []string{testCascadeChainW}, Candidate: classify.Candidate{ChangeID: "w"},
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

// stubTeaModel is a bare tea.Model that is not *model — used to drive
// resultFromFinalModel's fallback branch for a finalModel of the wrong
// type, which program.Run() never actually produces in practice (it always
// returns the same concrete type it was started with) but which
// resultFromFinalModel still guards defensively since it takes a plain
// tea.Model.
type stubTeaModel struct{}

func (stubTeaModel) Init() tea.Cmd                       { return nil }
func (stubTeaModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return stubTeaModel{}, nil }
func (stubTeaModel) View() tea.View                      { return tea.View{} }

func TestResultFromFinalModel_WrongType_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	result, err := resultFromFinalModel(stubTeaModel{})

	require.NoError(t, err)
	assert.Equal(t, Result{}, result)
}

func TestListView_FitsNarrowTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct{ width, height int }{
		{width: 20, height: 12},
		{width: 40, height: 15},
		{width: 80, height: 24},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dx%d", tt.width, tt.height), func(t *testing.T) {
			t.Parallel()

			longID := strings.Repeat("longchangeid", 8)
			m := newModel(
				t.Context(), &jj.Fake{},
				[]Item{itemWithReason(longID, classify.ReasonNoDescription), wItem()},
				deleteWithCascade(), noopFetch,
			)
			_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: tt.width, Height: tt.height})

			lines := strings.Split(m.listView(), "\n")

			require.LessOrEqual(t, len(lines), tt.height)

			for _, line := range lines {
				require.LessOrEqual(t, lipgloss.Width(line), tt.width, "line %q", line)
			}
		})
	}
}

func manyItemsModel(t *testing.T, n, width, height int) *model {
	t.Helper()

	items := make([]Item, 0, n)
	for i := range n {
		items = append(
			items,
			itemWithReason(fmt.Sprintf("id%03d", i), classify.ReasonNoDescription),
		)
	}

	m := newModel(t.Context(), &jj.Fake{}, items, deleteWithCascade(), noopFetch)
	_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: width, Height: height})

	return m
}

func TestConfirmView_BoundedAndScrollable(t *testing.T) {
	t.Parallel()

	const height = 12

	m := manyItemsModel(t, 50, 60, height)

	for range 50 {
		_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	}

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, screenConfirm, m.screen)

	view := m.confirmView()
	lines := strings.Split(view, "\n")

	assert.LessOrEqual(t, len(lines), height)
	assert.Contains(t, lines[len(lines)-1], "enter=confirm", "footer must stay visible")
	assert.Contains(t, view, "id000")
	assert.NotContains(t, view, "id049", "off-screen items are scrolled out, not rendered")

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})

	assert.NotContains(t, m.confirmView(), "id000", "page down scrolls the item list")
}

func TestView_TooSmallTerminalShowsMessage(t *testing.T) {
	t.Parallel()

	m := manyItemsModel(t, 3, 20, 5)

	assert.Contains(t, m.View().Content, "too small")
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}

	_, ok := cmd().(tea.QuitMsg)

	return ok
}

func TestListQuit_WithMarksNeedsSecondPress(t *testing.T) {
	t.Parallel()

	quitKeys := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "q", key: tea.KeyPressMsg{Code: 'q', Text: "q"}},
		{name: "esc", key: tea.KeyPressMsg{Code: tea.KeyEscape}},
	}

	for _, qk := range quitKeys {
		t.Run(qk.name+" with nothing marked quits at once", func(t *testing.T) {
			t.Parallel()

			m := manyItemsModel(t, 2, 80, 24)

			_, cmd := m.handleKey(qk.key)

			assert.True(t, isQuit(cmd))
		})

		t.Run(qk.name+" with marks warns then quits", func(t *testing.T) {
			t.Parallel()

			m := manyItemsModel(t, 2, 80, 24)
			_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})

			_, cmd := m.handleKey(qk.key)
			assert.False(t, isQuit(cmd), "first press must not quit")
			assert.Contains(t, m.tally(), "again to discard")

			_, cmd = m.handleKey(qk.key)
			assert.True(t, isQuit(cmd), "second press quits")
		})

		t.Run(qk.name+" warning is disarmed by another key", func(t *testing.T) {
			t.Parallel()

			m := manyItemsModel(t, 2, 80, 24)
			_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
			_, _ = m.handleKey(qk.key)
			_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})

			assert.NotContains(t, m.tally(), "again to discard")

			_, cmd := m.handleKey(qk.key)
			assert.False(t, isQuit(cmd), "must warn again after being disarmed")
		})
	}

	t.Run("ctrl+c always quits immediately", func(t *testing.T) {
		t.Parallel()

		m := manyItemsModel(t, 2, 80, 24)
		_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})

		_, cmd := m.handleKey(ctrlKey('c'))

		assert.True(t, isQuit(cmd))
	})
}

func TestNewModel_ReservedMarkKeyPanics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action Action
	}{
		{
			name:   "primary verb starts with q",
			action: Action{Verb: "quarantine", Past: "quarantined"},
		},
		{name: "primary verb starts with u", action: Action{Verb: "undo", Past: "undone"}},
		{
			name: "cascade verb starts with u",
			action: Action{
				Verb: testVerbDelete, Past: testPastDeleted,
				CascadeAction: &Action{Verb: "unlink", Past: "unlinked"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Panics(t, func() {
				newModel(t.Context(), &jj.Fake{}, nil, tt.action, noopFetch)
			})
		})
	}
}

func TestTally_HelpShrinksToWidth(t *testing.T) {
	t.Parallel()

	wide := manyItemsModel(t, 2, 200, 24)
	narrow := manyItemsModel(t, 2, 60, 24)

	assert.Contains(t, wide.tally(), "ctrl+u/d")
	assert.NotContains(t, narrow.tally(), "ctrl+u/d")
	assert.LessOrEqual(t, lipgloss.Width(narrow.tally()), 60)
}

func wheelDown() tea.MouseWheelMsg { return tea.MouseWheelMsg{Button: tea.MouseWheelDown} }

func TestMouseWheel_ScrollsTheActivePane(t *testing.T) {
	t.Parallel()

	t.Run("list screen scrolls the detail pane", func(t *testing.T) {
		t.Parallel()

		m := detailScrollTestModel(t)
		before := m.list.Index()

		_, _ = m.Update(wheelDown())

		assert.Positive(t, m.detail.YOffset())
		assert.Equal(t, before, m.list.Index())
	})

	t.Run("confirm screen scrolls the item list", func(t *testing.T) {
		t.Parallel()

		m := manyItemsModel(t, 50, 60, 12)
		for range 50 {
			_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
		}

		_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		_, _ = m.Update(wheelDown())

		assert.Positive(t, m.confirm.YOffset())
	})

	t.Run("applied screen scrolls the op log", func(t *testing.T) {
		t.Parallel()

		m := appliedScreenModel(t)
		m.opLog.SetContent(fillLines(100, "op"))

		_, _ = m.Update(wheelDown())

		assert.Positive(t, m.opLog.YOffset())
	})
}

func helpKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: '?', Text: "?"} }

func TestHelpOverlay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model func(t *testing.T) *model
		want  []string
	}{
		{
			name: "list screen",
			model: func(t *testing.T) *model {
				t.Helper()

				return manyItemsModel(t, 3, 80, 24)
			},
			want: []string{
				testVerbDelete,
				testVerbAbandon,
				"unmark",
				"move",
				"half page",
				"cancel",
			},
		},
		{
			name: "confirm screen",
			model: func(t *testing.T) *model {
				t.Helper()

				m := manyItemsModel(t, 3, 80, 24)
				_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

				return m
			},
			want: []string{"apply", "back", "scroll"},
		},
		{
			name: "applied screen",
			model: func(t *testing.T) *model {
				t.Helper()

				return appliedScreenModel(t)
			},
			want: []string{"continue", "scroll"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := tt.model(t)
			_, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 80, Height: 24})
			before := m.screen

			_, _ = m.handleKey(helpKey())

			view := m.View().Content
			require.Contains(t, view, "Help")

			for _, w := range tt.want {
				assert.Contains(t, view, w)
			}

			_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
			assert.False(t, isQuit(cmd), "the dismissing key is consumed, not acted on")
			assert.Equal(t, before, m.screen)
			assert.NotContains(t, m.View().Content, "press any key to close")

			_, _ = m.handleKey(helpKey())
			_, cmd = m.handleKey(ctrlKey('c'))
			assert.True(t, isQuit(cmd), "ctrl+c still quits with help open")
		})
	}
}

func TestHelpOverlay_DismissKeyDoesNotMark(t *testing.T) {
	t.Parallel()

	m := manyItemsModel(t, 3, 80, 24)
	_, _ = m.handleKey(helpKey())
	_, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Text: "d"})

	marked, cascade := m.markedItems()
	assert.Empty(t, marked)
	assert.Empty(t, cascade)
}

func TestHelpOverlay_NotIdle(t *testing.T) {
	t.Parallel()

	m := manyItemsModel(t, 3, 80, 24)
	require.True(t, m.Idle())

	_, _ = m.handleKey(helpKey())

	assert.False(t, m.Idle(), "an embedding model must not treat the overlay as idle")
}

func TestHelpOverlay_ShowsExtraBindings(t *testing.T) {
	t.Parallel()

	m := manyItemsModel(t, 3, 80, 24)
	m.AddHelp(key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch mode")))
	_, _ = m.handleKey(helpKey())

	assert.Contains(t, m.View().Content, "switch mode")
}

func TestTally_AlwaysAdvertisesHelp(t *testing.T) {
	t.Parallel()

	for _, width := range []int{40, 60, 80, 200} {
		m := manyItemsModel(t, 2, width, 24)

		assert.Contains(t, ansi.Strip(m.tally()), "? help", "width %d", width)
		assert.LessOrEqual(t, lipgloss.Width(m.tally()), width)
	}
}
