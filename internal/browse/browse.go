// Package browse implements jj-trim's TUI-first front end: bare `jj-trim`
// launches straight into a live candidate view (no settings gate first),
// with a mode toggle (bookmarks/commits) and an in-flow filters overlay
// layered on top of the existing internal/review list/detail/confirm
// screen. It composes rather than reimplements: review's own model handles
// marking/confirm/apply exactly as it does for `bookmarks review`/`commits
// review` today — this package only intercepts a few chrome keys before
// delegating everything else to it.
package browse

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
)

// Mode selects which candidate set the browse screen is currently showing.
type Mode int

// Mode values.
const (
	ModeBookmarks Mode = iota
	ModeCommits
)

// String is Mode's display label, used by both the tab bar and the filters
// overlay's header so there's one spelling of each mode's name.
func (m Mode) String() string {
	if m == ModeCommits {
		return "Commits"
	}

	return "Bookmarks"
}

// Session is what a Builder computes for one mode from the current
// settings: the review items/action/context-fetcher review.NewModel needs.
type Session struct {
	Action review.Action
	Items  []review.Item
	Fetch  review.ContextFetcher
}

// Builder computes a Session for one mode from the current settings —
// reusing the exact same classification/query functions the CLI dispatch
// path already calls, so browse never reimplements them.
type Builder func(ctx context.Context, r jj.Runner, cfg trimconfig.Config) (Session, error)

// Options wires browse to the caller's classification logic — the same
// functions run.go's CLI dispatch already calls.
type Options struct {
	Bookmarks Builder
	Commits   Builder
}

// NewModel builds the browse Bubbletea model directly, without running it —
// used by this package's teatest-based tests (production code launching a
// standalone browse session should use Run instead), mirroring
// internal/review.NewModel. Construction never fails: the initial
// candidate load happens asynchronously after Init() (see model.go's
// screenLoading/loadSessionCmd) — a failure there surfaces later as an
// error from Run, exactly like any other in-session failure.
func NewModel(ctx context.Context, r jj.Runner, cfg trimconfig.Config, opts Options) tea.Model {
	return newModel(ctx, r, cfg, opts)
}

// Run launches the interactive browse front end and blocks until the user
// applies or quits, returning the embedded review session's outcome.
// Callers must check stdin is a real terminal first (see internal/tty) —
// Run itself doesn't, matching internal/review.Run.
func Run(
	ctx context.Context, r jj.Runner, cfg trimconfig.Config, opts Options,
	stdin io.Reader, stdout io.Writer,
) (review.Result, error) {
	m := newModel(ctx, r, cfg, opts)

	program := tea.NewProgram(m, tea.WithInput(stdin), tea.WithOutput(stdout), tea.WithContext(ctx))

	finalModel, err := program.Run()
	if err != nil {
		return review.Result{}, fmt.Errorf("browse tui: %w", err)
	}

	fm, ok := finalModel.(*model)
	if !ok {
		return review.Result{}, nil
	}

	return resultFromFinishedModel(fm)
}

// resultFromFinishedModel recovers Run's return value from browse's own
// finished *model. On a session-load failure (fm.err), returns
// fm.pendingCarry rather than an empty Result — a batch already applied
// under a since-abandoned mode/filters switch must still be reported
// alongside the unrelated failure that ended the session. Otherwise
// delegates to the child's FinishedSession, again returning its Result
// alongside any pending error rather than discarding it — browse owns the
// actual top-level tea.Program, embedding review's model as a child, so the
// child's outcome has to be recovered via the exported accessor rather than
// the fm.result field review.Run itself uses internally.
func resultFromFinishedModel(fm *model) (review.Result, error) {
	if fm.err != nil {
		return fm.pendingCarry, fm.err
	}

	fs, ok := fm.child.(review.FinishedSession)
	if !ok {
		return review.Result{}, nil
	}

	res, err := fs.Outcome()
	if err != nil {
		return res, fmt.Errorf("review session: %w", err)
	}

	return res, nil
}
