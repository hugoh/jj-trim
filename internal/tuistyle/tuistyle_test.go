package tuistyle_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/hugoh/jj-trim/internal/tuistyle"
	"github.com/stretchr/testify/assert"
)

func TestAltScreenView(t *testing.T) {
	t.Parallel()

	v := tuistyle.AltScreenView("hello")

	assert.True(t, v.AltScreen)
	assert.Equal(t, "hello", v.Content)
	assert.Equal(t, "jj-trim", v.WindowTitle)
	assert.Equal(t, tea.MouseModeCellMotion, v.MouseMode, "wheel scrolling needs mouse reporting")
}

func TestNew_DarkMode(t *testing.T) {
	t.Parallel()

	s := tuistyle.New(true)

	assert.Contains(t, s.Header.Render("test"), "test")
	assert.NotEmpty(t, s.Header.Render("test"))
	assert.Contains(t, s.Selected.Render("test"), "test")
	assert.Contains(t, s.Footer.Render("test"), "test")
	assert.Contains(t, s.Rule.Render("test"), "test")
	assert.Contains(t, s.ErrorHeader.Render("test"), "test")
	assert.Contains(t, s.ErrorText.Render("test"), "test")
}

func TestNew_ErrorHeaderDiffersFromHeader(t *testing.T) {
	t.Parallel()

	s := tuistyle.New(true)

	assert.NotEqual(t, s.Header.Render("x"), s.ErrorHeader.Render("x"),
		"error header must render differently from the normal header")
}

func TestNew_LightMode(t *testing.T) {
	t.Parallel()

	s := tuistyle.New(false)

	assert.Contains(t, s.Header.Render("test"), "test")
	assert.Contains(t, s.Marked.Render("test"), "test")
	assert.Contains(t, s.ListRow.Render("test"), "test")
}

func TestRuleLine(t *testing.T) {
	t.Parallel()

	st := lipgloss.NewStyle()

	tests := []struct {
		name   string
		width  int
		expect int
	}{
		{name: "positive width", width: 10, expect: 10},
		{name: "zero width clamps to 1", width: 0, expect: 1},
		{name: "negative width clamps to 1", width: -5, expect: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			line := tuistyle.RuleLine(tt.width, st)

			assert.Equal(t, tt.expect, strings.Count(line, "─"))
		})
	}
}

func TestStyles_AreNotIdentical(t *testing.T) {
	t.Parallel()

	dark := tuistyle.New(true)
	light := tuistyle.New(false)

	assert.NotEqual(t, dark.Footer.Render("x"), light.Footer.Render("x"),
		"footer foreground must differ between light and dark mode")
	assert.NotEqual(t, dark.Rule.Render("x"), light.Rule.Render("x"),
		"rule foreground must differ between light and dark mode")
	assert.NotEqual(t, dark.Marked.Render("x"), light.Marked.Render("x"),
		"marked foreground must differ between light and dark mode")
}

func TestFitLine_StaysOneLine(t *testing.T) {
	t.Parallel()

	st := tuistyle.New(true)
	long := strings.Repeat("long-legend-text ", 10)

	tests := []struct {
		name  string
		style lipgloss.Style
		width int
	}{
		{name: "unpadded row style", style: st.Selected, width: 20},
		{name: "padded header style", style: st.Header, width: 20},
		{name: "tiny width", style: st.Header, width: 2},
		{name: "zero width", style: st.Header, width: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := tuistyle.FitLine(tt.style, tt.width, long)

			assert.NotContains(t, out, "\n")
			assert.LessOrEqual(t, lipgloss.Width(out), tt.width)
		})
	}
}

func TestFitLine_ShortTextKept(t *testing.T) {
	t.Parallel()

	out := tuistyle.FitLine(tuistyle.New(true).Header, 40, "short")

	assert.Contains(t, out, "short")
	assert.NotContains(t, out, "…")
}

func TestTooSmall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		width, height int
		want          bool
	}{
		{
			name:   "exactly the minimum",
			width:  tuistyle.MinWidth,
			height: tuistyle.MinHeight,
			want:   false,
		},
		{name: "roomy", width: 120, height: 40, want: false},
		{name: "too narrow", width: tuistyle.MinWidth - 1, height: 40, want: true},
		{name: "too short", width: 120, height: tuistyle.MinHeight - 1, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tuistyle.TooSmall(tt.width, tt.height))
		})
	}
}

func TestTooSmallView(t *testing.T) {
	t.Parallel()

	v := tuistyle.TooSmallView(20, 5)

	assert.True(t, v.AltScreen)
	assert.Contains(t, v.Content, "too small")
}
