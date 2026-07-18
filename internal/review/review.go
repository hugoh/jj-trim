// Package review implements jj-trim's shared interactive review TUI, used
// by both `jj-trim bookmarks --review` and `jj-trim commits --review`: a
// navigable list of candidates with a detail pane, a live tally, and an
// explicit apply/cancel gate. See DESIGN.md's Visualization & review UX
// section.
package review

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
)

// Item is one thing the review flow can act on: a bookmark (possibly
// several names pointing at the same commit) or a single anonymous
// commit.
type Item struct {
	// IDs is what's passed to Action.Apply if this item ends up marked
	// with Action's decision — bookmark name(s) for `bookmarks`, a
	// private-chain revset expression for `commits` (see
	// classify.PrivateChainRevset).
	IDs []string
	// CascadeIDs is what's passed to Action.CascadeAction.Apply if this item
	// ends up marked with the cascade decision. Only meaningful when
	// Action.CascadeAction is non-nil (bookmarks review); empty otherwise.
	// May itself evaluate to nothing at apply time (e.g. a bookmark whose
	// commit is already an ancestor of trunk) — cascade then degrades to a
	// no-op abandon, on top of whatever Action.Apply already did.
	CascadeIDs []string
	// Candidate backs ContextFetcher (which needs Candidate.ChangeID) and
	// SortOldestFirst ordering.
	Candidate classify.Candidate
	// Legend is reused as-is for the list row's label text.
	Legend classify.LegendEntry
}

// Action is a batch operation applied to every item marked with a given
// decision when the user confirms — jj.BookmarkDelete or jj.Abandon,
// wrapped so this package doesn't need to know which.
type Action struct {
	// Verb labels the action in the tally/confirm screen ("delete",
	// "abandon") and — lowercased, first letter only — is the keybinding
	// that marks an item with this action's decision. See model.go's
	// newModel for the collision check against CascadeAction.Verb.
	Verb string
	// Past is the past-tense form used in the final result message:
	// "deleted" or "abandoned".
	Past string
	// Apply runs the batch operation. Called at most once per confirm,
	// with every item's IDs (or CascadeIDs, for CascadeAction) flattened
	// together, so each batch lands as one `jj op log` entry.
	Apply func(ctx context.Context, r jj.Runner, ids []string) error
	// CascadeAction, if non-nil, offers a second per-item decision — see
	// Item.CascadeIDs. nil for `commits review`, which has only one
	// action. Only Verb/Past/Apply are used on a CascadeAction; its own
	// CascadeAction field, if set, is ignored (no nesting).
	CascadeAction *Action
}

// markKey returns the lowercase first letter of Verb — the keybinding
// that marks an item with this action's decision. Deriving it from Verb
// (rather than a separate hardcoded field) guarantees the key a user reads
// in the footer/confirm screen is always the key that produces it.
func (a Action) markKey() string {
	return strings.ToLower(a.Verb[:1])
}

// ContextFetcher fetches the detail-pane text for one item (e.g. `jj show`
// plus descendants for commits; `jj show` for bookmarks).
type ContextFetcher func(ctx context.Context, r jj.Runner, c classify.Candidate) (string, error)

// Result is the outcome of a review session. A bookmarks-review session
// with both Action- and CascadeAction-marked items produces two
// independent batch operations (ref delete, then abandon), hence up to two
// op ids — OpIDs is empty if nothing was applied, and has one entry per
// batch that actually ran.
type Result struct {
	Applied []classify.Candidate
	OpIDs   []string
}

// FinishedSession exposes a review session's outcome once its embedding
// program has quit — meaningful only after Update has returned tea.Quit.
// internal/browse asserts a NewModel-returned tea.Model against this to
// recover the child review model's Result after browse's own (outer)
// Program exits: browse.Run's finalModel is always browse's own model type
// (it owns the actual top-level tea.Program, embedding review's model as a
// child), so it can't reach into review's unexported model.result field
// directly the way Run below does.
type FinishedSession interface {
	Outcome() (Result, error)
}

// Outcome implements FinishedSession.
func (m *model) Outcome() (Result, error) {
	return m.result, m.pendingError()
}

// ChildStatus lets an embedding caller (internal/browse) check whether the
// review session is idle — on its list screen, with no marked-but-
// unconfirmed items, no confirm/apply popup showing, and no apply command
// in flight — before intercepting its own keys or discarding the model
// outright. Any of those in-progress states being cut through would lose
// marks, skip acknowledging an apply popup, or drop an apply result the
// child hasn't recorded into its own Outcome yet.
type ChildStatus interface {
	Idle() bool
}

// Idle implements ChildStatus.
func (m *model) Idle() bool {
	return m.screen == screenList
}

// NewModel builds the review Bubbletea model directly, without running it —
// used by internal/browse to embed a review session inside a larger
// composed model, and by this package's teatest-based tests. Production
// code launching a standalone review session should use Run instead.
func NewModel(
	ctx context.Context, r jj.Runner, items []Item, action Action, fetch ContextFetcher,
) tea.Model {
	return newModel(ctx, r, items, action, fetch)
}

// NewModelWithResult is NewModel, seeded with an already-accumulated
// Result — used by internal/browse when it rebuilds the child for a mode
// or filters switch (only ever done while the previous child reports
// Idle, so nothing marked/pending is in flight — see ChildStatus), so a
// batch already applied under the previous child isn't lost once the
// session eventually quits and Outcome() is read from whichever child is
// current at that point.
func NewModelWithResult(
	ctx context.Context, r jj.Runner, items []Item, action Action, fetch ContextFetcher,
	initial Result,
) tea.Model {
	m := newModel(ctx, r, items, action, fetch)
	m.result = initial

	return m
}

// Run launches the interactive review TUI over items and blocks until the
// user applies or cancels. A cancelled session (q/Ctrl-C/Esc from the list
// screen) returns a zero Result and nil error — nothing is applied. The
// batch Action.Apply call is reached only via the confirm screen's
// explicit "enter", never from a cancellation path.
func Run(
	ctx context.Context, r jj.Runner, items []Item, action Action, fetch ContextFetcher,
	in io.Reader, out io.Writer,
) (Result, error) {
	m := newModel(ctx, r, items, action, fetch)

	program := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))

	finalModel, err := program.Run()
	if err != nil {
		return Result{}, fmt.Errorf("review tui: %w", err)
	}

	return resultFromFinalModel(finalModel)
}

// resultFromFinalModel recovers Run's return value from program.Run()'s
// finished tea.Model. Delegates to Outcome so a batch that already applied
// successfully before a later error (e.g. a cascade failure) is still
// returned alongside that error, instead of being discarded.
func resultFromFinalModel(finalModel tea.Model) (Result, error) {
	fm, ok := finalModel.(*model)
	if !ok {
		return Result{}, nil
	}

	return fm.Outcome()
}
