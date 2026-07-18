package jj

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ErrNoCannedResponse is returned when a Fake sees an args combination it
// has no Stdout/Errs entry for.
var ErrNoCannedResponse = errors.New("fake jj runner: no canned response")

// FakeCall records one invocation made against a Fake.
type FakeCall struct {
	Args []string
}

// Fake is a Runner backed by canned responses, keyed by the joined args
// string, for unit tests that shouldn't need a real jj binary.
type Fake struct {
	// Stdout maps a joined-args key (see Key) to the canned stdout Run
	// should return for that call.
	Stdout map[string]string
	// StdoutSeq is Stdout for a key whose successive calls need different
	// responses in order — e.g. two jj.LastOpID calls around a batch that
	// may or may not have advanced the op log (see internal/review's
	// runCascadeBatch). Consumed one element at a time per key; once
	// exhausted, further calls repeat the last element. Checked before
	// Stdout for the same key.
	StdoutSeq map[string][]string
	// Errs maps a joined-args key to the canned error Run/Stream should
	// return for that call.
	Errs map[string]error

	Calls []FakeCall

	// mu guards Calls and seqIndex: run.go's classifyForks calls two
	// jj.Log queries concurrently via errgroup, both against the same
	// Runner — without this, two goroutines mutating shared state at once
	// is a real data race (confirmed by go test -race), unlike ExecRunner,
	// which has no shared mutable state to race on.
	mu       sync.Mutex
	seqIndex map[string]int
}

var _ Runner = (*Fake)(nil)

// Key joins args the same way for both registering and matching canned
// responses.
func Key(args ...string) string {
	return strings.Join(args, "\x00")
}

// Run implements Runner by looking up a canned response for args.
func (f *Fake) Run(_ context.Context, args ...string) (string, error) {
	f.recordCall(args)

	key := Key(args...)
	if err, ok := f.Errs[key]; ok {
		return "", err
	}

	if out, ok := f.stdoutFor(key); ok {
		return out, nil
	}

	return "", fmt.Errorf("%w: %v", ErrNoCannedResponse, args)
}

// Stream implements Runner by writing a canned response for args to w.
func (f *Fake) Stream(_ context.Context, w io.Writer, args ...string) error {
	f.recordCall(args)

	key := Key(args...)
	if err, ok := f.Errs[key]; ok {
		return err
	}

	if out, ok := f.stdoutFor(key); ok {
		if _, err := io.WriteString(w, out); err != nil {
			return fmt.Errorf("fake jj runner: writing canned stdout: %w", err)
		}

		return nil
	}

	return fmt.Errorf("%w: %v", ErrNoCannedResponse, args)
}

// recordCall appends args to Calls under mu — see Fake.mu's doc comment for
// why this needs to be safe for concurrent use.
func (f *Fake) recordCall(args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Calls = append(f.Calls, FakeCall{Args: args})
}

// stdoutFor resolves key against StdoutSeq (advancing one step per call,
// repeating the last element once exhausted) before falling back to the
// plain Stdout map.
func (f *Fake) stdoutFor(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if seq, ok := f.StdoutSeq[key]; ok && len(seq) > 0 {
		idx := f.seqIndex[key]
		if idx >= len(seq) {
			idx = len(seq) - 1
		}

		if f.seqIndex == nil {
			f.seqIndex = make(map[string]int)
		}

		f.seqIndex[key] = idx + 1

		return seq[idx], true
	}

	out, ok := f.Stdout[key]

	return out, ok
}
