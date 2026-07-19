package review

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/tuistyle"
)

type screen int

const (
	screenList screen = iota
	screenConfirm
	// screenApplied shows a transient confirmation (or error) popup for
	// the batch that was just applied — see handleApplied/handleAppliedKey.
	// Dismissing it (any key) returns to screenList rather than quitting,
	// so a session can review/apply multiple batches before the user
	// actually quits.
	screenApplied
)

type decision int

const (
	decisionPending decision = iota
	decisionMarked
	decisionMarkedCascade
)

// reviewItem wraps Item with its current decision. Implements list.Item.
type reviewItem struct {
	Item

	decision decision
}

func (i reviewItem) FilterValue() string { return i.Legend.String() }

const pendingDot = "· "

// keyEnter/keyEsc name the two keys shared across screenConfirm's,
// screenFilters-adjacent, and screenApplied's key handling.
const (
	keyEnter = "enter"
	keyEsc   = "esc"
)

// itemDelegate renders list rows. hasDarkBG is updated (via
// list.Model.SetDelegate) whenever the model learns the terminal's actual
// background — see model.handleBackgroundColor. actionLetter/
// cascadeLetter are the uppercased markKey()s of the review session's
// Action/CascadeAction — the letter shown on a marked row is always the
// letter that was pressed to mark it. cascadeLetter is empty when there's
// no CascadeAction (commits review).
type itemDelegate struct {
	hasDarkBG     bool
	actionLetter  string
	cascadeLetter string
}

func (itemDelegate) Height() int                         { return 1 }
func (itemDelegate) Spacing() int                        { return 0 }
func (itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	ri, ok := listItem.(reviewItem)
	if !ok {
		return
	}

	glyph := pendingDot

	switch ri.decision {
	case decisionPending:
	case decisionMarked:
		glyph = d.actionLetter + " "
	case decisionMarkedCascade:
		glyph = d.cascadeLetter + " "
	}

	line := glyph + ri.Legend.String()
	st := tuistyle.New(d.hasDarkBG)

	// Every row gets its own full-width Render call (never nested inside
	// another styled Render — see tabBar's doc comment in internal/browse
	// for why that pattern breaks background rendering), so the whole
	// list block — not just the selected row — carries a background,
	// giving it contrast against the plain-background detail pane below.
	switch {
	case index == m.Index():
		line = st.Selected.Width(m.Width()).Render(line)
	case ri.decision != decisionPending:
		line = st.Marked.Width(m.Width()).Render(line)
	default:
		line = st.ListRow.Width(m.Width()).Render(line)
	}

	_, _ = fmt.Fprint(w, line)
}

// model is the review TUI's Bubbletea model.
type model struct {
	ctx    context.Context //nolint:containedctx // lifecycle context wired once in Run()
	runner jj.Runner
	action Action
	fetch  ContextFetcher

	items []reviewItem

	list        list.Model
	detail      viewport.Model
	detailCache map[int]string

	// opLog is screenApplied's scrollable pager, showing `jj op show` for
	// each op id the last batch produced — what the batch actually did,
	// not merely that it ran. Set to "loading…" in handleApplied and
	// filled in asynchronously by handleOpLogFetched, mirroring
	// fetchDetailCmd/handleDetailFetched's pattern for the list's own
	// detail pane.
	opLog viewport.Model

	screen        screen
	width, height int
	ready         bool
	// hasDarkBG defaults true (most terminal themes are dark) until
	// tea.BackgroundColorMsg arrives in response to Init's
	// tea.RequestBackgroundColor, at which point styles are rebuilt for
	// the terminal's actual theme.
	hasDarkBG bool
	// actionLetter/cascadeLetter are the uppercased markKey()s of
	// action/action.CascadeAction, computed once in newModel and reused
	// by both the initial list delegate and handleBackgroundColor's
	// rebuild — see itemDelegate's doc comment.
	actionLetter  string
	cascadeLetter string

	// result accumulates across every batch applied in this session (a
	// session can now apply more than once before quitting — see
	// screenApplied); lastBatch is just the most recent one, for the
	// confirmation popup and for pendingError — see that method's doc
	// comment for why the error it reports isn't just a plain field.
	result    Result
	lastBatch appliedMsg
}

func newModel(
	ctx context.Context,
	r jj.Runner,
	items []Item,
	action Action,
	fetch ContextFetcher,
) *model {
	if action.CascadeAction != nil && action.markKey() == action.CascadeAction.markKey() {
		panic(fmt.Sprintf(
			"review: Action.Verb %q and CascadeAction.Verb %q collide on mark key %q",
			action.Verb, action.CascadeAction.Verb, action.markKey()))
	}

	listItems := make([]list.Item, 0, len(items))
	reviewItems := make([]reviewItem, 0, len(items))

	for _, it := range items {
		// Every item starts Pending, regardless of classification
		// certainty — review always requires an explicit choice, even
		// for the certain `merged` bucket (which `apply` can still
		// bulk-delete outside of review).
		ri := reviewItem{Item: it, decision: decisionPending}
		reviewItems = append(reviewItems, ri)
		listItems = append(listItems, ri)
	}

	actionLetter := strings.ToUpper(action.markKey())

	cascadeLetter := ""
	if action.CascadeAction != nil {
		cascadeLetter = strings.ToUpper(action.CascadeAction.markKey())
	}

	l := list.New(listItems, itemDelegate{
		hasDarkBG:     true,
		actionLetter:  actionLetter,
		cascadeLetter: cascadeLetter,
	}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowFilter(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
	l.SetFilteringEnabled(false)

	// SoftWrap: the detail pane's reason header now carries a full-sentence
	// Long description (see reasonHeader) — without it, that text runs off
	// the right edge instead of wrapping to the pane's width.
	detail := viewport.New()
	detail.SoftWrap = true

	return &model{
		ctx:           ctx,
		runner:        r,
		action:        action,
		fetch:         fetch,
		items:         reviewItems,
		list:          l,
		detail:        detail,
		detailCache:   make(map[int]string, len(items)),
		opLog:         viewport.New(),
		hasDarkBG:     true,
		actionLetter:  actionLetter,
		cascadeLetter: cascadeLetter,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetchDetailCmd(m.list.Index()), tea.RequestBackgroundColor)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case detailFetchedMsg:
		return m.handleDetailFetched(msg)
	case appliedMsg:
		return m.handleApplied(msg)
	case opLogFetchedMsg:
		return m.handleOpLogFetched(msg)
	case tea.BackgroundColorMsg:
		return m.handleBackgroundColor(msg)
	default:
		return m, nil
	}
}

// View renders the current screen. Alt-screen mode is set unconditionally
// (including on the pre-ready empty frame): this is a full-screen
// list/detail/confirm UI, not an inline dashboard meant to leave a trace
// in scrollback. It also fixes a real bug found in this session — without
// it, the jump from the empty pre-ready frame straight to the first full
// frame left a stray line (the last list item) uncleared above the
// header, since the inline (non-alt-screen) renderer diffs against the
// previous frame's line count rather than fully redrawing.
func (m *model) View() tea.View {
	if !m.ready {
		return tuistyle.AltScreenView("")
	}

	switch m.screen {
	case screenList:
		return tuistyle.AltScreenView(m.listView())
	case screenConfirm:
		return tuistyle.AltScreenView(m.confirmView())
	case screenApplied:
		return tuistyle.AltScreenView(m.appliedView())
	default:
		return tuistyle.AltScreenView(m.listView())
	}
}

// pendingError returns the most recent batch's error, but only while its
// confirmation popup is still showing (undismissed) — see
// handleAppliedKey, which returns to screenList on dismissal. Deriving it
// from screen+lastBatch instead of a separate cleared-on-dismiss field
// means there's exactly one place ("is the error popup still up?") that
// decides whether an error belongs to the session's outcome.
func (m *model) pendingError() error {
	if m.screen == screenApplied {
		return m.lastBatch.err
	}

	return nil
}

// handleBackgroundColor learns the terminal's actual light/dark theme (see
// Init's tea.RequestBackgroundColor) and rebuilds the list delegate so
// item rendering picks up the adaptive colors — see tuistyle.New.
func (m *model) handleBackgroundColor(msg tea.BackgroundColorMsg) (tea.Model, tea.Cmd) {
	m.hasDarkBG = msg.IsDark()
	m.list.SetDelegate(itemDelegate{
		hasDarkBG:     m.hasDarkBG,
		actionLetter:  m.actionLetter,
		cascadeLetter: m.cascadeLetter,
	})

	return m, nil
}

// detailFetchedMsg carries the ContextFetcher result for one item back to
// Update.
type detailFetchedMsg struct {
	index   int
	content string
	err     error
}

func (m *model) fetchDetailCmd(index int) tea.Cmd {
	if index < 0 || index >= len(m.items) {
		return nil
	}

	if _, cached := m.detailCache[index]; cached {
		return nil
	}

	candidate := m.items[index].Candidate

	return func() tea.Msg {
		content, err := m.fetch(m.ctx, m.runner, candidate)

		return detailFetchedMsg{index: index, content: content, err: err}
	}
}

func (m *model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.ready = true

	const (
		headerH   = 1 // "jj-trim review" bar
		sepH      = 2 // separator rule before the detail pane + before the footer
		footerH   = 1 // tally
		minDetail = 3
		maxList   = 10
	)

	available := max(m.height-headerH-sepH-footerH, minDetail+1)

	// The list only needs as many rows as there are items (up to maxList);
	// anything left over goes to the detail pane, so a short candidate list
	// doesn't leave the detail pane needlessly cramped — or, on a short
	// terminal, doesn't push content off-screen the way a fixed height did.
	listH := max(min(len(m.items), maxList), 1)

	detailH := available - listH
	if detailH < minDetail {
		detailH = minDetail
		listH = max(available-detailH, 1)
	}

	m.list.SetSize(m.width, listH)
	m.detail.SetWidth(m.width)
	m.detail.SetHeight(detailH)

	// screenApplied's own layout: header + rule, a fixed budget for the
	// summary lines (verb count plus one undo hint per op id — appliedSummaryH
	// is generous enough for the common one-or-two-op-id case), a rule,
	// the scrollable op log pager taking whatever's left, then the footer.
	const (
		appliedHeaderH  = 1
		appliedSepH     = 2
		appliedSummaryH = 3
		appliedFooterH  = 1
		minOpLog        = 3
	)

	opLogH := max(m.height-appliedHeaderH-appliedSepH-appliedSummaryH-appliedFooterH, minOpLog)
	m.opLog.SetWidth(m.width)
	m.opLog.SetHeight(opLogH)

	return m, nil
}

func (m *model) handleDetailFetched(msg detailFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.index < 0 || msg.index >= len(m.items) {
		return m, nil
	}

	header := reasonHeader(m.items[msg.index].Legend.Reason)

	if msg.err != nil {
		m.detailCache[msg.index] = header + "error fetching context: " + msg.err.Error()
	} else {
		m.detailCache[msg.index] = header + msg.content
	}

	if msg.index == m.list.Index() {
		m.detail.SetContent(m.detailCache[msg.index])
	}

	return m, nil
}

// reasonHeader renders a reviewItem's classification reason (confidence,
// short and long description) as a block leading the detail pane, above
// whatever the item's ContextFetcher fetched (e.g. `jj show`) — the "why
// was this flagged" that the plain Legend line doesn't have room for.
func reasonHeader(reason classify.Reason) string {
	info := classify.Describe(reason)

	return fmt.Sprintf("[%s] %s\n%s\n\n", info.Confidence.Letter(), info.Short, info.Long)
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	}

	switch m.screen {
	case screenList:
		if cmd, handled := m.handleListMarkKey(msg); handled {
			return m, cmd
		}

		return m.handleListNavigation(msg)
	case screenConfirm:
		return m.handleConfirmKey(msg)
	case screenApplied:
		return m.handleAppliedKey(msg)
	default:
		return m.handleListNavigation(msg)
	}
}

// handleListMarkKey handles the list screen's fixed keys (esc/enter/u) and
// the dynamically-derived action/cascade mark keys. The bool return is
// false when msg wasn't any of these, so the caller falls through to list
// navigation.
func (m *model) handleListMarkKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case keyEsc:
		return tea.Quit, true
	case keyEnter:
		m.screen = screenConfirm

		return nil, true
	case "u":
		m.clearDecision(m.list.Index())

		return nil, true
	case m.action.markKey():
		m.setDecision(m.list.Index(), decisionMarked)

		return m.advanceCursorCmd(), true
	}

	if m.action.CascadeAction != nil && msg.String() == m.action.CascadeAction.markKey() {
		m.setDecision(m.list.Index(), decisionMarkedCascade)

		return m.advanceCursorCmd(), true
	}

	return nil, false
}

func (m *model) handleListNavigation(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	before := m.list.Index()

	var cmd tea.Cmd

	m.list, cmd = m.list.Update(msg)

	if m.list.Index() != before {
		return m, tea.Batch(cmd, m.syncDetailCmd())
	}

	return m, cmd
}

// syncDetailCmd refreshes the detail pane for the list's current index —
// from cache if we've already fetched it, otherwise a "loading…" placeholder
// plus the fetch command. Shared by cursor movement (handleListNavigation)
// and the mark keys, which advance the cursor themselves via CursorDown.
func (m *model) syncDetailCmd() tea.Cmd {
	if content, ok := m.detailCache[m.list.Index()]; ok {
		m.detail.SetContent(content)
	} else {
		m.detail.SetContent("loading…")
	}

	return m.fetchDetailCmd(m.list.Index())
}

// advanceCursorCmd moves the list cursor to the next row after a mark key
// is pressed, so reviewing a batch of items doesn't require alternating
// between a mark key and a down-arrow for every item.
func (m *model) advanceCursorCmd() tea.Cmd {
	before := m.list.Index()
	m.list.CursorDown()

	if m.list.Index() == before {
		return nil
	}

	return m.syncDetailCmd()
}

func (m *model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc:
		m.screen = screenList

		return m, nil
	case keyEnter:
		marked, cascade := m.markedItems()
		if len(marked) == 0 && len(cascade) == 0 {
			// Nothing to apply — go straight back to the list rather than
			// round-tripping through applyCmd/the applied popup for a
			// no-op batch.
			m.screen = screenList

			return m, nil
		}

		return m, m.applyCmd()
	default:
		return m, nil
	}
}

// showingOpLog reports whether appliedView is currently rendering the
// scrollable op log pager — only true for a successful batch that
// produced at least one op id (see appliedView). Gates handleAppliedKey's
// dismiss-key rule: with a pager on screen, most keys need to reach it as
// scroll input instead of dismissing; without one (error, or nothing
// applied), there's nothing to scroll, so any key still dismisses.
func (m *model) showingOpLog() bool {
	return m.lastBatch.err == nil && len(m.lastBatch.result.OpIDs) > 0
}

// handleAppliedKey routes screenApplied's keys (q/ctrl+c already quit
// immediately — handled in handleKey before this is ever reached). When
// the op log pager is showing (see showingOpLog), only enter/esc dismiss;
// every other key is forwarded to the pager's own viewport so its scroll
// keys (arrows/j/k/pgup/pgdn/etc. — see viewport.DefaultKeyMap) work.
// Otherwise (error, or nothing applied — no pager to scroll), any key
// still dismisses, as before.
func (m *model) handleAppliedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if !m.showingOpLog() {
		return m.dismissApplied()
	}

	switch msg.String() {
	case keyEnter, keyEsc:
		return m.dismissApplied()
	}

	var cmd tea.Cmd

	m.opLog, cmd = m.opLog.Update(msg)

	return m, cmd
}

// dismissApplied is handleAppliedKey's enter/esc case: return to the list
// screen. Whatever items lastBatch.result.Applied actually names get
// pruned from the list, regardless of whether the batch as a whole
// errored — applyCmd only ever includes an item there once its own
// operation genuinely ran (see its doc comment on partial cascade
// failure), so pruning off that exact set never discards an item that
// still needs a retry. Leaving screenApplied is what makes pendingError
// stop reporting this batch's error — it only lingers (and so only
// reaches Outcome/Run's caller as the session's final error) if the user
// quits while still looking at an undismissed error.
func (m *model) dismissApplied() (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	if len(m.lastBatch.result.Applied) > 0 {
		cmd = m.pruneApplied()
	}

	m.screen = screenList

	return m, cmd
}

// setDecision sets index's decision to d, unless it's already d, in which
// case it toggles back to pending — pressing a row's own mark key twice is
// the "go back and change your mind" mechanism. Pressing the *other*
// action's mark key while already marked switches state rather than
// stacking, since it's the same setDecision call with a different d.
func (m *model) setDecision(index int, d decision) {
	if index < 0 || index >= len(m.items) {
		return
	}

	ri := m.items[index]
	if ri.decision == d {
		ri.decision = decisionPending
	} else {
		ri.decision = d
	}

	m.items[index] = ri
	_ = m.list.SetItem(index, ri)
}

// clearDecision unconditionally returns index to pending regardless of its
// current state — the `u` key's unambiguous "start over" action,
// supplementing setDecision's same-key-toggles-off behavior.
func (m *model) clearDecision(index int) {
	if index < 0 || index >= len(m.items) {
		return
	}

	ri := m.items[index]
	ri.decision = decisionPending
	m.items[index] = ri
	_ = m.list.SetItem(index, ri)
}

// appliedMsg carries the outcome of one applyCmd batch back to Update —
// see handleApplied/screenApplied.
type appliedMsg struct {
	result Result
	err    error
}

// applyCmd runs each batch that has marked items as an independent
// operation — Action first, then CascadeAction (if present). A
// cascade-marked item always runs the *primary* action's IDs too (not just
// CascadeIDs): a real bug found in review is that cascade-abandon's
// private-chain revset is empty for a candidate whose own commit is
// already an ancestor of trunk (the "merged" bookmarks bucket) — there's
// nothing private left to abandon, so jj's own "delete the bookmark that
// pointed at an abandoned commit" behavior never triggers, and the
// bookmark silently survives no matter how many times the user marks and
// applies it. Always running the primary action for cascade-marked items
// too guarantees the thing being cleaned up (e.g. the bookmark ref) is
// actually gone regardless of whether there's any private history to
// additionally abandon — cascade becomes "primary action, plus additional
// cleanup" rather than "only the additional cleanup, hoping it has the
// same side effect." If the primary batch succeeds but the cascade batch
// then fails, the returned appliedMsg still carries the primary batch's
// opID and Applied entries (for every marked item, cascade included)
// alongside the error — a real op already ran and must not be lost from
// the session's result (see handleApplied), and those items must not be
// left marked for a re-apply that would rerun that same already-succeeded
// operation (see handleAppliedKey/pruneApplied).
func (m *model) applyCmd() tea.Cmd {
	marked, cascade := m.markedItems()

	var actionIDs, cascadeIDs []string

	allMarked := make([]reviewItem, 0, len(marked)+len(cascade))

	for _, ri := range marked {
		actionIDs = append(actionIDs, ri.IDs...)
		allMarked = append(allMarked, ri)
	}

	for _, ri := range cascade {
		actionIDs = append(actionIDs, ri.IDs...)
		cascadeIDs = append(cascadeIDs, ri.CascadeIDs...)
		allMarked = append(allMarked, ri)
	}

	return func() tea.Msg {
		if len(actionIDs) == 0 && len(cascadeIDs) == 0 {
			return appliedMsg{}
		}

		var result Result

		if err := m.runBatchInto(&result, m.action, actionIDs, allMarked); err != nil {
			return appliedMsg{result: result, err: err}
		}

		if m.action.CascadeAction != nil && len(cascadeIDs) > 0 {
			// Credited to result.Applied already, above — the primary
			// action already ran for every cascade-marked item, so this
			// step only needs to record its own opID, not re-append
			// candidates (that would duplicate them in Applied). Uses
			// runCascadeBatch (not runBatch) since this step can
			// legitimately no-op — see its doc comment.
			opID, err := m.runCascadeBatch(*m.action.CascadeAction, cascadeIDs)
			if err != nil {
				return appliedMsg{result: result, err: err}
			}

			if opID != "" {
				result.OpIDs = append(result.OpIDs, opID)
			}
		}

		return appliedMsg{result: result}
	}
}

// runBatchInto runs one batch (see runBatch) and, only on success, appends
// its opID and items' candidates into result — the shared step applyCmd's
// closure runs once for the primary batch and once more for the cascade
// batch, factored out so a cascade failure after a successful primary
// batch still leaves the primary's contribution in result (see applyCmd's
// doc comment on partial failure).
func (m *model) runBatchInto(
	result *Result,
	action Action,
	ids []string,
	items []reviewItem,
) error {
	opID, err := m.runBatch(action, ids)
	if err != nil {
		return err
	}

	if opID != "" {
		result.OpIDs = append(result.OpIDs, opID)
	}

	for _, ri := range items {
		result.Applied = append(result.Applied, ri.Candidate)
	}

	return nil
}

// runBatch runs action.Apply on ids (a no-op returning "", nil if ids is
// empty) and looks up the resulting op id.
// runBatch runs action.Apply on ids (a no-op returning "", nil if ids is
// empty) and looks up the resulting op id. Used for the primary batch,
// which never silently no-ops when ids is non-empty — an id that doesn't
// correspond to anything real (e.g. a bookmark name that no longer
// exists) surfaces as an error from action.Apply, not a quiet success.
func (m *model) runBatch(action Action, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}

	if err := action.Apply(m.ctx, m.runner, ids); err != nil {
		return "", fmt.Errorf("%s: %w", action.Verb, err)
	}

	opID, err := jj.LastOpID(m.ctx, m.runner)
	if err != nil {
		return "", fmt.Errorf("looking up resulting op id: %w", err)
	}

	return opID, nil
}

// runCascadeBatch is runBatch for the cascade batch specifically. Unlike
// the primary batch, a cascade abandon's private-chain revset can
// legitimately be empty (see applyCmd's doc comment — a candidate whose
// commit is already an ancestor of trunk has nothing private left), and
// `jj abandon` on an empty revset succeeds without advancing jj's own op
// log at all. Comparing the op id before and after the call is what
// catches that "succeeded but changed nothing" case. A real bug found in
// review: calling plain runBatch here (unconditional jj.LastOpID after
// every non-empty-ids call) meant a no-op abandon still "produced" the
// *previous* batch's own opID, so the applied popup showed the same
// "Undo with: jj op revert X" line twice for what both really are one
// operation.
func (m *model) runCascadeBatch(action Action, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}

	before, err := jj.LastOpID(m.ctx, m.runner)
	if err != nil {
		return "", fmt.Errorf("looking up current op id: %w", err)
	}

	if err := action.Apply(m.ctx, m.runner, ids); err != nil {
		return "", fmt.Errorf("%s: %w", action.Verb, err)
	}

	after, err := jj.LastOpID(m.ctx, m.runner)
	if err != nil {
		return "", fmt.Errorf("looking up resulting op id: %w", err)
	}

	if after == before {
		return "", nil
	}

	return after, nil
}

// handleApplied processes the outcome of one applyCmd batch: it always
// accumulates whatever msg.result carries into the session's cumulative
// result (a session can now apply more than once — see screenApplied) —
// even on failure, since applyCmd's cascade batch can fail after the
// primary batch already ran for real, in which case msg.result still
// carries that primary batch's opID/Applied entries and they must not be
// lost. Either way it shows the screenApplied popup rather than quitting —
// pruning the list happens when that popup is dismissed, not here (see
// handleAppliedKey/pendingError), so the error stays visible until the
// user acknowledges it.
func (m *model) handleApplied(msg appliedMsg) (tea.Model, tea.Cmd) {
	m.lastBatch = msg
	m.screen = screenApplied

	m.result.Applied = append(m.result.Applied, msg.result.Applied...)
	m.result.OpIDs = append(m.result.OpIDs, msg.result.OpIDs...)

	if len(msg.result.OpIDs) == 0 {
		m.opLog.SetContent("")

		return m, nil
	}

	m.opLog.SetContent("loading…")

	return m, fetchOpLogCmd(m.ctx, m.runner, msg.result.OpIDs)
}

// opLogFetchedMsg carries jj op show's rendering of the last batch's op
// ids — see fetchOpLogCmd/handleOpLogFetched.
type opLogFetchedMsg struct {
	text string
	err  error
}

// fetchOpLogCmd runs `jj op show` for each of opIDs and joins the results —
// what the batch that just ran actually did, not merely that it ran.
// Captures only ctx/r/opIDs by value, never the *model itself, for the
// same reason internal/browse's loadSessionCmd does (the closure runs on
// a goroutine the Bubbletea runtime never waits for).
func fetchOpLogCmd(ctx context.Context, r jj.Runner, opIDs []string) tea.Cmd {
	return func() tea.Msg {
		var b strings.Builder

		for i, id := range opIDs {
			out, err := jj.OpShow(ctx, r, id)
			if err != nil {
				return opLogFetchedMsg{err: err}
			}

			if i > 0 {
				b.WriteString("\n")
			}

			b.WriteString(out)
		}

		return opLogFetchedMsg{text: b.String()}
	}
}

func (m *model) handleOpLogFetched(msg opLogFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.opLog.SetContent("(couldn't load op log: " + msg.err.Error() + ")")

		return m, nil
	}

	m.opLog.SetContent(msg.text)

	return m, nil
}

// pruneApplied removes the most recently applied batch's items from the
// list (they're done, not merely still-marked) and resets the detail
// cache, since indices shift when items are removed. Only called after a
// successful batch — see handleAppliedKey.
func (m *model) pruneApplied() tea.Cmd {
	applied := make(map[string]bool, len(m.lastBatch.result.Applied))
	for _, c := range m.lastBatch.result.Applied {
		applied[c.ChangeID] = true
	}

	kept := make([]reviewItem, 0, len(m.items))
	listItems := make([]list.Item, 0, len(m.items))

	for _, ri := range m.items {
		if applied[ri.Candidate.ChangeID] {
			continue
		}

		kept = append(kept, ri)
		listItems = append(listItems, ri)
	}

	m.items = kept
	_ = m.list.SetItems(listItems)
	m.detailCache = make(map[int]string, len(kept))

	if len(kept) == 0 {
		m.detail.SetContent("(nothing left to review)")

		return nil
	}

	// SetItems doesn't clamp the cursor to the new, shorter list — left
	// alone, a cursor that had advanced past the last surviving item (see
	// advanceCursorCmd) would point past the end, and every subsequent
	// mark/unmark on it would silently no-op (setDecision/clearDecision's
	// bounds checks).
	if m.list.Index() >= len(kept) {
		m.list.Select(len(kept) - 1)
	}

	m.detail.SetContent("loading…")

	return m.fetchDetailCmd(m.list.Index())
}

func (m *model) listView() string {
	st := tuistyle.New(m.hasDarkBG)

	var b strings.Builder

	b.WriteString(st.Header.Width(m.width).Render(
		"jj-trim review   [H/M/L] = confidence it's safe to delete: high/medium/low"))
	b.WriteString("\n")
	b.WriteString(m.list.View())
	b.WriteString("\n")
	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")
	b.WriteString(m.detail.View())
	b.WriteString("\n")
	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")
	b.WriteString(st.Footer.Render(m.tally()))

	return b.String()
}

// appliedView renders the screenApplied popup shown after a batch runs —
// either what was applied (with undo hints, plus a scrollable `jj op show`
// pager below — see opLog/fetchOpLogCmd) or the error, if it failed.
// Dismissing it (enter/esc — see handleAppliedKey) returns to the list;
// every other key scrolls the pager instead.
func (m *model) appliedView() string {
	st := tuistyle.New(m.hasDarkBG)

	var b strings.Builder

	headerStyle := st.Header
	header := "Applied"

	if m.lastBatch.err != nil {
		headerStyle = st.ErrorHeader
		header = "Error"
	}

	b.WriteString(headerStyle.Width(m.width).Render(header))
	b.WriteString("\n")
	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")

	switch {
	case m.lastBatch.err != nil:
		b.WriteString(st.ErrorText.Render(m.lastBatch.err.Error()))
		b.WriteString("\n")
	case len(m.lastBatch.result.OpIDs) == 0:
		b.WriteString("Nothing applied.\n")
	default:
		fmt.Fprintf(&b, "%d item(s) processed.\n", len(m.lastBatch.result.Applied))

		for _, opID := range m.lastBatch.result.OpIDs {
			fmt.Fprintf(&b, "Undo with: jj op revert %s\n", opID)
		}

		b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
		b.WriteString("\n")
		b.WriteString(m.opLog.View())
		b.WriteString("\n")
	}

	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")

	footer := "press any key to continue"
	if m.showingOpLog() {
		footer = "↑/↓ scroll  enter/esc to continue"
	}

	b.WriteString(st.Footer.Render(footer))

	return b.String()
}

func (m *model) confirmView() string {
	st := tuistyle.New(m.hasDarkBG)

	var b strings.Builder

	verb := m.action.Verb
	if m.action.CascadeAction != nil {
		verb += "/" + m.action.CascadeAction.Verb
	}

	b.WriteString(st.Header.Width(m.width).Render(fmt.Sprintf("Confirm: %s the following", verb)))
	b.WriteString("\n")
	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")

	marked, cascade := m.markedItems()
	if len(marked) == 0 && len(cascade) == 0 {
		b.WriteString("(nothing marked)\n")
	}

	for _, ri := range marked {
		b.WriteString("  ")
		b.WriteString(ri.Legend.String())
		b.WriteString("\n")
	}

	for _, ri := range cascade {
		b.WriteString("  ")
		b.WriteString(ri.Legend.String())

		if m.action.CascadeAction != nil {
			fmt.Fprintf(&b, "  (+ private chain, will %s)", m.action.CascadeAction.Verb)
		}

		b.WriteString("\n")
	}

	b.WriteString(tuistyle.RuleLine(m.width, st.Rule))
	b.WriteString("\n")
	b.WriteString(st.Footer.Render(m.confirmSummary(len(marked), len(cascade))))

	return b.String()
}

// confirmSummary renders the confirm screen's footer summary line, with a
// second count only when a CascadeAction exists.
func (m *model) confirmSummary(actionCount, cascadeCount int) string {
	summary := fmt.Sprintf("%d to %s", actionCount, m.action.Verb)
	if m.action.CascadeAction != nil {
		summary = fmt.Sprintf("%s | %d to %s", summary, cascadeCount, m.action.CascadeAction.Verb)
	}

	return summary + " — enter=confirm esc=back q=cancel"
}

// markedItems splits m.items by decision: items marked with Action, and
// items marked with CascadeAction (always empty when m.action.CascadeAction
// is nil).
func (m *model) markedItems() ([]reviewItem, []reviewItem) {
	var marked, cascade []reviewItem

	for _, ri := range m.items {
		switch ri.decision {
		case decisionPending:
		case decisionMarked:
			marked = append(marked, ri)
		case decisionMarkedCascade:
			cascade = append(cascade, ri)
		}
	}

	return marked, cascade
}

func (m *model) tally() string {
	marked, cascade := m.markedItems()

	var countsPart, help string

	if m.action.CascadeAction != nil {
		countsPart = fmt.Sprintf("%d %s | %d %s",
			len(marked), m.action.Verb, len(cascade), m.action.CascadeAction.Verb)
		help = fmt.Sprintf("%s=%s  %s=%s  u=unmark  enter=next  q/esc=cancel",
			m.action.markKey(), m.action.Verb,
			m.action.CascadeAction.markKey(), m.action.CascadeAction.Verb)
	} else {
		countsPart = fmt.Sprintf("%d to %s", len(marked), m.action.Verb)
		help = fmt.Sprintf(
			"%s=%s  u=unmark  enter=next  q/esc=cancel",
			m.action.markKey(),
			m.action.Verb,
		)
	}

	return fmt.Sprintf(
		"%s | %d/%d reviewed | %s",
		countsPart,
		len(m.detailCache),
		len(m.items),
		help,
	)
}
