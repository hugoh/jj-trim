// Package tuistyle holds the lipgloss styling shared by jj-trim's
// full-screen Bubbletea views (internal/review, internal/browse), so the
// review list/detail/confirm screens and the browse chrome around them
// render with a consistent look instead of each defining near-identical
// styles independently.
package tuistyle

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AltScreenView wraps content in a tea.View rendered on the terminal's
// alternate screen buffer — shared by internal/review and internal/browse
// so both full-screen Bubbletea models render the same way.
func AltScreenView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true

	return v
}

// Styles are computed from hasDarkBG (learned via tea.BackgroundColorMsg)
// rather than hardcoded, so the TUI reads correctly on both light and dark
// terminal themes: Header/Selected use fixed colors since they render as
// solid bars overriding the terminal's own background regardless of theme,
// but everything drawn on the terminal's own default background (marked-row
// text, the footer, separator rules) needs a foreground that adapts, or it
// can wash out against one theme or the other.
type Styles struct {
	Header   lipgloss.Style
	Selected lipgloss.Style
	Marked   lipgloss.Style
	// ListRow is the background every non-selected list row renders with
	// (see internal/review's itemDelegate.Render) — in light mode only, a
	// light gray tint so the list block reads as visually distinct from
	// the plain-background detail pane below it; on a dark terminal the
	// two are already contrasty enough without one, so ListRow leaves the
	// background unset there.
	ListRow lipgloss.Style
	Footer  lipgloss.Style
	Rule    lipgloss.Style
	// ErrorHeader is Header's counterpart for a failed batch (see
	// internal/review's appliedView): a red bar instead of the neutral
	// accent color, so a failed `jj abandon`/`jj bookmark delete` reads as
	// unmistakably different from a normal "Applied" result rather than
	// just another header with different text.
	ErrorHeader lipgloss.Style
	// ErrorText colors the error message body itself, so it stands out
	// from the plain-background detail text below ErrorHeader too.
	ErrorText lipgloss.Style
}

//nolint:gochecknoglobals // effectively constant, lipgloss colors
var (
	colorAccent      = lipgloss.Color("62") // header bar background
	colorSelectBg    = lipgloss.Color("24") // selected-row background
	colorOnBar       = lipgloss.Color("255")
	colorListBgLight = lipgloss.Color("254") // light-mode list-row tint
	colorError       = lipgloss.Color("160") // error header background
	colorErrorText   = lipgloss.Color("196") // error message foreground
)

// New builds Styles for the terminal's current light/dark theme.
func New(hasDarkBG bool) Styles {
	ld := lipgloss.LightDark(hasDarkBG)

	listRow := lipgloss.NewStyle()
	marked := lipgloss.NewStyle().Foreground(ld(lipgloss.Color("28"), lipgloss.Color("10")))

	if !hasDarkBG {
		listRow = listRow.Background(colorListBgLight)
		marked = marked.Background(colorListBgLight)
	}

	return Styles{
		Header: lipgloss.NewStyle().Bold(true).Background(colorAccent).
			Foreground(colorOnBar).Padding(0, 1),
		Selected: lipgloss.NewStyle().Bold(true).Background(colorSelectBg).Foreground(colorOnBar),
		Marked:   marked,
		ListRow:  listRow,
		Footer:   lipgloss.NewStyle().Foreground(ld(lipgloss.Color("241"), lipgloss.Color("245"))),
		Rule:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("245"), lipgloss.Color("238"))),
		ErrorHeader: lipgloss.NewStyle().Bold(true).Background(colorError).
			Foreground(colorOnBar).Padding(0, 1),
		ErrorText: lipgloss.NewStyle().Bold(true).Foreground(colorErrorText),
	}
}

// RuleLine renders a full-width horizontal separator, used to visually
// distinguish sections of a screen from each other — otherwise everything
// renders on the terminal's own background and blends together.
func RuleLine(width int, st lipgloss.Style) string {
	if width < 1 {
		width = 1
	}

	return st.Render(strings.Repeat("─", width))
}
