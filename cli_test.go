package main

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testVersion = "test"

// newTestParser builds a Kong parser for CLI without touching os.Exit —
// kong.Exit is overridden to a no-op so a --help/--version parse in a test
// can't terminate the test binary.
func newTestParser(t *testing.T) (*kong.Kong, *CLI) {
	t.Helper()

	var cli CLI

	parser, err := kong.New(&cli, kong.Name("jj-trim"), kong.Vars{kongVersionVar: testVersion},
		kong.Exit(func(int) {}))
	require.NoError(t, err)

	return parser, &cli
}

func TestCLI_BookmarksBare_DefaultsToPreview(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	ctx, err := parser.Parse([]string{cmdGroupBookmarks})
	require.NoError(t, err)
	assert.Equal(t, cmdGroupBookmarks+" "+cmdLeafPreview, ctx.Command())
}

func TestCLI_CommitsBare_DefaultsToPreview(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	ctx, err := parser.Parse([]string{cmdGroupCommits})
	require.NoError(t, err)
	assert.Equal(t, cmdGroupCommits+" "+cmdLeafPreview, ctx.Command())
}

func TestCLI_Bare_ResolvesToTui(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	ctx, err := parser.Parse([]string{})
	require.NoError(t, err)
	assert.Equal(t, cmdTui, ctx.Command())
}

func TestCLI_BareWithGlobalFlags_ResolvesToTui(t *testing.T) {
	t.Parallel()

	parser, cli := newTestParser(t)

	ctx, err := parser.Parse([]string{"-R", "/some/path"})
	require.NoError(t, err)
	assert.Equal(t, cmdTui, ctx.Command())
	assert.Equal(t, "/some/path", cli.Repository)
}

// TestCLI_ManFlag_PrintsWithoutError guards the mango-kong wiring
// (cli.go's Man field): --man's BeforeApply hook must run cleanly against
// the kong version this project actually uses (mango-kong itself targets
// an older kong release), not just compile.
func TestCLI_ManFlag_PrintsWithoutError(t *testing.T) {
	t.Parallel()

	var cli CLI

	var out strings.Builder

	parser, err := kong.New(&cli, kong.Name("jj-trim"), kong.Vars{kongVersionVar: testVersion},
		kong.Writers(&out, &out), kong.Exit(func(int) {}))
	require.NoError(t, err)

	_, err = parser.Parse([]string{"--man"})
	require.NoError(t, err)
	assert.Contains(t, out.String(), ".SH NAME")
}

// TestCLI_InstallCompletions_ListedInHelp guards the kongplete wiring: the
// command must actually be registered on CLI, not just compile-referenced.
// It doesn't invoke the command itself (that has real side effects —
// detecting the login shell, writing a completion snippet) — --help is a
// side-effect-free way to confirm it's wired into the grammar.
func TestCLI_InstallCompletions_ListedInHelp(t *testing.T) {
	t.Parallel()

	var cli CLI

	var out strings.Builder

	exited := false

	parser, err := kong.New(&cli, kong.Name("jj-trim"), kong.Vars{kongVersionVar: testVersion},
		kong.Writers(&out, &out), kong.Exit(func(int) { exited = true }))
	require.NoError(t, err)

	// A no-op Exit doesn't actually stop Kong's own control flow — like
	// Run()'s exitEarly, a Parse error after --help fires is expected and
	// must be ignored, not asserted against (see run.go's Run for the same
	// exitEarly-before-err check).
	_, err = parser.Parse([]string{"--help"})

	require.True(t, exited, "--help must trigger Kong's exit hook")

	if !exited {
		require.NoError(t, err)
	}

	assert.Contains(t, out.String(), "install-completions")
}

func TestCLI_BookmarksApply_ResolvesExplicitly(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	ctx, err := parser.Parse([]string{cmdGroupBookmarks, cmdLeafApply})
	require.NoError(t, err)
	assert.Equal(t, cmdGroupBookmarks+" "+cmdLeafApply, ctx.Command())
}

func TestCLI_BookmarksReview_ResolvesExplicitly(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	ctx, err := parser.Parse([]string{cmdGroupBookmarks, cmdLeafReview})
	require.NoError(t, err)
	assert.Equal(t, cmdGroupBookmarks+" "+cmdLeafReview, ctx.Command())
}

// TestCLI_OldBoolFlags_Rejected guards against a design that was
// explicitly reversed this session: --apply/--review used to be boolean
// flags on `bookmarks`/`commits` themselves, which meant nothing stopped
// both being passed at once. They're subcommands now, so the old flag
// spelling must be rejected by Kong, not silently ignored.
func TestCLI_OldBoolFlags_Rejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{
			name: cmdGroupBookmarks + " --" + cmdLeafApply,
			args: []string{cmdGroupBookmarks, "--" + cmdLeafApply},
		},
		{
			name: cmdGroupBookmarks + " --" + cmdLeafReview,
			args: []string{cmdGroupBookmarks, "--" + cmdLeafReview},
		},
		{
			name: cmdGroupCommits + " --" + cmdLeafReview,
			args: []string{cmdGroupCommits, "--" + cmdLeafReview},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parser, _ := newTestParser(t)

			_, err := parser.Parse(tc.args)
			assert.Errorf(
				t,
				err,
				"%v must be rejected now that apply/review are subcommands, not flags",
				tc.args,
			)
		})
	}
}

func TestCLI_TrunkFlag_InheritedByLeafSubcommands(t *testing.T) {
	t.Parallel()

	parser, cli := newTestParser(t)

	ctx, err := parser.Parse([]string{cmdGroupBookmarks, "--trunk", "main@origin", cmdLeafApply})
	require.NoError(t, err)
	assert.Equal(t, cmdGroupBookmarks+" "+cmdLeafApply, ctx.Command())
	assert.Equal(t, "main@origin", cli.Bookmarks.Trunk)
}

// TestCLI_StaleAfter_DistinguishesZeroFromUnset guards against a real
// footgun found this session: with a plain time.Duration field, passing
// --stale-after=0 was indistinguishable from not passing the flag at all
// (both are Go's zero value), so an explicit "everything is stale"
// request silently fell back to the 90-day default instead. StaleAfter is
// *time.Duration specifically so nil (unset) and a non-nil zero
// (explicitly requested) are different states.
func TestCLI_StaleAfter_DistinguishesZeroFromUnset(t *testing.T) {
	t.Parallel()

	parser, cli := newTestParser(t)

	_, err := parser.Parse([]string{cmdGroupBookmarks, cmdLeafPreview})
	require.NoError(t, err)
	assert.Nil(t, cli.Bookmarks.StaleAfter, "unset must be nil, not a zero duration")

	parser, cli = newTestParser(t)

	_, err = parser.Parse([]string{cmdGroupBookmarks, "--stale-after=0", cmdLeafPreview})
	require.NoError(t, err)
	require.NotNil(
		t,
		cli.Bookmarks.StaleAfter,
		"--stale-after=0 must be distinguishable from unset",
	)
	assert.Equal(t, time.Duration(0), *cli.Bookmarks.StaleAfter)
}

// TestCLI_StaleAfter_RejectsNegative guards against a negative
// --stale-after silently turning into "almost everything is stale" (since
// now.Sub(ts) > after is true for virtually any commit when after is
// negative) instead of failing fast on what's almost certainly a typo.
func TestCLI_StaleAfter_RejectsNegative(t *testing.T) {
	t.Parallel()

	parser, _ := newTestParser(t)

	_, err := parser.Parse([]string{cmdGroupBookmarks, "--stale-after=-1h", cmdLeafPreview})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale-after")
}
