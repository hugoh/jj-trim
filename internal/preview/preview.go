// Package preview builds and prints the annotated `jj log` preview graph
// shared by jj-trim's `bookmarks` and `commits` commands: jj's own
// unmodified graph output, followed by a legend built from
// internal/classify's structured candidate data. It never re-renders or
// re-derives jj's graph.
package preview

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/tty"
)

// Print streams jj's own graph for revset to out, then a legend built from
// legend, then (if explain is true) a "Details:" section with one paragraph
// per distinct Reason present in legend, in first-seen order. Color is
// decided by checking whether out is itself a terminal — this package never
// trusts jj's own auto-detection, since out may be wrapped by jj-trim's own
// subprocess plumbing.
func Print(
	ctx context.Context,
	r jj.Runner,
	out io.Writer,
	revset string,
	legend []classify.LegendEntry,
	explain bool,
) error {
	color := isTerminal(out)

	if err := jj.LogPreview(ctx, r, out, revset, color); err != nil {
		return fmt.Errorf("printing preview graph: %w", err)
	}

	if len(legend) == 0 {
		return nil
	}

	// The bracketed letter every entry.String() line carries is explained
	// once here, rather than spelled out per line — see LegendEntry.String.
	legendHeader := "\nLegend: ([H]/[M]/[L] = confidence it's safe to delete: high/medium/low)"
	if _, err := fmt.Fprintln(out, legendHeader); err != nil {
		return fmt.Errorf("printing legend: %w", err)
	}

	for _, entry := range legend {
		if _, err := fmt.Fprintln(out, "  "+entry.String()); err != nil {
			return fmt.Errorf("printing legend: %w", err)
		}
	}

	if !explain {
		return nil
	}

	return printDetails(out, legend)
}

// printDetails prints one paragraph per distinct Reason present in legend
// (first-seen order), giving each entry's Confidence/Short/Long text — the
// expanded explanation behind the Legend's one-line-per-candidate text.
func printDetails(out io.Writer, legend []classify.LegendEntry) error {
	if _, err := fmt.Fprintln(out, "\nDetails:"); err != nil {
		return fmt.Errorf("printing details: %w", err)
	}

	seen := make(map[classify.Reason]bool, len(legend))

	for _, entry := range legend {
		if seen[entry.Reason] {
			continue
		}

		seen[entry.Reason] = true

		info := classify.Describe(entry.Reason)

		_, err := fmt.Fprintf(
			out,
			"  [%s] %s\n      %s\n",
			info.Confidence.Letter(),
			info.Short,
			info.Long,
		)
		if err != nil {
			return fmt.Errorf("printing details: %w", err)
		}
	}

	return nil
}

// isTerminal reports whether w is a real terminal, used to decide
// --color=always vs --color=never for the underlying jj log call.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)

	return ok && tty.IsTerminal(f)
}
