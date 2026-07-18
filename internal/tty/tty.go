// Package tty checks whether a file descriptor is a real interactive
// terminal, shared by any full-screen Bubbletea entry point (internal/review,
// internal/browse) that can't usefully run against a non-TTY stdin or
// stdout.
package tty

import (
	"errors"
	"os"

	"golang.org/x/term"
)

// ErrNotInteractive is returned by Require when stdin or stdout isn't a
// terminal.
var ErrNotInteractive = errors.New("requires an interactive terminal")

// IsTerminal is swapped out in tests — there's no portable way to fake "is
// a real terminal" for an arbitrary *os.File without an actual pty, and CI
// doesn't reliably have one attached. Shared by internal/spin and
// internal/preview so there's one terminal-detection check, not three.
//
//nolint:gochecknoglobals // see above
var IsTerminal = func(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// Require fails fast if stdin or stdout isn't a real terminal, rather than
// launching a Bubbletea program that can never receive input (stdin) or
// would dump raw ANSI/alt-screen escape sequences into a non-terminal
// destination like a redirected file or pipe (stdout).
func Require(stdin, stdout *os.File) error {
	if !IsTerminal(stdin) {
		return ErrNotInteractive
	}

	if !IsTerminal(stdout) {
		return ErrNotInteractive
	}

	return nil
}
