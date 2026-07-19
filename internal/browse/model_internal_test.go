package browse

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleLoadingKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       tea.KeyPressMsg
		wantQuit  bool
		wantScr   screen
		wantNoCmd bool
	}{
		{name: "q quits", key: tea.KeyPressMsg{Code: 'q'}, wantQuit: true},
		{name: "ctrl+c quits", key: tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, wantQuit: true},
		{
			name: "other key is a no-op", key: tea.KeyPressMsg{Code: tea.KeyEnter},
			wantScr: screenLoading, wantNoCmd: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert := assert.New(t)

			m := &model{screen: screenLoading}

			out, cmd := m.handleLoadingKey(tt.key)

			outModel, ok := out.(*model)
			require.True(t, ok)
			assert.Equal(
				screenLoading,
				outModel.screen,
				"handleLoadingKey never changes screen itself",
			)

			if tt.wantQuit {
				require.NotNil(t, cmd)
				assert.IsType(tea.QuitMsg{}, cmd())
			}

			if tt.wantNoCmd {
				assert.Nil(cmd)
			}
		})
	}
}

func TestFiltersForm_FocusPrev(t *testing.T) {
	t.Parallel()

	t.Run("no-op when there are no fields", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeCommits, trimconfig.Config{})
		require.Empty(t, f.fields)

		f.focusPrev()

		assert.Equal(0, f.focus)
	})

	t.Run("wraps backward from the first field to the last", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		require.Equal(t, 0, f.focus, "newFiltersForm focuses the first field")

		f.focusPrev()

		assert.Equal(len(f.fields)-1, f.focus)
		assert.True(f.fields[f.focus].Focused())
		assert.False(f.fields[0].Focused())
	})

	t.Run("moves back one field from the middle", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		f.focusNext()

		f.focusPrev()

		assert.Equal(0, f.focus)
		assert.True(f.fields[0].Focused())
	})
}

func TestFiltersForm_Apply(t *testing.T) {
	t.Parallel()

	t.Run("commits mode sets the bool value", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeCommits, trimconfig.Config{})
		f.boolValue = true

		cfg, err := f.apply(trimconfig.Config{})

		require.NoError(t, err)
		assert.True(cfg.NoDescriptionOnly)
	})

	t.Run("bookmarks mode parses protected globs", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		f.fields[1].SetValue(" a , b,c ")

		cfg, err := f.apply(trimconfig.Config{})

		require.NoError(t, err)
		assert.Equal([]string{"a", "b", "c"}, cfg.Protected)
	})

	t.Run("bookmarks mode clears protected when blank", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		f.fields[1].SetValue("  ")

		cfg, err := f.apply(trimconfig.Config{Protected: []string{"old"}})

		require.NoError(t, err)
		assert.Nil(cfg.Protected)
	})

	t.Run("bookmarks mode rejects an invalid stale-after duration", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		f.fields[2].SetValue("not-a-duration")

		_, err := f.apply(trimconfig.Config{})

		require.Error(t, err)
		assert.Contains(err.Error(), "not-a-duration")
	})

	t.Run("bookmarks mode parses a valid stale-after duration", func(t *testing.T) {
		t.Parallel()

		assert := assert.New(t)

		f := newFiltersForm(ModeBookmarks, trimconfig.Config{})
		f.fields[2].SetValue("48h")

		cfg, err := f.apply(trimconfig.Config{})

		require.NoError(t, err)
		require.NotNil(t, cfg.StaleAfter)
		assert.Equal(48*time.Hour, *cfg.StaleAfter)
	})
}

func TestStaleAfterString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    *time.Duration
		want string
	}{
		{name: "nil duration", d: nil, want: ""},
		{name: "whole hours drop the trailing 0m0s", d: new(2160 * time.Hour), want: "2160h"},
		{
			name: "non-hour-aligned falls back to String",
			d:    new(90 * time.Minute),
			want: "1h30m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, staleAfterString(tt.d))
		})
	}
}
