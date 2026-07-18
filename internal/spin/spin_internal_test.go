package spin

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hugoh/jj-trim/internal/tty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFakeTerminal swaps tty.IsTerminal for a fake that always returns
// want, restoring the original on cleanup (there's no portable way to fake
// "is a real terminal" for an arbitrary *os.File without an actual pty).
// Not run in parallel with other tests in this package: it mutates shared
// package state (and internal/tty's, which this package's Run now calls
// into directly).
func withFakeTerminal(t *testing.T, want bool) {
	t.Helper()

	original := tty.IsTerminal
	tty.IsTerminal = func(*os.File) bool { return want }

	t.Cleanup(func() { tty.IsTerminal = original })
}

// withFastTicks lowers tickInterval so a short-lived fn reliably observes
// at least one frame without racing the real ~100ms spinner.Dot.FPS.
func withFastTicks(t *testing.T) {
	t.Helper()

	original := tickInterval
	tickInterval = time.Millisecond

	t.Cleanup(func() { tickInterval = original })
}

// This test does not mutate package-level state (it passes a bytes.Buffer
// to Run, so isTerminal is never called) — safe to parallelize even though
// sibling tests in this package swap globals.
func TestRun_NotAFile_PassthroughOnly(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	calls := 0
	err := Run(&out, "loading", func() error {
		calls++

		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls, "fn must run exactly once")
	assert.Empty(t, out.String(), "no spinner output when stderr isn't an *os.File at all")
}

// Not run in parallel: mutates shared package state (isTerminal).
func TestRun_FileButNotTerminal_PassthroughOnly(t *testing.T) {
	withFakeTerminal(t, false)

	r, w, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	sentinel := errors.New("boom")

	calls := 0
	err = Run(w, "loading", func() error {
		calls++

		return sentinel
	})

	require.ErrorIs(t, err, sentinel, "fn's error must propagate unchanged")
	assert.Equal(t, 1, calls)

	require.NoError(t, w.Close())

	assert.Empty(
		t,
		readAllNonBlocking(r),
		"no spinner output when stderr is a real file but not a terminal",
	)
}

// TestRun_Terminal_WritesAndErasesFrames guards the actual spinner
// behavior: when stderr fakes as a real terminal, Run must write at least
// one spinner frame while fn is running, and must fully erase the line
// (leaving only carriage returns / blanks, no visible residue) before
// returning.
//
// Not run in parallel: mutates shared package state (isTerminal, tickInterval).
func TestRun_Terminal_WritesAndErasesFrames(t *testing.T) {
	withFakeTerminal(t, true)
	withFastTicks(t)

	r, w, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = r.Close() }()

	readDone := make(chan string, 1)

	go func() {
		readDone <- readAllBlocking(r)
	}()

	err = Run(w, "loading", func() error {
		time.Sleep(20 * time.Millisecond) // let a few 1ms ticks fire

		return nil
	})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	written := <-readDone

	assert.Contains(t, written, "\r", "must use carriage returns to redraw in place")
	assert.Contains(t, written, "loading", "must show the caller's label")

	hasFrame := false

	for _, f := range []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"} {
		if strings.Contains(written, f) {
			hasFrame = true

			break
		}
	}

	assert.True(t, hasFrame, "must draw at least one spinner.Dot frame")

	lastLine := written[strings.LastIndex(written, "\r")+1:]
	assert.Empty(t, strings.TrimSpace(lastLine), "must leave no visible residue after erasing")
}

// readAllNonBlocking reads whatever's already buffered on f without
// blocking for more — an EOF/no-data-yet result from a closed-writer pipe
// end just means "nothing was written," not a test failure.
func readAllNonBlocking(f *os.File) string {
	buf := make([]byte, 4096)

	n, _ := f.Read(buf)

	return string(buf[:n])
}

// readAllBlocking reads f until EOF (the write end closing) — expected,
// not a failure.
func readAllBlocking(f *os.File) string {
	var out bytes.Buffer

	buf := make([]byte, 4096)

	for {
		n, err := f.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}

		if err != nil {
			return out.String()
		}
	}
}
