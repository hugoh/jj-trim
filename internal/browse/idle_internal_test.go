package browse

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOpID is the canned op id shared by every test in this package that
// stubs a successful jj op-log lookup after a batch runs.
const testOpID = "abc123"

// verb/past-tense literals shared by this package's white-box tests, so
// there's one spelling of each rather than several copies that could drift.
const (
	testVerbDelete    = "delete"
	testPastDeleted   = "deleted"
	testVerbAbandon   = "abandon"
	testPastAbandoned = "abandoned"
)

// loadedModel drives m past its initial async load by executing Init()'s
// returned command(s) directly (mimicking what a real tea.Program's own
// batch-fanout would do) and feeding the resulting sessionLoadedMsg into
// Update — needed because these white-box tests drive Update directly,
// bypassing the tea.Program that would normally fan out tea.Batch and
// deliver both the load and the spinner's tick independently.
func loadedModel(t *testing.T, m *model) *model {
	t.Helper()

	return driveLoad(t, m, m.Init())
}

// driveLoad is loadedModel's shared mechanism, also used wherever a test
// triggers an in-session reload (toggleMode/applyFilters), both of which
// kick off the same kind of async load as the initial one.
func driveLoad(t *testing.T, m *model, cmd tea.Cmd) *model {
	t.Helper()

	msg := drainForSessionLoaded(t, cmd)

	next, _ := m.Update(msg)
	out, ok := next.(*model)
	require.True(t, ok)
	require.Equal(t, screenChild, out.screen)

	return out
}

// asModel checks and unwraps a tea.Model returned from Update, failing the
// test immediately if it isn't a *model.
func asModel(t *testing.T, m tea.Model) *model {
	t.Helper()

	out, ok := m.(*model)
	require.True(t, ok)

	return out
}

// drainForSessionLoaded executes cmd — and recurses into any tea.BatchMsg
// it yields — until it finds a sessionLoadedMsg. tea.Batch's own returned
// tea.Cmd yields a tea.BatchMsg ([]tea.Cmd) that only a real *tea.Program
// knows how to fan out; this is the manual equivalent for these
// Update-driven-directly tests.
func drainForSessionLoaded(t *testing.T, cmd tea.Cmd) sessionLoadedMsg {
	t.Helper()
	require.NotNil(t, cmd)

	if msg, ok := tryDrainSessionLoaded(cmd); ok {
		return msg
	}

	t.Fatal("sessionLoadedMsg not found among cmd's messages")

	return sessionLoadedMsg{}
}

func tryDrainSessionLoaded(cmd tea.Cmd) (sessionLoadedMsg, bool) {
	if cmd == nil {
		return sessionLoadedMsg{}, false
	}

	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if sm, ok := tryDrainSessionLoaded(c); ok {
				return sm, true
			}
		}
	case sessionLoadedMsg:
		return msg, true
	}

	return sessionLoadedMsg{}, false
}

// deleteOneItemOpts returns Options for a commits session (the default
// starting mode) with exactly one deletable item ("w") and an empty (but
// valid — see toggleMode's markKey() call) bookmarks session — shared setup
// for this file's tests, which all drive the child into a non-idle state
// before exercising browse's chrome-key gating, and some of which toggle
// into bookmarks mode.
func deleteOneItemOpts(jj.Runner) Options {
	item := review.Item{
		IDs:       []string{"w"},
		Candidate: classify.Candidate{ChangeID: "w"},
		Legend:    classify.LegendEntry{ChangeIDShort: "w"},
	}

	return Options{
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (Session, error) {
			return Session{
				Action: review.Action{
					Verb:  testVerbDelete,
					Past:  testPastDeleted,
					Apply: jj.BookmarkDelete,
				},
				Items: []review.Item{item},
				Fetch: func(context.Context, jj.Runner, classify.Candidate) (string, error) {
					return "", nil
				},
			}, nil
		},
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (Session, error) {
			return Session{
				Action: review.Action{
					Verb:  testVerbAbandon,
					Past:  testPastAbandoned,
					Apply: jj.Abandon,
				},
				Fetch: func(context.Context, jj.Runner, classify.Candidate) (string, error) {
					return "", nil
				},
			}, nil
		},
	}
}

// deleteOneItemFake returns a jj.Fake that accepts one bookmark delete
// action ("w") and returns a canned op-log lookup result — shared setup
// for the tests in this package that exercise the full
// mark-confirm-apply-feed cycle.
func deleteOneItemFake(t *testing.T) *jj.Fake {
	t.Helper()

	return &jj.Fake{
		Stdout: map[string]string{
			jj.Key("bookmark", testVerbDelete, "exact:w"): "",
			jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
				"self.id().short() ++ \"\\n\""): testOpID + "\n",
		},
	}
}

// markConfirmApply marks item 0 ("w") for delete on m, advances to the
// confirm screen, applies the batch, and feeds the returned cmd's result
// back into Update — shared by all tests that exercise the full
// apply-and-check-result cycle.
func markConfirmApply(t *testing.T, m *model) *model {
	t.Helper()

	next, _ := m.Update(tea.KeyPressMsg{Code: 'd'})
	m = asModel(t, next)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, next)
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, next)

	require.NotNil(t, cmd)
	next, _ = m.Update(cmd())
	m = asModel(t, next)

	return m
}

// applyDeleteThenDismiss calls markConfirmApply and then dismisses the
// applied popup with Escape — shared by tests that need to keep the model
// on its idle list screen after a batch completes (so they can exercise
// mode-switch or filter-rebuild).
func applyDeleteThenDismiss(t *testing.T, m *model) *model {
	t.Helper()
	m = markConfirmApply(t, m)
	n, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = asModel(t, n)

	return m
}

// TestModel_TabAndFilters_IgnoredWhileChildNotIdle is a white-box (package
// browse, not browse_test) regression test for a real bug found in
// review: handleChildScreenKey used to intercept tab/f unconditionally,
// regardless of what the embedded review child was doing. Pressing tab or
// f while the child had a marked-but-unconfirmed item (on its confirm
// screen) used to rebuild the child from scratch via toggleMode/
// applyFilters, silently discarding the mark. This drives the child onto
// its confirm screen directly (bypassing a real tea.Program) and confirms
// tab/f are now forwarded to the child instead of triggering browse's own
// chrome behavior.
func TestModel_TabAndFilters_IgnoredWhileChildNotIdle(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, deleteOneItemOpts(fake)))

	next, _ := m.Update(tea.KeyPressMsg{Code: 'd'}) // mark item 0 for delete
	m, _ = next.(*model)

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // -> confirm screen
	m, _ = next.(*model)

	cs, ok := m.child.(review.ChildStatus)
	require.True(t, ok, "child must implement review.ChildStatus")
	require.False(t, cs.Idle(), "child must be on its confirm screen, not idle")

	childBefore := m.child

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = next.(*model)

	assert.Equal(t, ModeCommits, m.mode, "tab must not switch mode while the child is confirming")
	assert.Same(t, childBefore, m.child, "the confirming child must not be replaced by tab")

	next, _ = m.Update(tea.KeyPressMsg{Code: 'f'})
	m, _ = next.(*model)

	assert.Equal(
		t,
		screenChild,
		m.screen,
		"f must not open the filters overlay while the child is confirming",
	)
	assert.Same(t, childBefore, m.child, "the confirming child must not be replaced by f")
}

// TestModel_ToggleMode_CarriesForwardAppliedResult is a white-box
// regression test for the other half of the same bug: even once the
// child is idle again (back on its list screen after a successfully
// applied-and-dismissed batch), toggleMode used to rebuild it from
// scratch with a fresh, zero Result — permanently losing that batch's
// record (opID/Applied) from what browse.Run eventually reports, since
// Outcome() is only ever read from whichever child is current when the
// program quits.
func TestModel_ToggleMode_CarriesForwardAppliedResult(t *testing.T) {
	t.Parallel()

	fake := deleteOneItemFake(t)

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, deleteOneItemOpts(fake)))

	m = applyDeleteThenDismiss(t, m)

	cs, ok := m.child.(review.ChildStatus)
	require.True(t, ok)
	require.True(t, cs.Idle(), "child must be idle (back on the list) after dismissing")

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // switch mode (starts an async load)
	m, _ = next.(*model)
	m = driveLoad(t, m, cmd)

	assert.Equal(t, ModeBookmarks, m.mode)

	fs, ok := m.child.(review.FinishedSession)
	require.True(t, ok, "new child must implement review.FinishedSession")

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{testOpID},
		result.OpIDs,
		"the batch applied before switching modes must survive the mode switch",
	)
	assert.Len(t, result.Applied, 1)
}

// TestModel_ApplyFilters_CarriesForwardAppliedResult is
// TestModel_ToggleMode_CarriesForwardAppliedResult for the filters-overlay
// rebuild path (applyFilters), the other call site that used to discard
// an already-applied batch's Result.
func TestModel_ApplyFilters_CarriesForwardAppliedResult(t *testing.T) {
	t.Parallel()

	fake := deleteOneItemFake(t)

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, deleteOneItemOpts(fake)))

	m = applyDeleteThenDismiss(t, m)

	next, _ := m.Update(tea.KeyPressMsg{Code: 'f'}) // open filters (child is idle)
	m, _ = next.(*model)
	require.Equal(t, screenFilters, m.screen)

	next, cmd := m.Update(
		tea.KeyPressMsg{Code: tea.KeyEnter},
	) // save with unchanged fields (starts an async load)
	m, _ = next.(*model)
	m = driveLoad(t, m, cmd)
	require.Equal(t, screenChild, m.screen)

	fs, ok := m.child.(review.FinishedSession)
	require.True(t, ok, "rebuilt child must implement review.FinishedSession")

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{testOpID},
		result.OpIDs,
		"the batch applied before saving filters must survive the rebuild",
	)
	assert.Len(t, result.Applied, 1)
}
