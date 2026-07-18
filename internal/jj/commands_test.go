package jj_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emptyNoop(t *testing.T, fn func(context.Context, jj.Runner, []string) error) {
	t.Helper()

	fake := &jj.Fake{}
	require.NoError(t, fn(context.Background(), fake, nil))
	assert.Empty(t, fake.Calls)
}

func TestLog(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "trunk()", "-T", "tmpl", "--no-graph"): `{"id":"abc"}` + "\n",
		},
	}

	out, err := jj.Log(context.Background(), fake, "trunk()", "tmpl")
	require.NoError(t, err)
	// Raw passthrough check (incl. trailing newline), not JSON-value
	// equality, so assert.JSONEq would be the wrong tool here.
	assert.Equal(t, `{"id":"abc"}`+"\n", out) //nolint:testifylint
}

func TestLogPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		color bool
		want  string
	}{
		{"color on", true, "--color=always"},
		{"color off", false, "--color=never"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &jj.Fake{
				Stdout: map[string]string{
					jj.Key("log", "-r", "candidates()", "--no-pager", tt.want): "graph output",
				},
			}

			var buf bytes.Buffer

			err := jj.LogPreview(context.Background(), fake, &buf, "candidates()", tt.color)
			require.NoError(t, err)
			assert.Equal(t, "graph output", buf.String())
		})
	}
}

func TestShow(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("show", "abc123"): "Change abc123\n",
		},
	}

	out, err := jj.Show(context.Background(), fake, "abc123")
	require.NoError(t, err)
	assert.Equal(t, "Change abc123\n", out)
}

func TestBookmarkDelete(t *testing.T) {
	t.Parallel()

	t.Run("empty is a no-op", func(t *testing.T) {
		t.Parallel()
		emptyNoop(t, jj.BookmarkDelete)
	})

	t.Run("single batch call", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("bookmark", "delete", "exact:a", "exact:b"): "",
			},
		}

		require.NoError(t, jj.BookmarkDelete(context.Background(), fake, []string{"a", "b"}))
		require.Len(t, fake.Calls, 1)
		assert.Equal(t, []string{"bookmark", "delete", "exact:a", "exact:b"}, fake.Calls[0].Args)
	})

	t.Run("names with glob-like characters are matched literally", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("bookmark", "delete", "exact:release:1.0", "exact:fix*"): "",
			},
		}

		require.NoError(
			t,
			jj.BookmarkDelete(context.Background(), fake, []string{"release:1.0", "fix*"}),
		)
		require.Len(t, fake.Calls, 1)
		assert.Equal(
			t,
			[]string{"bookmark", "delete", "exact:release:1.0", "exact:fix*"},
			fake.Calls[0].Args,
		)
	})
}

func TestAbandon(t *testing.T) {
	t.Parallel()

	t.Run("empty is a no-op", func(t *testing.T) {
		t.Parallel()
		emptyNoop(t, jj.Abandon)
	})

	t.Run("single batch call", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("abandon", "x", "y"): "",
			},
		}

		require.NoError(t, jj.Abandon(context.Background(), fake, []string{"x", "y"}))
		require.Len(t, fake.Calls, 1)
		assert.Equal(t, []string{"abandon", "x", "y"}, fake.Calls[0].Args)
	})
}

func TestGitFetch(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("git", "fetch"): errors.New("network unreachable"),
		},
	}

	err := jj.GitFetch(context.Background(), fake)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network unreachable")
}
