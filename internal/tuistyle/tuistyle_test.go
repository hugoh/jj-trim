package tuistyle_test

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/hugoh/jj-trim/internal/tuistyle"
	"github.com/stretchr/testify/assert"
)

func TestAltScreenView(t *testing.T) {
	t.Parallel()

	v := tuistyle.AltScreenView("hello")

	assert.True(t, v.AltScreen)
	assert.Equal(t, "hello", v.Content)
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
