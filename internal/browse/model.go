package browse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/hugoh/jj-trim/internal/tuistyle"
)

// tabBarH is how many lines the mode tab bar occupies above the embedded
// review model's own view — subtracted from every tea.WindowSizeMsg
// forwarded to the child so it sizes its list/detail split correctly.
const tabBarH = 1

type screen int

const (
	// screenLoading is first so the zero-value model starts in it —
	// newModel no longer builds a child synchronously (see loadSessionCmd),
	// so there's nothing to show until the first sessionLoadedMsg arrives.
	screenLoading screen = iota
	screenChild
	screenFilters
)

// model is browse's top-level Bubbletea model. It owns the current mode and
// settings, and holds a child internal/review model for the active mode —
// review's own Update/View drive marking/confirm/apply unchanged; this
// model only intercepts the chrome keys (tab/f) before delegating
// everything else. err is only ever set by a session-load failure (initial
// load, toggleMode, or applyFilters — see handleSessionLoaded) — the
// marking/confirm/apply outcome itself lives in the child review model,
// recovered via review.FinishedSession once this model's own Program has
// quit (see browse.Run).
type model struct {
	ctx    context.Context //nolint:containedctx // lifecycle context wired once in Run()
	runner jj.Runner
	opts   Options

	mode Mode
	cfg  trimconfig.Config

	session Session
	child   tea.Model

	screen  screen
	filters *filtersForm

	// spin animates screenLoading — created once and reused across every
	// load/reload (its id must stay stable for its own TickMsgs to keep
	// being recognized by spinner.Model.Update).
	spin spinner.Model
	// pendingCarry stashes the outgoing child's accumulated Result (see
	// childResult) between when a mode/filters switch starts (synchronous,
	// no I/O) and when its sessionLoadedMsg arrives (async) — the new
	// child can only be built once the load completes, but the outgoing
	// child (and its Result) is gone by then.
	pendingCarry review.Result

	width, height int
	hasDarkBG     bool

	err error
}

func newModel(
	ctx context.Context,
	r jj.Runner,
	cfg trimconfig.Config,
	opts Options,
) *model {
	return &model{
		ctx:       ctx,
		runner:    r,
		opts:      opts,
		mode:      ModeCommits,
		cfg:       cfg,
		hasDarkBG: true,
		screen:    screenLoading,
		spin:      spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		loadSessionCmd(m.ctx, m.runner, m.cfg, m.builderFor(m.mode), m.mode),
		m.spin.Tick,
	)
}

// sessionLoadedMsg is delivered once an async Builder call (initial load,
// toggleMode, or applyFilters) completes. mode records which mode it was
// building for — not currently load-bearing for correctness (see
// handleLoadingKey's doc comment on why overlapping loads can't happen),
// but kept as a cheap assertion/future-proofing hook.
type sessionLoadedMsg struct {
	mode Mode
	sess Session
	err  error
}

// loadSessionCmd returns a tea.Cmd that runs builder asynchronously and
// delivers its result as a sessionLoadedMsg. It captures only ctx/r/cfg/
// builder/mode by value, never the *model itself: the closure runs on a
// goroutine the Bubbletea runtime spawns and never waits for (commands are
// "fire and forget" until they return), so it must not read or write any
// model field that Update might be concurrently mutating on the main loop.
func loadSessionCmd(
	ctx context.Context, r jj.Runner, cfg trimconfig.Config, builder Builder, mode Mode,
) tea.Cmd {
	return func() tea.Msg {
		sess, err := builder(ctx, r, cfg)

		return sessionLoadedMsg{mode: mode, sess: sess, err: err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.handleWindowSize(wsMsg)
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(keyMsg)
	}

	if bgMsg, ok := msg.(tea.BackgroundColorMsg); ok {
		m.hasDarkBG = bgMsg.IsDark()

		return m, m.forwardToChild(msg)
	}

	if tickMsg, ok := msg.(spinner.TickMsg); ok {
		return m.handleSpinnerTick(tickMsg)
	}

	if loadedMsg, ok := msg.(sessionLoadedMsg); ok {
		return m.handleSessionLoaded(loadedMsg)
	}

	if m.screen == screenFilters {
		var cmd tea.Cmd

		m.filters, cmd = m.filters.update(msg)

		return m, cmd
	}

	if m.screen == screenLoading {
		return m, nil
	}

	return m, m.forwardToChild(msg)
}

func (m *model) View() tea.View {
	if m.width == 0 {
		return tuistyle.AltScreenView("")
	}

	switch m.screen {
	case screenLoading:
		return tuistyle.AltScreenView(m.loadingView())
	case screenFilters:
		return tuistyle.AltScreenView(m.filters.view(m.width, m.hasDarkBG))
	case screenChild:
		return tuistyle.AltScreenView(m.tabBar() + "\n" + m.child.View().Content)
	default:
		return tuistyle.AltScreenView("")
	}
}

// loadingView renders the spinner plus a mode-specific label — m.mode is
// already set to the *target* mode before a toggleMode/applyFilters load
// starts, so the label is correct even mid-switch.
func (m *model) loadingView() string {
	st := tuistyle.New(m.hasDarkBG)

	label := "Loading bookmarks…"
	if m.mode == ModeCommits {
		label = "Loading commits…"
	}

	return st.Header.Width(m.width).Render(m.spin.View() + " " + label)
}

// forwardToChild forwards msg to m.child if it exists — a no-op while
// m.child is nil (before the first sessionLoadedMsg arrives).
func (m *model) forwardToChild(msg tea.Msg) tea.Cmd {
	if m.child == nil {
		return nil
	}

	var cmd tea.Cmd

	m.child, cmd = m.child.Update(msg)

	return cmd
}

// handleSpinnerTick keeps the spinner animating only while screenLoading is
// showing — dropping the tick once loading ends is what stops the
// animation, since nothing re-issues it afterward.
func (m *model) handleSpinnerTick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenLoading {
		return m, nil
	}

	var cmd tea.Cmd

	m.spin, cmd = m.spin.Update(msg)

	return m, cmd
}

// handleSessionLoaded processes the result of an async load (initial,
// toggleMode, or applyFilters). On failure, m.err/tea.Quit mirrors the
// convention every other in-session failure already uses (checked by
// browse.Run via fm.err after program.Run() returns). On success, builds
// the child seeded with whatever Result was carried forward from the
// outgoing child (see pendingCarry) — always via NewModelWithResult, since
// a zero Result behaves identically to NewModel for the very first load.
func (m *model) handleSessionLoaded(msg sessionLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err

		return m, tea.Quit
	}

	m.session = msg.sess
	m.child = review.NewModelWithResult(
		m.ctx, m.runner, msg.sess.Items, msg.sess.Action, msg.sess.Fetch, m.pendingCarry,
	)
	m.pendingCarry = review.Result{}
	m.screen = screenChild

	var sizeCmd tea.Cmd

	m.child, sizeCmd = m.child.Update(m.childWindowSize())

	return m, tea.Batch(m.child.Init(), sizeCmd)
}

func (m *model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width, m.height = msg.Width, msg.Height

	return m, m.forwardToChild(m.childWindowSize())
}

func (m *model) childWindowSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: m.width, Height: max(m.height-tabBarH, 0)}
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenLoading:
		return m.handleLoadingKey(msg)
	case screenFilters:
		return m.handleFiltersKey(msg)
	case screenChild:
		return m.handleChildScreenKey(msg)
	default:
		return m, nil
	}
}

// handleLoadingKey is screenLoading's only key handling: q/ctrl+c quit —
// there is no child yet (initial load) or the previous child has already
// been torn down (reload), so nothing else is reachable. Deliberately does
// NOT handle tab/f: a load already in flight must run to completion (or
// the whole program quit) before another one can start, which is what
// makes overlapping loads structurally unreachable rather than something
// needing a generation counter to guard against.
func (m *model) handleLoadingKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	default:
		return m, nil
	}
}

// handleChildScreenKey intercepts browse's own chrome keys — mode toggle,
// filters overlay — before falling through to the embedded review model
// for everything else (navigation, marking, confirm, quit, and apply —
// review's own confirm screen is the only apply path browse offers).
// q/ctrl+c/esc-on-list-screen already mean "quit everything" inside
// review's own model — that propagates up through this delegation
// unchanged, since browse never intercepts them itself.
//
// tab/f are only intercepted while the child reports Idle (see
// childIdle) — otherwise they're forwarded to the child like any other
// key. Intercepting them unconditionally used to be a real bug: pressing
// `f`/`tab` while the child was showing its confirm screen (marked items
// awaiting confirm) or its post-apply popup discarded that state outright
// (a rebuilt child starts from scratch), and — worse — an apply command
// already in flight when the user switched to the filters screen would
// deliver its appliedMsg to the filters form instead of the child once it
// resolved (Update routes non-key messages by m.screen), silently losing
// a real jj mutation's result and risking a double-apply on retry. Since
// the child never leaves its confirm/applied screens for screenList until
// that flow is fully resolved, gating on Idle rules out both cases with
// one check instead of two special cases.
func (m *model) handleChildScreenKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.childIdle() {
		switch msg.String() {
		case "tab":
			return m.toggleMode()
		case "f":
			m.screen = screenFilters
			m.filters = newFiltersForm(m.mode, m.cfg)

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.child, cmd = m.child.Update(msg)

	return m, cmd
}

// childIdle reports whether it's safe to intercept browse's own chrome
// keys or discard the child model outright — see handleChildScreenKey's
// doc comment. The child is always a *review model built by NewModel/
// NewModelWithResult, both of which implement review.ChildStatus, so the
// type assertion only fails in tests that swap in some other tea.Model.
func (m *model) childIdle() bool {
	cs, ok := m.child.(review.ChildStatus)

	return ok && cs.Idle()
}

// childResult recovers the child's accumulated Result so it can be
// carried forward into a replacement child (see toggleMode/applyFilters)
// — called only while childIdle() is true, so the child is never mid-
// popup and its pendingError is always nil.
func (m *model) childResult() review.Result {
	fs, ok := m.child.(review.FinishedSession)
	if !ok {
		return review.Result{}
	}

	res, _ := fs.Outcome()

	return res
}

// toggleMode starts an async load of the other mode's session from the
// current settings, exactly like starting a fresh `bookmarks review`/
// `commits review` — same builder functions, just triggered by a keypress
// instead of a Kong subcommand. The outgoing child's accumulated Result is
// stashed in m.pendingCarry (see its doc comment) so a batch already
// applied under the previous mode isn't lost once the new child is built
// in handleSessionLoaded — only reachable while childIdle() is true (see
// handleChildScreenKey), so there's never a pending popup/marks to lose
// alongside it.
func (m *model) toggleMode() (tea.Model, tea.Cmd) {
	next := ModeCommits
	if m.mode == ModeCommits {
		next = ModeBookmarks
	}

	m.pendingCarry = m.childResult()
	m.mode = next
	m.screen = screenLoading

	return m, tea.Batch(
		loadSessionCmd(m.ctx, m.runner, m.cfg, m.builderFor(next), next),
		m.spin.Tick,
	)
}

func (m *model) builderFor(mode Mode) Builder {
	if mode == ModeBookmarks {
		return m.opts.Bookmarks
	}

	return m.opts.Commits
}

func (m *model) handleFiltersKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenChild
		m.filters = nil

		return m, nil
	case "enter":
		return m.applyFilters()
	case "tab":
		m.filters.focusNext()

		return m, nil
	case "shift+tab":
		m.filters.focusPrev()

		return m, nil
	case "space":
		// bubbletea's Key.String() spells the spacebar "space", not " "
		// (see charmbracelet/ultraviolet's keyNames) — a literal " " case
		// here would never match a real spacebar press.
		if m.filters.mode == ModeCommits {
			m.filters.toggleBool()

			return m, nil
		}
	}

	var cmd tea.Cmd

	m.filters, cmd = m.filters.update(msg)

	return m, cmd
}

// applyFilters parses the filters form, and — only if it parses cleanly —
// commits the new config and starts an async reload of the active mode's
// session (see toggleMode's doc comment for the pendingCarry/async
// pattern this shares). A parse error (e.g. an unparsable stale-after
// duration) stays on the filters screen with an inline message rather than
// losing the form's contents. The child was idle when the filters screen
// was entered (the only way to reach it — see handleChildScreenKey) and
// has been untouched (no messages are routed to it while m.screen ==
// screenFilters) ever since, so it's still idle now — safe to read its
// Result via childResult.
func (m *model) applyFilters() (tea.Model, tea.Cmd) {
	newCfg, err := m.filters.apply(m.cfg)
	if err != nil {
		m.filters.err = err.Error()

		return m, nil
	}

	m.cfg = newCfg
	m.pendingCarry = m.childResult()
	m.filters = nil
	m.screen = screenLoading

	return m, tea.Batch(
		loadSessionCmd(m.ctx, m.runner, m.cfg, m.builderFor(m.mode), m.mode),
		m.spin.Tick,
	)
}

// tabBar builds one plain (unstyled) string and applies a single Style at
// the very end, rather than nesting a differently-styled Render call (e.g.
// for the active tab) inside it — a nested Render's own trailing reset
// sequence cancels the outer style's background for everything after it,
// since the outer Style.Render only emits its background/foreground codes
// once, at the very start of the whole string. The active tab is marked
// with brackets in the plain text instead, so the whole bar shares one
// uninterrupted background.
func (m *model) tabBar() string {
	st := tuistyle.New(m.hasDarkBG)

	modes := []Mode{ModeBookmarks, ModeCommits}

	var b strings.Builder

	for _, mode := range modes {
		label := mode.String()
		if mode == m.mode {
			b.WriteString("[")
			b.WriteString(label)
			b.WriteString("]")
		} else {
			b.WriteString(" ")
			b.WriteString(label)
			b.WriteString(" ")
		}

		b.WriteString(" ")
	}

	b.WriteString(" tab=switch mode  f=filters")

	return st.Header.Width(m.width).Render(b.String())
}

// filtersForm is the in-flow filters overlay: text fields for bookmarks
// mode (trunk/protected/stale-after), a single toggle for commits mode
// (no-description-only) — the same knobs as BookmarksCmd/CommitsCmd's CLI
// flags (cli.go), just edited interactively. Repository and fetch-first
// aren't editable here — -R and --fetch are resolved once before the
// browse session starts (fetch runs up front regardless of mode;
// repository selects which jj.Runner is even in play), so there's no
// in-session value for them to override.
type filtersForm struct {
	mode Mode

	fieldNames []string
	fieldHelp  []string
	fields     []textinput.Model
	focus      int

	boolValue bool // commits mode only

	err string
}

// staleAfterHelp gives a concrete example in an accepted unit, since Go's
// time.ParseDuration has no "days" unit — the "90d" reading of the
// default doesn't itself indicate a valid input.
const staleAfterHelp = `Go duration, e.g. "168h" for 7 days`

// defaultStaleAfterDisplay mirrors run.go's defaultStaleAfter (90 days) for
// display purposes only — the actual default is applied downstream
// (bookmarksBrowseSession) when cfg.StaleAfter is nil; this is only what
// the placeholder/help text shows.
const defaultStaleAfterDisplay = "2160h"

// fieldWidth is passed to every filters-form textinput's SetWidth — wide
// enough to show the longest placeholder/value here in full; see the
// SetWidth call below for why an unset width is a correctness bug, not
// just cosmetic.
const fieldWidth = 40

func newFiltersForm(mode Mode, cfg trimconfig.Config) *filtersForm {
	f := &filtersForm{mode: mode}

	if mode == ModeCommits {
		f.boolValue = cfg.NoDescriptionOnly

		return f
	}

	f.fieldNames = []string{"Trunk", "Protected", "Stale after"}
	f.fieldHelp = []string{
		"Revset override for trunk() (default: trunk())",
		"Comma-separated bookmark name globs, never deleted (default: none)",
		staleAfterHelp + " (default: " + defaultStaleAfterDisplay + ")",
	}

	values := []string{
		cfg.Trunk,
		strings.Join(cfg.Protected, ", "),
		staleAfterString(cfg.StaleAfter),
	}
	placeholders := []string{"trunk()", "", defaultStaleAfterDisplay}

	f.fields = make([]textinput.Model, len(f.fieldNames))
	for i := range f.fields {
		ti := textinput.New()
		// Prompt defaults to "> " — clear it since the "> "/"  " focus
		// prefix is already drawn by view() itself; left alone, the two
		// stack into a confusing double arrow. Width defaults to 0, which
		// caps the placeholder view at a single rune (see
		// textinput.Model.placeholderView: it sizes its rune buffer to
		// Width()+1) — a real bug found this session, where an unset
		// width silently truncated "2160h" down to just "2".
		ti.Prompt = ""
		ti.SetWidth(fieldWidth)
		ti.Placeholder = placeholders[i]
		ti.SetValue(values[i])
		f.fields[i] = ti
	}

	f.fields[0].Focus()

	return f
}

func (f *filtersForm) update(msg tea.Msg) (*filtersForm, tea.Cmd) {
	if f.mode == ModeCommits || len(f.fields) == 0 {
		return f, nil
	}

	var cmd tea.Cmd

	f.fields[f.focus], cmd = f.fields[f.focus].Update(msg)

	return f, cmd
}

func (f *filtersForm) focusNext() {
	if len(f.fields) == 0 {
		return
	}

	f.fields[f.focus].Blur()
	f.focus = (f.focus + 1) % len(f.fields)
	f.fields[f.focus].Focus()
}

func (f *filtersForm) focusPrev() {
	if len(f.fields) == 0 {
		return
	}

	f.fields[f.focus].Blur()
	f.focus = (f.focus - 1 + len(f.fields)) % len(f.fields)
	f.fields[f.focus].Focus()
}

func (f *filtersForm) toggleBool() {
	f.boolValue = !f.boolValue
}

// apply parses the form's current values into a new trimconfig.Config
// derived from base, returning an error (and leaving base untouched) if
// any field fails to parse.
func (f *filtersForm) apply(base trimconfig.Config) (trimconfig.Config, error) {
	cfg := base

	if f.mode == ModeCommits {
		cfg.NoDescriptionOnly = f.boolValue

		return cfg, nil
	}

	cfg.Trunk = strings.TrimSpace(f.fields[0].Value())

	protectedRaw := strings.TrimSpace(f.fields[1].Value())
	cfg.Protected = nil

	if protectedRaw != "" {
		for g := range strings.SplitSeq(protectedRaw, ",") {
			if g = strings.TrimSpace(g); g != "" {
				cfg.Protected = append(cfg.Protected, g)
			}
		}
	}

	staleRaw := strings.TrimSpace(f.fields[2].Value())
	if staleRaw == "" {
		cfg.StaleAfter = nil

		return cfg, nil
	}

	d, err := time.ParseDuration(staleRaw)
	if err != nil {
		return trimconfig.Config{}, fmt.Errorf("stale after %q: %w", staleRaw, err)
	}

	cfg.StaleAfter = &d

	return cfg, nil
}

// staleAfterString formats d the same way the field accepts it back
// (h/m/s units), dropping the "0m0s" tail time.Duration.String() always
// appends for whole-hour durations — "2160h0m0s" reads as a typo target,
// "2160h" reads as the value it is.
func staleAfterString(d *time.Duration) string {
	if d == nil {
		return ""
	}

	if *d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int64(*d/time.Hour))
	}

	return d.String()
}

func (f *filtersForm) view(width int, hasDarkBG bool) string {
	st := tuistyle.New(hasDarkBG)

	var b strings.Builder

	b.WriteString(st.Header.Width(width).Render("Filters — " + f.mode.String()))
	b.WriteString("\n")
	b.WriteString(tuistyle.RuleLine(width, st.Rule))
	b.WriteString("\n")

	if f.mode == ModeCommits {
		mark := "[ ]"
		if f.boolValue {
			mark = "[x]"
		}

		b.WriteString("> ")
		b.WriteString(mark)
		b.WriteString(" No-description only\n")
		b.WriteString("  space to toggle — restrict to commits with an empty description")
		b.WriteString(" (default: off)\n")
	} else {
		for i, name := range f.fieldNames {
			prefix := "  "
			if i == f.focus {
				prefix = "> "
			}

			b.WriteString(prefix)
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(f.fields[i].View())
			b.WriteString("\n")
			b.WriteString("    ")
			b.WriteString(st.Footer.Render(f.fieldHelp[i]))
			b.WriteString("\n")
		}
	}

	if f.err != "" {
		b.WriteString("\n")
		b.WriteString(st.ErrorText.Render("error: " + f.err))
		b.WriteString("\n")
	}

	b.WriteString(tuistyle.RuleLine(width, st.Rule))
	b.WriteString("\n")
	b.WriteString(st.Footer.Render("tab=next field  enter=save  esc=cancel"))

	return b.String()
}
