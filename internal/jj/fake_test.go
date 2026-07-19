package jj_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFake_ConcurrentRun_NoDataRace guards a real bug found in review:
// run.go's classifyForks runs two jj.Log queries concurrently via
// errgroup, both against the same Runner — jj.Fake.Run used to append to
// Calls with no synchronization, a data race confirmed by `go test -race`
// whenever a test exercised that path against a shared *Fake. Every
// goroutine here calls Run concurrently against one Fake; under
// `go test -race` this must not report a race, and every call must be
// recorded (an unsynchronized append racing across goroutines can silently
// drop entries even when it doesn't crash).
func TestFake_ConcurrentRun_NoDataRace(t *testing.T) {
	t.Parallel()

	const goroutines = 50

	fake := concurrentFake()
	n := concurrentCalls(goroutines, func() {
		_, err := fake.Run(context.Background(), "log")
		assert.NoError(t, err)
	})

	require.Len(t, fake.Calls, n)
}

// TestFake_StdoutSeq_ConsumesInOrderThenRepeatsLast guards StdoutSeq's
// contract: successive calls to the same key consume the sequence one
// element at a time, and once exhausted, further calls keep returning the
// last element rather than erroring — used by internal/review's tests to
// simulate an op id changing (or not) across two jj.LastOpID calls around
// a batch.
func TestFake_StdoutSeq_ConsumesInOrderThenRepeatsLast(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		StdoutSeq: map[string][]string{
			jj.Key("op", "log"): {"first\n", "second\n"},
		},
	}

	out, err := fake.Run(context.Background(), "op", "log")
	require.NoError(t, err)
	assert.Equal(t, "first\n", out)

	out, err = fake.Run(context.Background(), "op", "log")
	require.NoError(t, err)
	assert.Equal(t, "second\n", out)

	out, err = fake.Run(context.Background(), "op", "log")
	require.NoError(t, err)
	assert.Equal(t, "second\n", out, "further calls repeat the last element once exhausted")
}

// TestFake_StdoutSeq_TakesPriorityOverStdout guards that a key present in
// both StdoutSeq and Stdout resolves via StdoutSeq.
func TestFake_StdoutSeq_TakesPriorityOverStdout(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("op", "log"): "from-stdout\n",
		},
		StdoutSeq: map[string][]string{
			jj.Key("op", "log"): {"from-seq\n"},
		},
	}

	out, err := fake.Run(context.Background(), "op", "log")
	require.NoError(t, err)
	assert.Equal(t, "from-seq\n", out)
}

// TestFake_Run_NoCannedResponse guards Run's fallback branch: an args
// combination with neither a Stdout nor an Errs entry returns
// ErrNoCannedResponse.
func TestFake_Run_NoCannedResponse(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}

	out, err := fake.Run(context.Background(), "log")
	require.ErrorIs(t, err, jj.ErrNoCannedResponse)
	assert.Empty(t, out)
}

// TestFake_Stream_CannedError guards Stream's Errs branch: a canned error
// for the key is returned as-is, without writing anything to w.
func TestFake_Stream_CannedError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("stream failed")
	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("log"): wantErr,
		},
	}

	var buf strings.Builder

	err := fake.Stream(context.Background(), &buf, "log")
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, buf.String())
}

// TestFake_Stream_NoCannedResponse guards Stream's fallback branch: an args
// combination with neither a Stdout nor an Errs entry returns
// ErrNoCannedResponse.
func TestFake_Stream_NoCannedResponse(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{}

	var buf strings.Builder

	err := fake.Stream(context.Background(), &buf, "log")
	require.ErrorIs(t, err, jj.ErrNoCannedResponse)
	assert.Empty(t, buf.String())
}

func concurrentFake() *jj.Fake {
	return &jj.Fake{
		Stdout: map[string]string{jj.Key("log"): "output\n"},
	}
}

// concurrentCalls spawns n goroutines calling fn concurrently, waits for all
// to finish, and returns n (so the caller can assert on expected call counts).
func concurrentCalls(n int, fn func()) int {
	var wg sync.WaitGroup

	wg.Add(n)

	for range n {
		go func() {
			defer wg.Done()

			fn()
		}()
	}

	wg.Wait()

	return n
}

// TestFake_ConcurrentStream_NoDataRace is TestFake_ConcurrentRun_NoDataRace
// for Stream, the other Fake method that appends to Calls.
func TestFake_ConcurrentStream_NoDataRace(t *testing.T) {
	t.Parallel()

	const goroutines = 50

	fake := concurrentFake()
	n := concurrentCalls(goroutines, func() {
		var buf strings.Builder

		assert.NoError(t, fake.Stream(context.Background(), &buf, "log"))
	})

	require.Len(t, fake.Calls, n)
}
