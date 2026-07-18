package browse_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/hugoh/jj-trim/internal/browse"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	verbDelete    = "delete"
	pastDeleted   = "deleted"
	verbAbandon   = "abandon"
	pastAbandoned = "abandoned"
)

func noopFetch(context.Context, jj.Runner, classify.Candidate) (string, error) {
	return "context", nil
}

// bookmarksItem stays parameterized to mirror commitsItem's shape, even
// though every current caller happens to pass "w".
//

func bookmarksItem(id string) review.Item {
	return review.Item{
		IDs:       []string{id},
		Candidate: classify.Candidate{ChangeID: id},
		Legend:    classify.LegendEntry{ChangeIDShort: id, Reason: classify.ReasonMerged},
	}
}

func commitsItem(id string) review.Item {
	return review.Item{
		IDs:       []string{id},
		Candidate: classify.Candidate{ChangeID: id},
		Legend:    classify.LegendEntry{ChangeIDShort: id, Reason: classify.ReasonNoDescription},
	}
}

// testOptions returns Builder calls recorded on the returned *callLog, so
// tests can assert which mode was actually built without depending on
// internal/browse's own state.
type callLog struct {
	bookmarksBuilds int
	commitsBuilds   int
}

func testOptions(log *callLog) browse.Options {
	return browse.Options{
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			log.bookmarksBuilds++

			return browse.Session{
				Action: review.Action{
					Verb:  verbDelete,
					Past:  pastDeleted,
					Apply: jj.BookmarkDelete,
					CascadeAction: &review.Action{
						Verb:  verbAbandon,
						Past:  pastAbandoned,
						Apply: jj.Abandon,
					},
				},
				Items: []review.Item{bookmarksItem("w")},
				Fetch: noopFetch,
			}, nil
		},
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			log.commitsBuilds++

			return browse.Session{
				Action: review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
				Items:  []review.Item{commitsItem("c")},
				Fetch:  noopFetch,
			}, nil
		},
	}
}

func newTestModelWaitingFor(t *testing.T, opts browse.Options, wait string) *teatest.TestModel {
	t.Helper()

	m := browse.NewModel(t.Context(), &jj.Fake{}, trimconfig.Config{}, opts)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), wait)
	})

	return tm
}

func newTestModel(t *testing.T, opts browse.Options) *teatest.TestModel {
	t.Helper()

	return newTestModelWaitingFor(t, opts, "Bookmarks")
}

func bookmarksSession() browse.Session {
	return browse.Session{
		Action: review.Action{
			Verb:  verbDelete,
			Past:  pastDeleted,
			Apply: jj.BookmarkDelete,
		},
		Items: []review.Item{bookmarksItem("w")},
		Fetch: noopFetch,
	}
}

func commitsSession() browse.Session {
	return browse.Session{
		Action: review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
		Items:  []review.Item{commitsItem("c")},
		Fetch:  noopFetch,
	}
}

func TestBrowse_OpensDirectlyIntoCommitsMode_NoGate(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	tm := newTestModel(t, testOptions(log))

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.Equal(
		t,
		1,
		log.commitsBuilds,
		"commits mode must be built up front, no settings gate first",
	)
	assert.Equal(t, 0, log.bookmarksBuilds)
}

func TestBrowse_TabTogglesMode(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	tm := newTestModel(t, testOptions(log))

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		// "[Bookmarks]" (the active-tab marker), not plain "Bookmarks" — the
		// tab bar always shows both mode labels, so the inactive one is
		// already present in the very first (commits-mode) frame.
		return strings.Contains(string(bts), "[Bookmarks]")
	})

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab}) // back to commits
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		// "[Commits]" (the active-tab marker) rather than plain "Commits",
		// which is already present in the tab bar's inactive label while
		// still in Bookmarks mode.
		return strings.Contains(string(bts), "[Commits]")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.Equal(t, 2, log.commitsBuilds, "initial build + toggling back")
	assert.Equal(t, 1, log.bookmarksBuilds)
}

// TestBrowse_FiltersOverlay_StaleAfterPlaceholder_ShowsFullDefault guards a
// real bug found this session: textinput.Model sizes its placeholder
// rendering to Width()+1 runes, and the fields here never called
// SetWidth, so the default Width()==0 truncated the "2160h" placeholder
// down to just its first rune ("2") — read on screen as "Stale after: > 2"
// (the "> " being textinput's own default Prompt, stacked confusingly on
// top of this form's own focus-indicator prefix, also fixed alongside).
func TestBrowse_FiltersOverlay_StaleAfterPlaceholder_ShowsFullDefault(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	tm := newTestModel(t, testOptions(log))

	var out string

	// Bookmarks mode's filters form is the one with Trunk/Protected/Stale
	// after fields — Commits mode's is a single bool toggle.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "[Bookmarks]")
	})

	tm.Send(tea.KeyPressMsg{Code: 'f'})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		out = string(bts)

		return strings.Contains(out, "2160h")
	})

	assert.NotContains(t, out, "> 2\n", "placeholder must not be truncated to its first rune")
	assert.NotContains(
		t,
		out,
		">> ",
		"textinput's own default prompt must not stack with the focus prefix",
	)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestBrowse_FiltersOverlay_EditTrunkReclassifiesOnSave(t *testing.T) {
	t.Parallel()

	var sawTrunk string

	opts := browse.Options{
		Bookmarks: func(_ context.Context, _ jj.Runner, cfg trimconfig.Config) (browse.Session, error) {
			sawTrunk = cfg.Trunk

			return bookmarksSession(), nil
		},
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{
				Action: review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
				Fetch:  noopFetch,
			}, nil
		},
	}

	tm := newTestModel(t, opts)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab}) // this test edits Trunk, a bookmarks-only field
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "[Bookmarks]")
	})

	tm.Send(tea.KeyPressMsg{Code: 'f'})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Filters")
	})

	for _, r := range "custom_trunk()" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // save, returns to browse screen
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Bookmarks")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.Equal(t, "custom_trunk()", sawTrunk)
}

// TestBrowse_FiltersOverlay_CommitsToggle_SpaceKeyWorks guards a real bug
// found in review: bubbletea's Key.String() spells the spacebar "space",
// not " " — a handler matching on the literal " " string never fires for
// an actual spacebar press.
func TestBrowse_FiltersOverlay_CommitsToggle_SpaceKeyWorks(t *testing.T) {
	t.Parallel()

	var sawNoDescriptionOnly bool

	opts := browse.Options{
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{
				Action: review.Action{
					Verb:  verbDelete,
					Past:  pastDeleted,
					Apply: jj.BookmarkDelete,
				},
				Fetch: noopFetch,
			}, nil
		},
		Commits: func(_ context.Context, _ jj.Runner, cfg trimconfig.Config) (browse.Session, error) {
			sawNoDescriptionOnly = cfg.NoDescriptionOnly

			return browse.Session{
				Action: review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
				Items:  []review.Item{commitsItem("c")},
				Fetch:  noopFetch,
			}, nil
		},
	}

	tm := newTestModel(t, opts) // commits is the default starting mode

	tm.Send(tea.KeyPressMsg{Code: 'f'})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Filters")
	})

	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // save
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Commits")
	}, teatest.WithDuration(20*time.Second))

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	assert.True(t, sawNoDescriptionOnly, "space must toggle the no-description-only filter")
}

func TestBrowse_FiltersOverlay_InvalidStaleAfter_StaysOnScreenWithError(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	tm := newTestModel(t, testOptions(log))

	// This test edits the Stale after field, which only exists in
	// Bookmarks mode's filters form.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "[Bookmarks]")
	})

	tm.Send(tea.KeyPressMsg{Code: 'f'})
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Filters")
	})

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab}) // Trunk -> Protected
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab}) // Protected -> Stale after

	for _, r := range "not-a-duration" {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	buildsBefore := log.bookmarksBuilds

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter}) // save attempt should fail to parse
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "error:")
	})

	assert.Equal(t, buildsBefore, log.bookmarksBuilds, "an invalid form must not reclassify")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestBrowse_BuilderError_QuitsWithError guards a builder failure (e.g. an
// unparsable --trunk revset) surfacing as an error from Run. Construction
// itself can no longer fail synchronously — the initial candidate load
// happens asynchronously after Init() (see screenLoading/loadSessionCmd),
// so this goes through Run (which drives a real, if headless, tea.Program
// to completion) rather than asserting on NewModel's return directly.
func TestBrowse_BuilderError_QuitsWithError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	opts := browse.Options{
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{}, boom
		},
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{}, nil
		},
	}

	_, err := browse.Run(
		t.Context(), &jj.Fake{}, trimconfig.Config{}, opts,
		strings.NewReader(""), &strings.Builder{},
	)
	require.ErrorIs(t, err, boom)
}

// blockingBuilder returns a Builder that blocks until release is closed,
// then returns session — used to make the loading screen observably slow
// in tests, since a normal fake Builder can complete before teatest's
// polling loop ever samples the loading frame.
func blockingBuilder(session browse.Session, release <-chan struct{}) browse.Builder {
	return func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
		<-release

		return session, nil
	}
}

// TestBrowse_ShowsLoadingScreen_UntilBuilderReturns guards the initial
// load: bare `jj-trim` used to build its first candidate set synchronously
// before the Bubbletea program ever started rendering, so the terminal
// showed nothing at all while classification ran. Now it must show a
// loading indicator immediately, and only replace it with the real list
// once the builder actually returns.
func TestBrowse_ShowsLoadingScreen_UntilBuilderReturns(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	opts := browse.Options{
		Commits: blockingBuilder(commitsSession(), release),
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{}, nil
		},
	}

	tm := newTestModelWaitingFor(t, opts, "Loading commits")

	close(release)

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Commits")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestBrowse_QuitDuringLoad_ExitsCleanly guards screenLoading's quit path:
// a slow load must still be interruptible, even though the builder itself
// never returns during the test.
func TestBrowse_QuitDuringLoad_ExitsCleanly(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	opts := browse.Options{
		Commits: blockingBuilder(browse.Session{Fetch: noopFetch}, release),
		Bookmarks: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return browse.Session{}, nil
		},
	}

	tm := newTestModelWaitingFor(t, opts, "Loading commits")

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestBrowse_ToggleMode_ShowsLoadingBetweenModes guards the reload path:
// pressing tab used to block the whole event loop synchronously (no
// spinner, no partial redraw) while the other mode's session was built.
func TestBrowse_ToggleMode_ShowsLoadingBetweenModes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	opts := browse.Options{
		Commits: func(context.Context, jj.Runner, trimconfig.Config) (browse.Session, error) {
			return commitsSession(), nil
		},
		Bookmarks: blockingBuilder(bookmarksSession(), release),
	}

	tm := newTestModel(t, opts)

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Loading bookmarks")
	})

	close(release)

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "Bookmarks")
	})

	tm.Send(tea.KeyPressMsg{Code: 'q'})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}
