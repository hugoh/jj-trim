package jj

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Log runs `jj log` against revset with the given -T template and returns
// captured stdout for parsing.
func Log(ctx context.Context, r Runner, revset, template string) (string, error) {
	out, err := r.Run(ctx, "log", "-r", revset, "-T", template, "--no-graph")
	if err != nil {
		return "", fmt.Errorf("jj log: %w", err)
	}

	return out, nil
}

// LogPreview streams jj's own rendered graph for revset straight to w,
// using jj's default template. color decides --color=always vs
// --color=never; the caller (internal/preview) is responsible for deciding
// it by checking its own stdout, since this package never inspects a
// terminal itself.
func LogPreview(ctx context.Context, r Runner, w io.Writer, revset string, color bool) error {
	args := []string{"log", "-r", revset, "--no-pager"}
	if color {
		args = append(args, "--color=always")
	} else {
		args = append(args, "--color=never")
	}

	if err := r.Stream(ctx, w, args...); err != nil {
		return fmt.Errorf("jj log (preview): %w", err)
	}

	return nil
}

// Show returns `jj show`'s output (description + full diff) for a change.
func Show(ctx context.Context, r Runner, changeID string) (string, error) {
	out, err := r.Run(ctx, "show", changeID)
	if err != nil {
		return "", fmt.Errorf("jj show: %w", err)
	}

	return out, nil
}

// ExactRevsetTerm renders name as a literal `exact:"..."` revset term.
// Deliberately not fmt's %q: it \uXXXX-escapes runes like U+202F (seen in
// macOS backup-bookmark names), an escape jj's revset grammar can't parse.
func ExactRevsetTerm(name string) string {
	var b strings.Builder

	b.WriteString(`exact:"`)

	for _, r := range name {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')

	return b.String()
}

// BookmarkDelete deletes the named bookmarks in a single batch call. Each
// name is prefixed with "exact:" since jj 0.36.0 parses `bookmark delete`
// arguments as string patterns (glob by default), not literal names — a
// bookmark named e.g. "release:1.0" or containing "*" would otherwise be
// misinterpreted instead of matched literally.
func BookmarkDelete(ctx context.Context, r Runner, names []string) error {
	if len(names) == 0 {
		return nil
	}

	args := []string{"bookmark", "delete"}
	for _, name := range names {
		args = append(args, ExactRevsetTerm(name))
	}

	if _, err := r.Run(ctx, args...); err != nil {
		return fmt.Errorf("jj bookmark delete: %w", err)
	}

	return nil
}

// Abandon abandons the given change IDs in a single batch call, so the
// whole batch lands as one entry in `jj op log`. Each element is passed to
// `jj abandon` positionally, which evaluates each as a revset expression
// (not necessarily a literal change id) and unions the results — callers
// may pass a revset string covering a whole private commit chain, not just
// a single id (see classify.PrivateChainRevset).
func Abandon(ctx context.Context, r Runner, changeIDs []string) error {
	if len(changeIDs) == 0 {
		return nil
	}

	args := append([]string{"abandon"}, changeIDs...)

	if _, err := r.Run(ctx, args...); err != nil {
		return fmt.Errorf("jj abandon: %w", err)
	}

	return nil
}

// TrunkHistory returns every commit description in trunk's ancestry,
// separated by a "---" marker line, so classify.Candidate.ProbablyMerged
// can do a local substring search rather than one jj call per bookmark
// candidate being checked.
func TrunkHistory(ctx context.Context, r Runner, trunk string) (string, error) {
	out, err := r.Run(ctx, "log", "-r", fmt.Sprintf("::(%s)", trunk), "--no-graph",
		"-T", `description ++ "\n---\n"`)
	if err != nil {
		return "", fmt.Errorf("jj log (trunk history): %w", err)
	}

	return out, nil
}

// GitFetch runs `jj git fetch`, which also auto-deletes stray bookmarks
// whose tracked remote counterpart was deleted upstream.
func GitFetch(ctx context.Context, r Runner) error {
	if _, err := r.Run(ctx, "git", "fetch"); err != nil {
		return fmt.Errorf("jj git fetch: %w", err)
	}

	return nil
}

// LastOpID returns the id of the most recent operation in `jj op log`, so
// callers can report it as the undo point for a batch of changes just
// applied (per DESIGN.md's op-log-as-safety-net pattern).
func LastOpID(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(
		ctx,
		"op",
		"log",
		"--no-graph",
		"--limit",
		"1",
		"-T",
		"self.id().short() ++ \"\\n\"",
	)
	if err != nil {
		return "", fmt.Errorf("jj op log: %w", err)
	}

	return strings.TrimSpace(out), nil
}

// CurrentOpID returns the id of the most recent operation, like LastOpID,
// but passes --ignore-working-copy so the check itself never triggers a
// working-copy snapshot operation. That distinction matters for
// CachingRunner: if the freshness probe could append its own op-log entry,
// every check would see a "mutation" and invalidate the cache it just
// populated. LastOpID's post-mutation undo-hint callers don't need this,
// since a real snapshot there is already accounted for by the batch that
// just ran.
func CurrentOpID(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(
		ctx,
		"op",
		"log",
		"--ignore-working-copy",
		"--no-graph",
		"--limit",
		"1",
		"-T",
		"self.id().short() ++ \"\\n\"",
	)
	if err != nil {
		return "", fmt.Errorf("jj op log: %w", err)
	}

	return strings.TrimSpace(out), nil
}

// OpShow returns `jj op show`'s rendering of opID — the operation's own
// description plus the diff of what it actually changed (e.g. "Deleted
// bookmark tags" or the abandoned commit's summary) — used by review's
// applied popup to show what a batch just did, not merely that it ran.
// No explicit --color flag, matching Show above: jj already disables color
// on its own once stdout isn't a terminal (true of every subprocess call
// this package makes).
func OpShow(ctx context.Context, r Runner, opID string) (string, error) {
	out, err := r.Run(ctx, "op", "show", opID)
	if err != nil {
		return "", fmt.Errorf("jj op show: %w", err)
	}

	return out, nil
}
