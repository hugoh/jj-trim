package browse

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModel_ChildOutcome_RecoverableAfterApply is a white-box (package
// browse, not browse_test) regression test for a real bug found this
// session: browse owns the actual top-level tea.Program, embedding
// review's model as a child, so browse.Run's finalModel is always browse's
// own *model — a naive `fm.result` read (the way review.Run itself reads
// its own final model) would always be the zero value, silently dropping
// the outcome of every ordinary mark-and-confirm-and-apply session reached
// through the TUI. This drives the child's Update calls directly (bypassing
// a real tea.Program) to confirm review.FinishedSession recovers the
// child's actual Result once it has quit.
func TestModel_ChildOutcome_RecoverableAfterApply(t *testing.T) {
	t.Parallel()

	fake := deleteOneItemFake(t)

	opts := deleteOneItemOpts(fake)

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, opts))

	m = markConfirmApply(t, m)

	fs, ok := m.child.(review.FinishedSession)
	require.True(t, ok, "child must implement review.FinishedSession")

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Equal(t, []string{testOpID}, result.OpIDs)
	assert.Len(t, result.Applied, 1)
}

// TestResultFromFinishedModel_ReloadFailureKeepsPendingCarry guards a real
// defect: browse.Run used to return an empty Result whenever fm.err was set
// (a mode/filters reload failure), discarding whatever batch the outgoing
// child had already applied and stashed in m.pendingCarry before the
// reload started.
func TestResultFromFinishedModel_ReloadFailureKeepsPendingCarry(t *testing.T) {
	t.Parallel()

	fake := deleteOneItemFake(t)

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, deleteOneItemOpts(fake)))
	m = applyDeleteThenDismiss(t, m)

	next, cmd := m.Update(
		tea.KeyPressMsg{Code: tea.KeyTab},
	) // starts an async reload, stashing pendingCarry
	m = asModel(t, next)
	require.NotNil(t, cmd)
	require.Equal(t, []string{testOpID}, m.pendingCarry.OpIDs)

	reloadErr := errors.New("boom")
	next, _ = m.Update(sessionLoadedMsg{mode: ModeBookmarks, err: reloadErr})
	m = asModel(t, next)

	result, err := resultFromFinishedModel(m)
	require.ErrorIs(t, err, reloadErr)
	assert.Equal(
		t,
		[]string{testOpID},
		result.OpIDs,
		"the batch applied before the failed reload must still be reported",
	)
}

// TestResultFromFinishedModel_ChildErrorKeepsResult guards the other real
// defect in the same area: browse.Run used to discard the child's Result
// whenever fs.Outcome() returned a pending error (e.g. a cascade batch
// failing after the primary batch already succeeded).
func TestResultFromFinishedModel_ChildErrorKeepsResult(t *testing.T) {
	t.Parallel()

	items := []review.Item{
		{
			IDs:       []string{"w"},
			Candidate: classify.Candidate{ChangeID: "w"},
			Legend:    classify.LegendEntry{ChangeIDShort: "w"},
		},
		{
			IDs: []string{"a"}, CascadeIDs: []string{"chain-a"},
			Candidate: classify.Candidate{
				ChangeID: "a",
			}, Legend: classify.LegendEntry{ChangeIDShort: "a"},
		},
	}
	opts := Options{
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (Session, error) {
			return Session{
				Action: review.Action{
					Verb: testVerbDelete, Past: testPastDeleted, Apply: jj.BookmarkDelete,
					CascadeAction: &review.Action{
						Verb:  testVerbAbandon,
						Past:  testPastAbandoned,
						Apply: jj.Abandon,
					},
				},
				Items: items,
				Fetch: func(context.Context, jj.Runner, classify.Candidate) (string, error) { return "", nil },
			}, nil
		},
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (Session, error) {
			return Session{
				Action: review.Action{
					Verb:  testVerbAbandon,
					Past:  testPastAbandoned,
					Apply: jj.Abandon,
				},
			}, nil
		},
	}
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("bookmark", testVerbDelete, "exact:w", "exact:a"): "",
			jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
				"self.id().short() ++ \"\\n\""): testOpID + "\n",
		},
		Errs: map[string]error{jj.Key(testVerbAbandon, "chain-a"): errors.New("boom")},
	}

	m := loadedModel(t, newModel(t.Context(), fake, trimconfig.Config{}, opts))

	next, _ := m.Update(tea.KeyPressMsg{Code: 'd'}) // mark item 0 ("w") for primary delete
	m = asModel(t, next)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = asModel(t, next)
	next, _ = m.Update(tea.KeyPressMsg{Code: 'a'}) // mark item 1 ("a") for cascade abandon
	m = asModel(t, next)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // -> confirm
	m = asModel(t, next)
	next, cmd := m.Update(
		tea.KeyPressMsg{Code: tea.KeyEnter},
	) // apply: primary succeeds, cascade fails
	m = asModel(t, next)
	require.NotNil(t, cmd)
	next, _ = m.Update(cmd())
	m = asModel(t, next)

	result, err := resultFromFinishedModel(m)
	require.Error(t, err, "the cascade's error must still be surfaced")
	assert.Equal(
		t,
		[]string{testOpID},
		result.OpIDs,
		"the primary batch's opID must not be discarded just because the cascade batch errored",
	)
}
