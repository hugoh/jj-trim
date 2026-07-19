package review_test

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
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared literals across the test cases below: verbDelete/verbAbandon
// match the review.Action.Verb strings bookmarks/commits review actually
// use ("delete"/"abandon"), and chainW/chainA stand in for a resolved
// private-chain revset expression (see classify.PrivateChainRevset).
const (
	verbDelete    = "delete"
	verbAbandon   = "abandon"
	pastAbandoned = "abandoned"
	chainW        = "chain-w"
	chainA        = "chain-a"
	bookmarkVerb  = "bookmark"
	exactW        = `exact:"w"`
)

func testItems(t *testing.T) []review.Item {
	t.Helper()

	return []review.Item{
		{
			IDs:        []string{"w"},
			CascadeIDs: []string{chainW},
			Candidate:  classify.Candidate{ChangeID: "w"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "w",
				Reason:        classify.ReasonNoDescription,
			},
		},
		{
			IDs:        []string{"a"},
			CascadeIDs: []string{chainA},
			Candidate:  classify.Candidate{ChangeID: "a"},
			Legend: classify.LegendEntry{
				ChangeIDShort: "a",
				Reason:        classify.ReasonHasDescription,
			},
		},
	}
}

func noopFetch(context.Context, jj.Runner, classify.Candidate) (string, error) {
	return "context", nil
}

func opLogStdout(id string) (string, string) {
	return jj.Key("op", "log", "--no-graph", "--limit", "1", "-T",
		"self.id().short() ++ \"\\n\""), id + "\n"
}

// opLogSeq is opLogStdout for a batch that includes a cascade step:
// jj.LastOpID is called once after the primary batch, once again right
// before the cascade batch (to snapshot "before"), and once more after it
// (to snapshot "after") — see internal/review's runCascadeBatch. ids
// gives each of those three calls' canned response in order, so a test
// can simulate either a cascade that actually changes something (its
// third id differs from the first two) or one that's a genuine no-op
// (all three ids the same).
func opLogSeq(ids ...string) (string, []string) {
	key, _ := opLogStdout("")

	seq := make([]string, len(ids))
	for i, id := range ids {
		seq[i] = id + "\n"
	}

	return key, seq
}

// cascadeAppliedFake returns a jj.Fake wired for a "w" primary delete
// followed by a cascade abandon whose op-log actually changes ("abc123" ->
// "xyz789"), plus the two `jj op show` fetches the applied popup needs —
// the fixture shared by tests that mark item 0 for cascade and expect it to
// actually apply.
func cascadeAppliedFake() *jj.Fake {
	opLogKey, opLogVals := opLogSeq("abc123", "abc123", "xyz789")

	return &jj.Fake{
		Stdout: map[string]string{
			jj.Key(bookmarkVerb, verbDelete, exactW): "",
			jj.Key(verbAbandon, chainW):              "",
			jj.Key("op", "show", "abc123"):           "",
			jj.Key("op", "show", "xyz789"):           "",
		},
		StdoutSeq: map[string][]string{opLogKey: opLogVals},
	}
}

// deleteAction is the "delete" Action shared by tests that don't care which
// action is under test, just that Apply is/isn't called. Its markKey is
// "d" ("delete"[:1]).
func deleteAction(t *testing.T) review.Action {
	t.Helper()

	return review.Action{Verb: verbDelete, Past: "deleted", Apply: jj.BookmarkDelete}
}

// deleteWithCascadeAction adds an "abandon" CascadeAction (markKey "a") on
// top of deleteAction, matching `bookmarks review`'s real wiring.
func deleteWithCascadeAction(t *testing.T) review.Action {
	t.Helper()
	a := deleteAction(t)
	a.CascadeAction = &review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon}

	return a
}

func abandonAction(t *testing.T) review.Action {
	t.Helper()

	return review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon}
}

// newTestModel builds a teatest harness and waits for the first full
// render. Tests below intentionally avoid asserting on later mid-session
// output: bubbletea's renderer transmits only the diff between frames, so
// a single-character change (e.g. the tally's "0" becoming "1") never
// appears as a contiguous substring in the raw byte stream — only the
// initial full paint and full-screen transitions (list <-> confirm) are
// safe to substring-match. Everything else is verified via WaitFinished
// plus the fake Runner's recorded calls.
func newTestModel(
	t *testing.T,
	fake *jj.Fake,
	items []review.Item,
	action review.Action,
) *teatest.TestModel {
	t.Helper()

	tm := teatest.NewTestModel(
		t,
		review.NewModel(t.Context(), fake, items, action, noopFetch),
		teatest.WithInitialTermSize(100, 30),
	)

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})

	return tm
}

// waitForApplied waits for the screenApplied popup — a batch has just run
// and the session is still alive, waiting for any key to dismiss it back
// to the list (see internal/review/model.go's handleApplied/
// handleAppliedKey). Sessions no longer quit automatically after applying.
func waitForApplied(t *testing.T, tm *teatest.TestModel) {
	t.Helper()

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Applied") || strings.Contains(string(bts), "Error")
	})
}

func markAndConfirm(t *testing.T, tm *teatest.TestModel, keys ...tea.KeyPressMsg) {
	t.Helper()

	for _, k := range keys {
		tm.Send(k)
	}

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Confirm")
	})
}

func confirmApplyAndQuit(t *testing.T, tm *teatest.TestModel) {
	t.Helper()

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForApplied(t, tm)
	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func dismissAndGetOutcome(t *testing.T, tm *teatest.TestModel) (review.Result, error) {
	t.Helper()

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})
	tm.Send(tea.KeyPressMsg{Code: 'q'})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	fs, ok := fm.(review.FinishedSession)
	require.True(t, ok)

	result, err := fs.Outcome()
	if err != nil {
		return result, fmt.Errorf("getting outcome: %w", err)
	}

	return result, nil
}

func singleItem(id string) []review.Item {
	return []review.Item{{
		IDs:       []string{id},
		Candidate: classify.Candidate{ChangeID: id},
		Legend:    classify.LegendEntry{ChangeIDShort: id, Reason: classify.ReasonNoDescription},
	}}
}

func TestReview_MarkAndApply(t *testing.T) {
	t.Parallel()

	key, stdout := opLogStdout("abc123")
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(bookmarkVerb, verbDelete, exactW): "",
			key:                                      stdout,
			jj.Key("op", "show", "abc123"):           "",
		},
	}
	action := deleteAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'd'})

	confirmApplyAndQuit(t, tm)

	// Delete, op-log, and the applied popup's own async `jj op show` fetch.
	require.Len(t, fake.Calls, 3)
	assert.Equal(t, []string{bookmarkVerb, verbDelete, exactW}, fake.Calls[0].Args)
}

// TestReview_CascadeMarkAndApply guards a real defect found in review:
// marking an item with the cascade key used to run ONLY the cascade
// action (abandon its private chain), never the primary action — for a
// candidate whose private chain is empty (e.g. a bookmark that's already
// a literal ancestor of trunk), that meant nothing happened at all: no
// bookmark deletion, no error, the item simply stayed. Cascade now always
// runs the primary action too, so the primary delete call must appear
// first, followed by the cascade abandon call.
func TestReview_CascadeMarkAndApply(t *testing.T) {
	t.Parallel()

	// Three op-log snapshots in order: after the primary delete ("abc123"),
	// before the cascade abandon (still "abc123" — nothing's changed yet),
	// after it ("xyz789" — the abandon actually did something this time).
	fake := cascadeAppliedFake()
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})

	confirmApplyAndQuit(t, tm)

	// Primary delete, its op-log, cascade's before-check op-log, cascade
	// abandon, its after-check op-log, and two `jj op show` fetches (one
	// per distinct op id) for the applied popup.
	require.Len(t, fake.Calls, 7)
	assert.Equal(t, []string{bookmarkVerb, verbDelete, exactW}, fake.Calls[0].Args)
	assert.Equal(t, []string{verbAbandon, chainW}, fake.Calls[3].Args)
}

// TestReview_MixedActionAndCascade_RunsTwoBatches guards the primary
// batch always including cascade-marked items' IDs too — "w" is marked
// delete, "a" is marked cascade, so the primary delete call must cover
// both bookmark names in one batch, and the cascade abandon call must
// still run separately afterward for "a"'s private chain.
func TestReview_MixedActionAndCascade_RunsTwoBatches(t *testing.T) {
	t.Parallel()

	opLogKey, opLogVals := opLogSeq("op-id", "op-id", "op-id-2")

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(bookmarkVerb, verbDelete, exactW, `exact:"a"`): "",
			jj.Key(verbAbandon, chainA):                           "",
			jj.Key("op", "show", "op-id"):                         "",
			jj.Key("op", "show", "op-id-2"):                       "",
		},
		StdoutSeq: map[string][]string{opLogKey: opLogVals},
	}
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(
		t,
		tm,
		tea.KeyPressMsg{Code: 'd'},
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: 'a'},
	)

	confirmApplyAndQuit(t, tm)

	// Primary delete (covering both "w" and "a"), its op-log, cascade's
	// before-check op-log, cascade abandon (for "a"'s private chain only),
	// its after-check op-log, and two `jj op show` fetches for the
	// applied popup.
	require.Len(t, fake.Calls, 7)
	assert.Equal(t, []string{bookmarkVerb, verbDelete, exactW, `exact:"a"`}, fake.Calls[0].Args)
	assert.Equal(t, []string{verbAbandon, chainA}, fake.Calls[3].Args)
}

// TestReview_PrimarySucceedsCascadeFails_KeepsPrimaryResult guards two
// real defects found in review, both around applyCmd/cascade:
//  1. The primary batch must always include cascade-marked items' IDs too
//     (here, item "a"'s bookmark name joins item "w"'s in one delete
//     call) — otherwise a candidate whose private chain is empty (e.g. a
//     merged bookmark) never actually gets its ref deleted, since jj's own
//     "delete the bookmark that pointed at an abandoned commit" behavior
//     never triggers for an empty abandon.
//  2. If that combined primary batch succeeds but the separate cascade
//     abandon batch then fails afterward, the already-obtained opID/
//     Applied entries for BOTH items must survive into the session's
//     result — the bookmark refs are genuinely gone by that point, only
//     the additional private-history cleanup failed.
func TestReview_PrimarySucceedsCascadeFails_KeepsPrimaryResult(t *testing.T) {
	t.Parallel()

	opKey, opOut := opLogStdout("abc123")
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(bookmarkVerb, verbDelete, exactW, `exact:"a"`): "",
			opKey: opOut,
		},
		Errs: map[string]error{
			jj.Key(verbAbandon, chainA): errors.New("boom"),
		},
	}
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(
		t,
		tm,
		tea.KeyPressMsg{Code: 'd'},
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: 'a'},
	)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // primary succeeds (both items), cascade fails
	waitForApplied(t, tm)

	result, err := dismissAndGetOutcome(t, tm)
	require.NoError(t, err, "dismissing must clear the cascade error from the final outcome")
	assert.Equal(
		t,
		[]string{"abc123"},
		result.OpIDs,
		"the primary batch's opID must survive even though the cascade batch failed afterward",
	)
	require.Len(t, result.Applied, 2, "both items' bookmarks were deleted by the primary batch")

	changeIDs := []string{result.Applied[0].ChangeID, result.Applied[1].ChangeID}
	assert.ElementsMatch(t, []string{"w", "a"}, changeIDs)
}

// TestReview_CascadeAbandonNoOp_DoesNotDuplicateOpID guards a real bug
// found in production use: `jj abandon` on an empty revset (the private
// chain for a candidate whose commit is already an ancestor of trunk —
// e.g. a merged bookmark) succeeds without advancing jj's own op log at
// all. runBatch used to call jj.LastOpID unconditionally after every
// non-empty-ids batch, so this genuine no-op still "produced" the
// *previous* (primary) batch's own opID a second time — the applied popup
// showed the same "Undo with: jj op revert X" line twice for what was
// really only ever one operation. Simulated here by making the op-log
// canned response identical before and after the cascade abandon call.
func TestReview_CascadeAbandonNoOp_DoesNotDuplicateOpID(t *testing.T) {
	t.Parallel()

	// Both op-log snapshots around the cascade abandon return the SAME
	// id as the primary batch's — simulating jj not advancing the op log
	// because the abandon had nothing to do.
	opLogKey, opLogVals := opLogSeq("abc123", "abc123", "abc123")
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(bookmarkVerb, verbDelete, exactW): "",
			jj.Key(verbAbandon, chainW):              "", // "no revisions to abandon", per jj's real output
			jj.Key("op", "show", "abc123"):           "",
		},
		StdoutSeq: map[string][]string{opLogKey: opLogVals},
	}
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply
	waitForApplied(t, tm)
	tm.Send(tea.KeyPressMsg{Code: 'q'})

	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	fs, ok := fm.(review.FinishedSession)
	require.True(t, ok)

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"abc123"},
		result.OpIDs,
		"a no-op cascade batch must not add a second (duplicate) entry for the same op id",
	)

	// Primary delete, its op-log, cascade's before-check op-log, the
	// (no-op) abandon call itself, its after-check op-log (same id as
	// before, so no opID is recorded for it), and exactly one `jj op
	// show` fetch — not two — for the applied popup.
	require.Len(t, fake.Calls, 6)
}

func TestReview_Cancel_AppliesNothing(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}
	action := abandonAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	tm.Send(tea.KeyPressMsg{Code: 'a'}) // mark item 0
	tm.Send(tea.KeyPressMsg{Code: 'q'}) // cancel, even though something is marked
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.Empty(t, fake.Calls, "cancel must not apply anything, even if items were marked")
}

// confirmWithNothingMarkedThenQuit sends enter on the confirm screen
// (nothing marked, so it goes straight back to the list — no popup,
// nothing to report, see handleConfirmKey) and then quits.
func confirmWithNothingMarkedThenQuit(t *testing.T, tm *teatest.TestModel) {
	t.Helper()

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestReview_ToggleMarkTwice_Unmarks(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}
	action := deleteAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	// Unmark — this is the "go back and change your mind" navigation the
	// review flow is built around: no separate back-key needed, since
	// pressing the same mark key twice reverts the decision. A mark key
	// advances the cursor, so press up to land back on the same item
	// before pressing it again.
	markAndConfirm(
		t,
		tm,
		tea.KeyPressMsg{Code: 'd'},
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.KeyPressMsg{Code: 'd'},
	)

	confirmWithNothingMarkedThenQuit(t, tm)

	assert.Empty(t, fake.Calls, "toggling mark off then applying must abandon/delete nothing")
}

func TestReview_Unmark_ClearsEitherState(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(
		t,
		tm,
		tea.KeyPressMsg{Code: 'a'},
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.KeyPressMsg{Code: 'u'},
	)

	confirmWithNothingMarkedThenQuit(t, tm)

	assert.Empty(t, fake.Calls, "u must clear the decision regardless of which state it was in")
}

func TestReview_SwitchingMarkKeyChangesState(t *testing.T) {
	t.Parallel()

	fake := cascadeAppliedFake()
	action := deleteWithCascadeAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	markAndConfirm(
		t,
		tm,
		tea.KeyPressMsg{Code: 'd'},
		tea.KeyPressMsg{Code: tea.KeyUp},
		tea.KeyPressMsg{Code: 'a'},
	)

	confirmApplyAndQuit(t, tm)

	// Item 0 ends up cascade-marked (the last key pressed wins) — the
	// primary delete still runs for it (see applyCmd's doc comment), plus
	// the cascade abandon: delete, op-log, cascade's before-check op-log,
	// abandon, its after-check op-log, and two `jj op show` fetches.
	require.Len(t, fake.Calls, 7)
	assert.Equal(t, []string{bookmarkVerb, verbDelete, exactW}, fake.Calls[0].Args,
		"switching to the other mark key must replace, not add to, the decision")
	assert.Equal(t, []string{verbAbandon, chainW}, fake.Calls[3].Args)
}

func TestReview_ConfirmEsc_GoesBackToList(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}
	action := deleteAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm screen with nothing marked
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Confirm")
	})

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape}) // back to list, not a cancel
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.Empty(t, fake.Calls)
}

func TestReview_CommitsShapedSession_HasNoCascadeKey(t *testing.T) {
	t.Parallel()

	// commits review's only action has Verb "abandon" (markKey "a") and no
	// CascadeAction — pressing 'a' marks Action's decision directly,
	// exactly as it does today, since there's no second action to
	// disambiguate against.
	key, stdout := opLogStdout("abc123")
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(verbAbandon, "w"):       "",
			key:                            stdout,
			jj.Key("op", "show", "abc123"): "",
		},
	}
	action := abandonAction(t)

	items := singleItem("w")

	tm := newTestModel(t, fake, items, action)

	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})

	confirmApplyAndQuit(t, tm)

	// Abandon, op-log, and the applied popup's own async `jj op show` fetch.
	require.Len(t, fake.Calls, 3)
	assert.Equal(t, []string{verbAbandon, "w"}, fake.Calls[0].Args)
}

// TestReview_View_AlwaysUsesAltScreen guards against the rendering bug
// found earlier this session: without alt-screen mode, the jump from the
// empty pre-ready frame to the first full frame left a stray line (the
// last list item) uncleared above the header, since the inline
// (non-alt-screen) renderer diffs against the previous frame's line count
// rather than fully redrawing.
func TestReview_View_AlwaysUsesAltScreen(t *testing.T) {
	t.Parallel()

	m := review.NewModel(t.Context(), &jj.Fake{}, testItems(t), deleteAction(t), noopFetch)

	assert.True(t, m.View().AltScreen, "pre-ready empty view must still request alt-screen")

	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	assert.True(t, m.View().AltScreen, "ready view must request alt-screen")
}

// TestReview_NothingMarkedByDefault guards against the earlier "Preseed"
// design, which pre-marked the certain `merged` bucket by default — that
// made the TUI's first screen look like something had already been
// selected before the user acted. review.Item no longer has any way to
// start an item pre-marked; this asserts the confirm screen agrees no
// matter which action wiring is used.
func TestReview_NothingMarkedByDefault(t *testing.T) {
	t.Parallel()

	m := review.NewModel(
		t.Context(),
		&jj.Fake{},
		testItems(t),
		deleteWithCascadeAction(t),
		noopFetch,
	)

	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // list -> confirm, nothing marked

	assert.Contains(t, m.View().Content, "nothing marked")
	assert.Contains(t, m.View().Content, "0 to delete")
	assert.Contains(t, m.View().Content, "0 to abandon")
}

func TestReview_ColliderVerbs_Panics(t *testing.T) {
	t.Parallel()

	action := review.Action{
		Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon,
		CascadeAction: &review.Action{Verb: "archive", Past: "archived", Apply: jj.Abandon},
	}

	assert.Panics(t, func() {
		review.NewModel(t.Context(), &jj.Fake{}, testItems(t), action, noopFetch)
	}, "Action.Verb and CascadeAction.Verb sharing a first letter must fail loudly, not silently collide")
}

// TestReview_ApplyThenContinue_AccumulatesAndPrunes guards the new
// return-to-list-on-apply behavior: a session that applies more than once
// before quitting must accumulate every batch's outcome (not just the
// last), and each successful batch must remove its own items from the
// list rather than leaving them behind to be re-marked.
func TestReview_ApplyThenContinue_AccumulatesAndPrunes(t *testing.T) {
	t.Parallel()

	opKey, opStdout := opLogStdout("abc123")
	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(verbAbandon, "w"): "",
			jj.Key(verbAbandon, "a"): "",
			opKey:                    opStdout,
		},
	}
	action := abandonAction(t)

	tm := newTestModel(t, fake, testItems(t), action)

	// Apply item "w" first.
	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForApplied(t, tm)

	// Dismiss with a non-quit key — the session must still be alive,
	// back on the list screen, not exited.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "jj-trim review")
	})

	// Item "w" is gone now, so item "a" is the sole remaining (and
	// selected) row — mark and apply it too, in the same still-alive
	// session.
	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForApplied(t, tm)

	tm.Send(tea.KeyPressMsg{Code: 'q'})

	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	fs, ok := fm.(review.FinishedSession)
	require.True(t, ok, "final model must implement FinishedSession")

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Len(t, result.Applied, 2, "both batches' outcomes must accumulate, not just the last")
	assert.Len(t, result.OpIDs, 2)
}

func applyErrorTestModel(t *testing.T) *teatest.TestModel {
	t.Helper()

	fake := &jj.Fake{
		Errs: map[string]error{jj.Key(verbAbandon, "w"): errors.New("boom")},
	}
	tm := newTestModel(t, fake, singleItem("w"), abandonAction(t))
	markAndConfirm(t, tm, tea.KeyPressMsg{Code: 'a'})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForApplied(t, tm)

	return tm
}

// TestReview_ApplyError_DismissClearsIt_QuitWhileShownPropagatesIt covers
// both halves of the error-popup lifecycle: dismissing it with a non-quit
// key must leave the failed item untouched (not pruned) and clear the
// error from the session's final outcome, but quitting immediately while
// it's still shown must surface that error to the caller.
func TestReview_ApplyError_DismissClearsIt_QuitWhileShownPropagatesIt(t *testing.T) {
	t.Parallel()

	t.Run("dismiss clears the error and keeps the item", func(t *testing.T) {
		t.Parallel()

		tm := applyErrorTestModel(t)
		result, err := dismissAndGetOutcome(t, tm)
		require.NoError(t, err, "dismissing the error popup must clear it from the final outcome")
		assert.Empty(t, result.Applied, "a failed batch must not be recorded as applied")
	})

	t.Run("quitting while shown propagates the error", func(t *testing.T) {
		t.Parallel()

		tm := applyErrorTestModel(t)
		tm.Send(tea.KeyPressMsg{Code: 'q'}) // quit directly, without dismissing first

		fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
		fs, ok := fm.(review.FinishedSession)
		require.True(t, ok)

		_, err := fs.Outcome()
		require.Error(t, err, "quitting while an error is still shown must surface it")
	})
}

// TestIdle_ReflectsListVsConfirmScreen guards the ChildStatus contract
// internal/browse relies on: Idle() must report true on the list screen and
// false once the session has moved to the confirm screen.
func TestIdle_ReflectsListVsConfirmScreen(t *testing.T) {
	t.Parallel()

	m := review.NewModel(t.Context(), &jj.Fake{}, testItems(t), deleteAction(t), noopFetch)

	cs, ok := m.(review.ChildStatus)
	require.True(t, ok, "model must implement review.ChildStatus")
	assert.True(t, cs.Idle(), "a fresh model on the list screen must be idle")

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	cs, ok = m.(review.ChildStatus)
	require.True(t, ok)
	assert.False(t, cs.Idle(), "the confirm screen must not be idle")
}

// TestNewModelWithResult_SeedsInitialResult guards internal/browse's mode-
// and filters-switch rebuild path: a child built via NewModelWithResult
// must report the seeded Result via Outcome even before anything has been
// applied in the new session.
func TestNewModelWithResult_SeedsInitialResult(t *testing.T) {
	t.Parallel()

	initial := review.Result{OpIDs: []string{"seed-op"}}
	m := review.NewModelWithResult(
		t.Context(), &jj.Fake{}, testItems(t), deleteAction(t), noopFetch, initial,
	)

	fs, ok := m.(review.FinishedSession)
	require.True(t, ok, "model must implement review.FinishedSession")

	result, err := fs.Outcome()
	require.NoError(t, err)
	assert.Equal(t, initial, result, "the seeded Result must survive until something new applies")
}

// TestRun_QuitReturnsZeroResult exercises Run end-to-end over real io
// streams (rather than teatest's synthetic message injection), guarding
// that quitting immediately returns a zero Result and no error.
func TestRun_QuitReturnsZeroResult(t *testing.T) {
	t.Parallel()

	result, err := review.Run(
		t.Context(), &jj.Fake{}, testItems(t), deleteAction(t), noopFetch,
		strings.NewReader("q"), &strings.Builder{},
	)

	require.NoError(t, err)
	assert.Equal(t, review.Result{}, result)
}

// TestRun_ContextCanceled_ReturnsError guards Run's own error path: when the
// tea.Program's context is already cancelled, program.Run() returns an
// error, which Run must wrap and surface rather than silently returning a
// zero Result.
func TestRun_ContextCanceled_ReturnsError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := review.Run(
		ctx, &jj.Fake{}, testItems(t), deleteAction(t), noopFetch,
		strings.NewReader(""), &strings.Builder{},
	)

	require.Error(t, err)
}
