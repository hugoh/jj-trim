// Package trimconfig defines the settings jj-trim's bookmarks/commits
// classification depends on — trunk override, protected globs, staleness
// threshold, description filter, repository path, fetch-first — as one
// struct. Both Kong's CLI-flag parse and internal/browse's interactive
// settings overlay populate a Config identically, so the classification and
// action code downstream doesn't need to know which path produced it.
package trimconfig

import "time"

// Config mirrors BookmarksCmd/CommitsCmd's flags today: Trunk/StaleAfter
// use the same "empty/nil means default" contract as their CLI flag
// counterparts (see cli.go), rather than having the default baked in here,
// so a settings UI can display "using default" instead of a value that
// looks arbitrarily chosen.
type Config struct {
	Repository string
	Fetch      bool

	// Bookmarks-specific.
	Protected  []string
	Trunk      string
	StaleAfter *time.Duration

	// Commits-specific.
	NoDescriptionOnly bool
}
