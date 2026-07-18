package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/alecthomas/kong"
	mangokong "github.com/alecthomas/mango-kong"
	"github.com/willabides/kongplete"
)

// errNegativeStaleAfter is BookmarksCmd.Validate's error when --stale-after
// is negative.
var errNegativeStaleAfter = errors.New("--stale-after must not be negative")

// CLI is jj-trim's flag surface. See DESIGN.md's "CLI surface (sketch)"
// section — this struct is the literal implementation of that sketch.
type CLI struct {
	Repository string `help:"Path to repository to operate on (default: current directory)" short:"R"`
	Fetch      bool   `help:"Run jj git fetch first"`

	Bookmarks BookmarksCmd `cmd:"" help:"Delete bookmarks already merged into trunk"`
	Commits   CommitsCmd   `cmd:"" help:"Clean up abandoned anonymous commits"`

	// Tui is the default when no subcommand is given: bare `jj-trim` opens
	// the interactive front end (internal/browse) covering everything the
	// subcommands above do, driven by the same underlying functions rather
	// than a second implementation — see run.go's runTui. Hidden from
	// --help since "just run jj-trim" is the documented entry point, not
	// an explicit `jj-trim tui` invocation.
	Tui TuiCmd `cmd:"" default:"1" help:"Launch the interactive TUI (default)" hidden:""`

	// InstallCompletions installs shell completions (bash/zsh/fish) for
	// jj-trim itself — see kongplete.Complete's wiring in run.go, which
	// intercepts COMP_LINE-style completion requests before Parse runs.
	InstallCompletions kongplete.InstallCompletions `cmd:"" help:"Install shell completions"`

	Man     mangokong.ManFlag `help:"Print man page and exit"`
	Version kong.VersionFlag  `help:"Print version and exit"`
}

// TuiCmd has no flags of its own — every setting it would otherwise take is
// gathered interactively (internal/browse's settings overlay) instead.
type TuiCmd struct{}

// BookmarksCmd cleans up bookmarks already merged into trunk (DESIGN.md's
// Part 1). Preview/Apply/Review are subcommands, not flags, so the three
// modes are mutually exclusive by construction rather than by convention —
// an earlier draft used `--apply`/`--review` bools, which nothing stopped
// from both being set at once. Preview is `default:"1"` so a bare
// `jj-trim bookmarks` still resolves to it, preserving the
// non-destructive-by-default invariant without needing an explicit flag.
type BookmarksCmd struct {
	Protected []string `help:"Bookmark name globs never deleted (default: none)" short:"p"`
	Trunk     string   `help:"Override trunk() revset (default: trunk())"        short:"t"`
	// *time.Duration, not time.Duration: distinguishes "flag not passed"
	// (nil) from "explicitly passed --stale-after=0" (non-nil, zero) —
	// a plain time.Duration can't tell those apart, since Go's zero value
	// for the type IS zero, silently falling back to the default even
	// when the user explicitly asked for zero.
	StaleAfter *time.Duration `help:"Age threshold for the 'stale, no remote' heuristic (default: 2160h/90d)"`
	Explain    bool           `help:"Print a Details section explaining each finding reason"`

	Preview struct{} `cmd:"" default:"1" help:"Show what would be deleted (default)"`

	Apply  struct{} `cmd:"" help:"Delete merged bookmarks"`
	Review struct{} `cmd:"" help:"Interactive walk over merged/probably-merged/stale bookmarks"`
}

// Validate implements kong's per-command validation hook, run during Parse.
func (c *BookmarksCmd) Validate() error {
	if c.StaleAfter != nil && *c.StaleAfter < 0 {
		return fmt.Errorf("%w: got %s", errNegativeStaleAfter, *c.StaleAfter)
	}

	return nil
}

// CommitsCmd cleans up anonymous commit forks that aren't on any bookmark
// (DESIGN.md's Part 2). See BookmarksCmd's doc comment for why Preview/
// Review are subcommands rather than flags.
type CommitsCmd struct {
	NoDescriptionOnly bool `help:"Within commits, restrict to description(\"\")"`
	Explain           bool `help:"Print a Details section explaining each finding reason"`

	Preview struct{} `cmd:"" default:"1" help:"Show what would be abandoned (default)"`

	Review struct{} `cmd:"" help:"Interactive walk over anonymous fork candidates"`
}
