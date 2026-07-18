package review

import (
	"context"
	"errors"
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
