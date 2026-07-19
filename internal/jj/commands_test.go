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

func TestLog_Error(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("log", "-r", "trunk()", "-T", "tmpl", "--no-graph"): errors.New("boom"),
		},
	}

	_, err := jj.Log(context.Background(), fake, "trunk()", "tmpl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
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

func TestLogPreview_Error(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): errors.New("boom"),
		},
	}

	var buf bytes.Buffer

	err := jj.LogPreview(context.Background(), fake, &buf, "candidates()", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestShow_Error(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("show", "abc123"): errors.New("no such change"),
		},
	}

	_, err := jj.Show(context.Background(), fake, "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such change")
}

const (
	bookmarkVerb = "bookmark"
	deleteVerb   = "delete"
)

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
				jj.Key(bookmarkVerb, deleteVerb, `exact:"a"`, `exact:"b"`): "",
			},
		}

		require.NoError(t, jj.BookmarkDelete(context.Background(), fake, []string{"a", "b"}))
		require.Len(t, fake.Calls, 1)
		assert.Equal(
			t,
			[]string{bookmarkVerb, deleteVerb, `exact:"a"`, `exact:"b"`},
			fake.Calls[0].Args,
		)
	})

	t.Run("names with glob-like characters are matched literally", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key(bookmarkVerb, deleteVerb, `exact:"release:1.0"`, `exact:"fix*"`): "",
			},
		}

		require.NoError(
			t,
			jj.BookmarkDelete(context.Background(), fake, []string{"release:1.0", "fix*"}),
		)
		require.Len(t, fake.Calls, 1)
		assert.Equal(
			t,
			[]string{bookmarkVerb, deleteVerb, `exact:"release:1.0"`, `exact:"fix*"`},
			fake.Calls[0].Args,
		)
	})

	t.Run("names with spaces are quoted", func(t *testing.T) {
		t.Parallel()

		name := "backup/refactor/slog-8-14-52 PM"
		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key(bookmarkVerb, deleteVerb, `exact:"`+name+`"`): "",
			},
		}

		require.NoError(t, jj.BookmarkDelete(context.Background(), fake, []string{name}))
		require.Len(t, fake.Calls, 1)
		assert.Equal(
			t,
			[]string{bookmarkVerb, deleteVerb, `exact:"` + name + `"`},
			fake.Calls[0].Args,
		)
	})

	t.Run("names with narrow no-break spaces are not \\u-escaped", func(t *testing.T) {
		t.Parallel()

		name := "backup/slog-8-31-14 PM"
		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key(bookmarkVerb, deleteVerb, `exact:"`+name+`"`): "",
			},
		}

		require.NoError(t, jj.BookmarkDelete(context.Background(), fake, []string{name}))
		require.Len(t, fake.Calls, 1)
		assert.Equal(
			t,
			[]string{bookmarkVerb, deleteVerb, `exact:"` + name + `"`},
			fake.Calls[0].Args,
		)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key(bookmarkVerb, deleteVerb, `exact:"a"`): errors.New("no such bookmark"),
			},
		}

		err := jj.BookmarkDelete(context.Background(), fake, []string{"a"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such bookmark")
	})
}

func TestExactRevsetTerm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain name", "feat", `exact:"feat"`},
		{"glob-like characters", "fix*", `exact:"fix*"`},
		{"embedded quote", `we"ird`, `exact:"we\"ird"`},
		{"embedded backslash", `back\slash`, `exact:"back\\slash"`},
		{"narrow no-break space", "8-31-14 PM", "exact:\"8-31-14 PM\""},
		{"embedded newline", "a\nb", `exact:"a\nb"`},
		{"embedded carriage return", "a\rb", `exact:"a\rb"`},
		{"embedded tab", "a\tb", `exact:"a\tb"`},
		{"embedded null byte", "a\x00b", `exact:"a\0b"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, jj.ExactRevsetTerm(tt.in))
		})
	}
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

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key("abandon", "x"): errors.New("cannot abandon immutable commit"),
			},
		}

		err := jj.Abandon(context.Background(), fake, []string{"x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot abandon immutable commit")
	})
}

func TestTrunkHistory(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("log", "-r", "::(trunk())", "--no-graph",
					"-T", `description ++ "\n---\n"`): "first\n---\nsecond\n---\n",
			},
		}

		out, err := jj.TrunkHistory(context.Background(), fake, "trunk()")
		require.NoError(t, err)
		assert.Equal(t, "first\n---\nsecond\n---\n", out)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key("log", "-r", "::(trunk())", "--no-graph",
					"-T", `description ++ "\n---\n"`): errors.New("bad revset"),
			},
		}

		_, err := jj.TrunkHistory(context.Background(), fake, "trunk()")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad revset")
	})
}

func TestGitFetch(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("git", "fetch"): "",
			},
		}

		require.NoError(t, jj.GitFetch(context.Background(), fake))
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key("git", "fetch"): errors.New("network unreachable"),
			},
		}

		err := jj.GitFetch(context.Background(), fake)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network unreachable")
	})
}

func TestLastOpID(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("op", "log", "--no-graph", "--limit", "1",
					"-T", "self.id().short() ++ \"\\n\""): "abc123\n",
			},
		}

		out, err := jj.LastOpID(context.Background(), fake)
		require.NoError(t, err)
		assert.Equal(t, "abc123", out)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key("op", "log", "--no-graph", "--limit", "1",
					"-T", "self.id().short() ++ \"\\n\""): errors.New("no operations"),
			},
		}

		_, err := jj.LastOpID(context.Background(), fake)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no operations")
	})
}

func TestOpShow(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Stdout: map[string]string{
				jj.Key("op", "show", "abc123"): "Deleted bookmark tags\n",
			},
		}

		out, err := jj.OpShow(context.Background(), fake, "abc123")
		require.NoError(t, err)
		assert.Equal(t, "Deleted bookmark tags\n", out)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		fake := &jj.Fake{
			Errs: map[string]error{
				jj.Key("op", "show", "abc123"): errors.New("no such operation"),
			},
		}

		_, err := jj.OpShow(context.Background(), fake, "abc123")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such operation")
	})
}
