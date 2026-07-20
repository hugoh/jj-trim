package jj_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	logCmd       = "log"
	revsetA      = "trunk()..@"
	revsetB      = "bookmarks()"
	logTemplate  = `change_id ++ "\n"`
	outA         = "aaa\n"
	outB         = "bbb\n"
	op1          = "op1\n"
	op2          = "op2\n"
	opTemplate   = "self.id().short() ++ \"\\n\""
	noGraphFlag  = "--no-graph"
	limitOneFlag = "1"
)

func logArgs(revset string) []string {
	return []string{logCmd, "-r", revset, "-T", logTemplate, noGraphFlag}
}

func opProbeKey() string {
	return jj.Key(
		"op", logCmd, "--ignore-working-copy", noGraphFlag,
		"--limit", limitOneFlag, "-T", opTemplate,
	)
}

func newFakeAtOp1() *jj.Fake {
	return &jj.Fake{
		Stdout: map[string]string{
			opProbeKey(): op1,
		},
	}
}

func callCount(f *jj.Fake, args []string) int {
	key := jj.Key(args...)

	n := 0

	for _, c := range f.Calls {
		if jj.Key(c.Args...) == key {
			n++
		}
	}

	return n
}

func TestCachingRunner_SecondIdenticalScan_ReturnsCachedWithoutCallingLog(t *testing.T) {
	t.Parallel()

	fake := newFakeAtOp1()
	fake.Stdout[jj.Key(logArgs(revsetA)...)] = outA

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	out1, err := c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)

	out2, err := c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)

	assert.Equal(t, outA, out1)
	assert.Equal(t, outA, out2)
	assert.Equal(t, 1, callCount(fake, logArgs(revsetA)))
}

func TestCachingRunner_OpIDChanges_TriggersRescan(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		StdoutSeq: map[string][]string{
			opProbeKey(): {op1, op2},
		},
		Stdout: map[string]string{
			jj.Key(logArgs(revsetA)...): outA,
		},
	}

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	_, err := c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)

	_, err = c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)

	assert.Equal(t, 2, callCount(fake, logArgs(revsetA)))
}

func TestCachingRunner_DifferentRevsetsSameOpID_BothCachedIndependently(t *testing.T) {
	t.Parallel()

	fake := newFakeAtOp1()
	fake.Stdout[jj.Key(logArgs(revsetA)...)] = outA
	fake.Stdout[jj.Key(logArgs(revsetB)...)] = outB

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	for range 2 {
		got, err := c.Run(ctx, logArgs(revsetA)...)
		require.NoError(t, err)
		assert.Equal(t, outA, got)

		got, err = c.Run(ctx, logArgs(revsetB)...)
		require.NoError(t, err)
		assert.Equal(t, outB, got)
	}

	assert.Equal(t, 1, callCount(fake, logArgs(revsetA)))
	assert.Equal(t, 1, callCount(fake, logArgs(revsetB)))
}

func TestCachingRunner_NonLogShapedCalls_AlwaysPassThroughUncached(t *testing.T) {
	t.Parallel()

	showArgs := []string{"show", "abc123"}
	trunkHistoryArgs := []string{
		logCmd, "-r", "::(trunk())", noGraphFlag, "-T", `description ++ "\n---\n"`,
	}
	opLogArgs := []string{"op", logCmd, noGraphFlag, "--limit", limitOneFlag, "-T", opTemplate}
	opShowArgs := []string{"op", "show", "abc123"}

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(showArgs...):         "show output\n",
			jj.Key(trunkHistoryArgs...): "trunk history\n",
			jj.Key(opLogArgs...):        op1,
			jj.Key(opShowArgs...):       "op show output\n",
		},
	}

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	for range 2 {
		_, err := c.Run(ctx, showArgs...)
		require.NoError(t, err)

		_, err = c.Run(ctx, trunkHistoryArgs...)
		require.NoError(t, err)

		_, err = c.Run(ctx, opLogArgs...)
		require.NoError(t, err)

		_, err = c.Run(ctx, opShowArgs...)
		require.NoError(t, err)
	}

	for _, args := range [][]string{showArgs, trunkHistoryArgs, opLogArgs, opShowArgs} {
		assert.Equal(t, 2, callCount(fake, args), "always passthrough: %v", args)
	}
}

func TestCachingRunner_InnerLogError_NotCached(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	fake := newFakeAtOp1()
	fake.Errs = map[string]error{
		jj.Key(logArgs(revsetA)...): wantErr,
	}

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	_, err := c.Run(ctx, logArgs(revsetA)...)
	require.ErrorIs(t, err, wantErr)

	// Fix the canned response and retry: a failed scan must not have been
	// cached as a permanent failure.
	delete(fake.Errs, jj.Key(logArgs(revsetA)...))
	fake.Stdout[jj.Key(logArgs(revsetA)...)] = outA

	out, err := c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)
	assert.Equal(t, outA, out)
	assert.Equal(t, 2, callCount(fake, logArgs(revsetA)))
}

func TestCachingRunner_CurrentOpIDError_Propagates(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("op log failed")

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key(logArgs(revsetA)...): outA,
			opProbeKey():                op1,
		},
	}

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	_, err := c.Run(ctx, logArgs(revsetA)...)
	require.NoError(t, err)

	// Now make the op-id probe fail: a cache hit must not be served despite
	// having a valid cached entry from the prior op id.
	delete(fake.Stdout, opProbeKey())
	fake.Errs = map[string]error{opProbeKey(): wantErr}

	_, err = c.Run(ctx, logArgs(revsetA)...)
	require.ErrorIs(t, err, wantErr)
}

func TestCachingRunner_ConcurrentScans_NoRace(t *testing.T) {
	t.Parallel()

	fake := newFakeAtOp1()
	fake.Stdout[jj.Key(logArgs(revsetA)...)] = outA
	fake.Stdout[jj.Key(logArgs(revsetB)...)] = outB

	c := jj.NewCachingRunner(fake)
	ctx := context.Background()

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		_, err := c.Run(ctx, logArgs(revsetA)...)
		assert.NoError(t, err)
	}()

	go func() {
		defer wg.Done()

		_, err := c.Run(ctx, logArgs(revsetB)...)
		assert.NoError(t, err)
	}()

	wg.Wait()
}
