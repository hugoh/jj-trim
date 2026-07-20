package jj

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// CachingRunner wraps a Runner and memoizes jj.Log-shaped calls
// (`log -r <revset> -T <template> --no-graph`) in-process, invalidating all
// of them at once whenever jj's operation-log head id changes — the signal
// that some operation, by this process or any other tool touching the
// repo, mutated state since the cache was populated. Every other call
// shape (Show, TrunkHistory, BookmarkDelete, Abandon, op log/show,
// LogPreview's Stream) passes straight through to Inner, uncached.
//
// Intended for long-lived sessions (internal/browse's TUI) where the same
// scan can otherwise repeat on every mode toggle or filter change; one-shot
// CLI invocations get no benefit from an in-process cache and shouldn't be
// wrapped with one.
type CachingRunner struct {
	Inner Runner

	mu      sync.Mutex
	opID    string
	primed  bool
	entries map[string]string
}

var _ Runner = (*CachingRunner)(nil)

// NewCachingRunner returns a CachingRunner wrapping inner.
func NewCachingRunner(inner Runner) *CachingRunner {
	return &CachingRunner{Inner: inner}
}

// Run implements Runner. Calls shaped like jj.Log are served from cache
// when the current op id matches the cache's; every other call passes
// straight through.
func (c *CachingRunner) Run(ctx context.Context, args ...string) (string, error) {
	revset, template, ok := logScanArgs(args)
	if !ok {
		out, err := c.Inner.Run(ctx, args...)
		if err != nil {
			return "", fmt.Errorf("caching runner: %w", err)
		}

		return out, nil
	}

	return c.scan(ctx, revset, template, args)
}

// Stream implements Runner. LogPreview's args never match logScanArgs'
// shape (it passes --no-pager, not -T), so streamed output is always a
// plain passthrough — it can't be cheaply memoized as a string anyway.
func (c *CachingRunner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	if err := c.Inner.Stream(ctx, w, args...); err != nil {
		return fmt.Errorf("caching runner: %w", err)
	}

	return nil
}

// logScanArgs reports whether args is exactly jj.Log's shape and, if so,
// extracts revset/template from their fixed positions.
func logScanArgs(args []string) (string, string, bool) {
	const logArgCount = 6

	if len(args) != logArgCount || args[0] != "log" || args[1] != "-r" ||
		args[3] != "-T" || args[5] != "--no-graph" {
		return "", "", false
	}

	return args[2], args[4], true
}

// scan serves revset/template from cache when the current op id matches the
// cached one, populating or invalidating the cache as needed. A failed op-id
// probe or a failed scan is never cached, so both fail closed and retry on
// the next call rather than replaying stale data or a stale error.
func (c *CachingRunner) scan(
	ctx context.Context, revset, template string, args []string,
) (string, error) {
	currentOp, err := CurrentOpID(ctx, c.Inner)
	if err != nil {
		return "", err
	}

	key := Key(revset, template)

	if out, hit := c.lookup(currentOp, key); hit {
		return out, nil
	}

	out, err := c.Inner.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("caching runner: %w", err)
	}

	c.store(currentOp, key, out)

	return out, nil
}

// lookup returns the cached entry for key if currentOp still matches the
// cache's op id, clearing the whole cache first if it doesn't (a global
// invalidation gate, since op id is a global signal, not a per-key one).
func (c *CachingRunner) lookup(currentOp, key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.primed || c.opID != currentOp {
		c.entries = make(map[string]string)
		c.opID = currentOp
		c.primed = true

		return "", false
	}

	out, hit := c.entries[key]

	return out, hit
}

// store records out under key, provided currentOp is still the cache's op
// id — it may have moved while the underlying scan was in flight.
func (c *CachingRunner) store(currentOp, key, out string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.opID != currentOp {
		return
	}

	c.entries[key] = out
}
