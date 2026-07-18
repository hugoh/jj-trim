// Package spin shows an indeterminate spinner on stderr while a blocking
// call runs — for jj-trim's plain CLI commands (bookmarks/commits
// preview|review), which run outside the Bubbletea TUI but still shell out
// to jj and would otherwise print nothing until classification finishes.
// Never touches stdout — that's the actual command output and must stay
// pipeable.
package spin

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/hugoh/jj-trim/internal/tty"
)

// tickInterval is spinner.Dot's own frame rate by default, swapped to a
// much shorter interval in this package's own tests so a short-lived fake
// fn is guaranteed to observe at least one frame without a flaky real-time
// race against the default ~100ms rate.
//
//nolint:gochecknoglobals // see above
var tickInterval = spinner.Dot.FPS

// Run shows an indeterminate spinner on stderr while fn runs, then erases
// it — but only if stderr is a real terminal (checked the same way
// internal/tty/internal/preview already do). Otherwise fn runs with zero
// output side effects, so piped/redirected/CI usage (including every
// existing test of the callers of Run, none of which run with a real
// terminal stderr) is completely unaffected.
func Run(stderr io.Writer, label string, fn func() error) error {
	f, ok := stderr.(*os.File)
	if !ok || !tty.IsTerminal(f) {
		return fn()
	}

	var wg sync.WaitGroup

	done := make(chan struct{})

	wg.Go(func() {
		animate(f, label, done)
	})

	err := fn()

	close(done)
	wg.Wait() // wait for the erase-write to land before the real output follows

	return err
}

// animate drives spinner.Dot's frames on a time.Ticker (set to
// tickInterval, not spinner.Dot.FPS directly — that indirection is exactly
// what makes it swappable in tests) until done closes, then erases the
// line so the next real output starts at column 0 cleanly.
func animate(w io.Writer, label string, done <-chan struct{}) {
	frames := spinner.Dot.Frames

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	var maxWidth int

	index := 0

	for {
		select {
		case <-done:
			erase(w, maxWidth)

			return
		case <-ticker.C:
			line := frames[index%len(frames)] + " " + label
			if len(line) > maxWidth {
				maxWidth = len(line)
			}

			_, _ = fmt.Fprint(w, "\r"+line)
			index++
		}
	}
}

// erase overwrites width columns of the current line with spaces, then
// returns the cursor to column 0 — leaving no visible spinner residue.
func erase(w io.Writer, width int) {
	_, _ = fmt.Fprint(w, "\r"+strings.Repeat(" ", width)+"\r")
}
