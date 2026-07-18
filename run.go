package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/hugoh/jj-trim/internal/browse"
	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/preview"
	"github.com/hugoh/jj-trim/internal/review"
	"github.com/hugoh/jj-trim/internal/spin"
	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/hugoh/jj-trim/internal/tty"
	"github.com/willabides/kongplete"
	"golang.org/x/sync/errgroup"
)

// errUnknownCommand should be unreachable: Kong only ever passes a command
// string it validated during Parse.
var errUnknownCommand = errors.New("unknown command")

// Subcommand group/leaf names, as kong.Context.Command() spells them —
// shared between dispatch (run) and tests (cli_test.go) so there's one
// source of truth for the literal strings.
const (
	cmdGroupBookmarks = "bookmarks"
	cmdGroupCommits   = "commits"

	cmdLeafPreview = "preview"
	cmdLeafApply   = "apply"
	cmdLeafReview  = "review"

	// cmdTui is the command string kongCtx.Command() reports for a bare
	// invocation (Kong's default:"1" top-level command, see cli.go's
	// TuiCmd) — it has no children, so it's a leaf name on its own, never
	// a "group action" pair, and is special-cased before run()'s
	// strings.Cut(command, " ") group split.
	cmdTui = "tui"
)

// Exit codes. Simplified from a multi-repo tool's three-way scheme since
// jj-trim has no "ran across many repos, some failed" concept.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// defaultStaleAfter is BookmarksCmd.StaleAfter's default when unset.
const defaultStaleAfter = 90 * 24 * time.Hour

// defaultTrunkRevset is BookmarksCmd.Trunk's/trimconfig.Config.Trunk's
// default when unset.
const defaultTrunkRevset = "trunk()"

// review.Action verb/past-tense literals, shared by every review.Action
// built in this file (CLI dispatch and runTui's browse sessions alike) so
// there's one spelling of each rather than several copies that could drift.
const (
	verbDelete    = "delete"
	pastDeleted   = "deleted"
	verbAbandon   = "abandon"
	pastAbandoned = "abandoned"
)

// kongVersionVar is the kong.Vars key kong.VersionFlag reads its printed
// value from — shared with cli_test.go's parser constructions so there's
// one spelling of it.
const kongVersionVar = "version"

// terminationSignals is every signal Run treats as "shut down gracefully" —
// passed to signal.NotifyContext so a Bubbletea TUI's cleanup (restoring the
// terminal from raw/alt-screen mode) still runs when the process receives
// one, rather than dying immediately and leaving the terminal in a broken
// state. os.Interrupt covers Ctrl-C; syscall.SIGTERM covers `kill`/process
// managers/CI job cancellation.
//
//nolint:gochecknoglobals // read-only signal list
var terminationSignals = []os.Signal{
	os.Interrupt,
	syscall.SIGTERM,
}

// Run parses args and executes jj-trim, returning the process exit code.
func Run(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var cli CLI

	exitEarly := false

	parser, err := kong.New(
		&cli,
		kong.Name("jj-trim"),
		kong.Description(
			"Clean up merged bookmarks and abandoned anonymous commits in a jj repository.",
		),
		kong.Vars{kongVersionVar: version},
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { exitEarly = true }),
	)
	if err != nil {
		fprintln(stderr, err)

		return exitUsage
	}

	// kongplete.Complete is a no-op unless the shell is actually requesting
	// completions (COMP_LINE set in the environment) — safe to call
	// unconditionally before every real Parse. It shares kong.Exit's
	// exitEarly hook above, same as --version/--help/--man/
	// install-completions.
	kongplete.Complete(parser)

	kongCtx, err := parser.Parse(args)

	if exitEarly {
		return exitOK
	}

	if err != nil {
		fprintln(stderr, err)

		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals...)
	defer stop()

	// Restore default SIGINT disposition as soon as the first one lands,
	// so a second Ctrl-C during a blocked stdin read still kills the
	// process rather than hanging forever with SIGINT intercepted.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := run(ctx, cli, kongCtx.Command(), stdin, stdout); err != nil {
		fprintln(stderr, "error:", err)

		return exitRuntime
	}

	return exitOK
}

func run(ctx context.Context, cli CLI, command string, stdin io.Reader, stdout io.Writer) error {
	runner := jj.ExecRunner{Repository: cli.Repository}

	if cli.Fetch {
		if err := jj.GitFetch(ctx, runner); err != nil {
			return fmt.Errorf("jj git fetch: %w", err)
		}
	}

	if command == cmdTui {
		return runTui(ctx, runner, stdin, stdout)
	}

	group, action, ok := strings.Cut(command, " ")
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownCommand, command)
	}

	switch group {
	case cmdGroupBookmarks:
		return runBookmarks(ctx, runner, cli.Bookmarks, action, stdin, stdout)
	case cmdGroupCommits:
		return runCommits(ctx, runner, cli.Commits, action, stdin, stdout)
	default:
		return fmt.Errorf("%w: %q", errUnknownCommand, command)
	}
}

// runBookmarks previews bookmarks merged into trunk (certain) plus
// probably-merged/stale bookmarks (heuristic). action is "preview" (the
// default subcommand), "apply" (deletes the certain bucket), or "review"
// (interactive walk over all three buckets).
func runBookmarks(
	ctx context.Context,
	r jj.Runner,
	cmd BookmarksCmd,
	action string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	trunk := cmd.Trunk
	if trunk == "" {
		trunk = defaultTrunkRevset
	}

	staleAfter := defaultStaleAfter
	if cmd.StaleAfter != nil {
		staleAfter = *cmd.StaleAfter
	}

	var merged, probablyMerged, stale []classify.Candidate

	err := spin.Run(os.Stderr, "Classifying bookmarks…", func() error {
		var err error

		merged, err = mergedBookmarks(ctx, r, trunk, cmd.Protected)
		if err != nil {
			return err
		}

		probablyMerged, stale, err = heuristicBookmarks(ctx, r, trunk, cmd.Protected, staleAfter)

		return err
	})
	if err != nil {
		return fmt.Errorf("classifying bookmarks: %w", err)
	}

	legend := bookmarkLegend(merged, classify.ReasonMerged)
	legend = append(legend, bookmarkLegend(probablyMerged, classify.ReasonProbablyMerged)...)
	legend = append(legend, bookmarkLegend(stale, classify.ReasonStale)...)

	revset := bookmarksPreviewRevset(trunk, probablyMerged, stale)
	if err := preview.Print(ctx, r, stdout, revset, legend, cmd.Explain); err != nil {
		return fmt.Errorf("printing preview: %w", err)
	}

	names := bookmarkNames(merged)

	switch action {
	case cmdLeafApply:
		return applyMerged(ctx, r, stdout, names)
	case cmdLeafReview:
		return reviewBookmarks(ctx, r, stdin, stdout, merged, probablyMerged, stale, trunk)
	case cmdLeafPreview:
		fallthrough
	default:
		summarizeBookmarks(stdout, names, len(probablyMerged)+len(stale))

		return nil
	}
}

// reviewBookmarks launches `bookmarks review`: delete is the Action
// (ref-only, matching --apply's certain-bucket scope), abandon is the
// CascadeAction (delete + abandon the private commit chain — see
// bookmarkItems/classify.PrivateChainRevset).
func reviewBookmarks(
	ctx context.Context,
	r jj.Runner,
	stdin io.Reader,
	stdout io.Writer,
	merged, probablyMerged, stale []classify.Candidate,
	trunk string,
) error {
	items := bookmarkReviewItems(merged, probablyMerged, stale, trunk)
	reviewAction := review.Action{
		Verb: verbDelete, Past: pastDeleted, Apply: jj.BookmarkDelete,
		CascadeAction: &review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
	}

	return reviewCandidates(ctx, r, stdin, stdout, items, reviewAction, newBookmarkContext(trunk))
}

// bookmarkReviewItems builds the review.Item list for `bookmarks review`:
// the certain merged bucket plus the heuristic probably-merged/stale
// buckets, all starting unmarked — review always requires an explicit
// per-item choice (apply remains the bulk path for the certain bucket).
func bookmarkReviewItems(
	merged, probablyMerged, stale []classify.Candidate,
	trunk string,
) []review.Item {
	items := make([]review.Item, 0, len(merged)+len(probablyMerged)+len(stale))
	items = append(items, bookmarkItems(merged, classify.ReasonMerged, trunk)...)
	items = append(items, bookmarkItems(probablyMerged, classify.ReasonProbablyMerged, trunk)...)
	items = append(items, bookmarkItems(stale, classify.ReasonStale, trunk)...)

	return items
}

func bookmarkItems(
	candidates []classify.Candidate,
	reason classify.Reason,
	trunk string,
) []review.Item {
	items := make([]review.Item, 0, len(candidates))

	for _, c := range candidates {
		keep := classify.KeepRevset(trunk, bookmarksExceptSelf(c))
		items = append(items, review.Item{
			IDs:        c.LocalBookmarks,
			CascadeIDs: []string{classify.PrivateChainRevset(c.ChangeID, keep)},
			Candidate:  c,
			Legend:     bookmarkLegend([]classify.Candidate{c}, reason)[0],
		})
	}

	return items
}

// bookmarksExceptSelf returns bookmarks() with c's own local bookmark
// name(s) excluded, for use as the "keep" set's bookmark term when
// computing a bookmark's cascade-abandon private chain — without this
// exclusion, a bookmark trivially self-protects its own chain (its own
// commit is always an ancestor of itself) and cascade always degrades to
// a no-op, not just for the merged bucket.
func bookmarksExceptSelf(c classify.Candidate) string {
	var b strings.Builder

	b.WriteString("bookmarks()")

	for _, name := range c.LocalBookmarks {
		fmt.Fprintf(&b, " ~ bookmarks(exact:%q)", name)
	}

	return b.String()
}

// newBookmarkContext builds the review TUI's detail-pane fetcher for
// `bookmarks`: the tip commit's own `jj show`, plus a preview of what
// cascade would additionally abandon (empty for the merged bucket, whose
// commit is already part of trunk's own ancestry — see
// classify.PrivateChainRevset's doc comment).
func newBookmarkContext(trunk string) review.ContextFetcher {
	return func(ctx context.Context, r jj.Runner, c classify.Candidate) (string, error) {
		show, err := jj.Show(ctx, r, c.ChangeID)
		if err != nil {
			return "", fmt.Errorf("showing candidate %s: %w", c.ChangeID, err)
		}

		keep := classify.KeepRevset(trunk, bookmarksExceptSelf(c))

		chain, err := jj.Log(ctx, r, classify.PrivateChainRevset(c.ChangeID, keep),
			`self.change_id().shortest() ++ "\n"`)
		if err != nil {
			return "", fmt.Errorf("checking private chain of %s: %w", c.ChangeID, err)
		}

		if strings.TrimSpace(chain) == "" {
			show += "\nCascade: sits on trunk's own history — no private commits to abandon.\n"
		} else {
			show += "\nCascade would additionally abandon:\n" + chain
		}

		return show, nil
	}
}

// heuristicBookmarks classifies bookmarks that aren't literal ancestors of
// trunk (so MergedBookmarks/apply can't catch them) but are very likely
// safe to clean up anyway: ProbablyMerged (message found in trunk history —
// the signature of a squash-merged PR) takes priority over Stale (old, no
// tracked remote). Never auto-deleted by apply — see DESIGN.md.
func heuristicBookmarks(
	ctx context.Context,
	r jj.Runner,
	trunk string,
	protectedGlobs []string,
	staleAfter time.Duration,
) ([]classify.Candidate, []classify.Candidate, error) {
	out, err := jj.Log(ctx, r, classify.UnmergedBookmarks(trunk), classify.Template)
	if err != nil {
		return nil, nil, fmt.Errorf("querying unmerged bookmarks: %w", err)
	}

	candidates, err := classify.ParseCandidates(out)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing unmerged bookmarks: %w", err)
	}

	candidates = filterProtected(candidates, protectedGlobs)

	trunkHistory, err := jj.TrunkHistory(ctx, r, trunk)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching trunk history: %w", err)
	}

	now := time.Now()

	var probablyMerged, stale []classify.Candidate

	for _, c := range candidates {
		switch {
		case c.ProbablyMerged(trunkHistory):
			probablyMerged = append(probablyMerged, c)
		case c.Stale(staleAfter, now):
			stale = append(stale, c)
		}
	}

	return probablyMerged, stale, nil
}

// bookmarksPreviewRevset widens MergedBookmarks(trunk) to also include the
// heuristically classified bookmarks, so the graph shows every commit the
// legend has a line for.
func bookmarksPreviewRevset(trunk string, heuristicSets ...[]classify.Candidate) string {
	var b strings.Builder

	b.WriteString(classify.MergedBookmarks(trunk))

	for _, set := range heuristicSets {
		for _, c := range set {
			for _, name := range c.LocalBookmarks {
				fmt.Fprintf(&b, " | bookmarks(exact:%q)", name)
			}
		}
	}

	return b.String()
}

// bookmarkNames flattens the local bookmark names off a candidate list.
func bookmarkNames(candidates []classify.Candidate) []string {
	names := make([]string, 0, len(candidates))

	for _, c := range candidates {
		names = append(names, c.LocalBookmarks...)
	}

	return names
}

// summarizeBookmarks tells the user what `bookmarks apply` would do, since
// the preview graph + legend above don't spell out the pending action
// itself. heuristicCount is reported separately since apply never touches
// those.
func summarizeBookmarks(out io.Writer, names []string, heuristicCount int) {
	if len(names) == 0 {
		fprintln(out, "No bookmarks to delete.")
	} else {
		fprintf(
			out,
			"Would delete %d bookmark(s): %v (run `bookmarks apply` to delete)\n",
			len(names),
			names,
		)
	}

	if heuristicCount > 0 {
		fprintf(out,
			"%d more bookmark(s) look probably-merged/stale — run `bookmarks review` to decide.\n",
			heuristicCount)
	}
}

// runCommits previews anonymous commit forks, walking them interactively
// if action is "review".
func runCommits(
	ctx context.Context,
	r jj.Runner,
	cmd CommitsCmd,
	action string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	var buckets []forkBucket

	err := spin.Run(os.Stderr, "Classifying commits…", func() error {
		var err error

		buckets, err = classifyForks(ctx, r, cmd.NoDescriptionOnly)

		return err
	})
	if err != nil {
		return fmt.Errorf("classifying commits: %w", err)
	}

	revset := forksPreviewRevset(buckets, cmd.NoDescriptionOnly)
	legend := buildForksLegend(buckets)

	if err := preview.Print(ctx, r, stdout, revset, legend, cmd.Explain); err != nil {
		return fmt.Errorf("printing preview: %w", err)
	}

	all := combineForks(buckets)

	if action == cmdLeafReview {
		items := forkReviewItems(buckets, all, commitsTrunk)
		reviewAction := review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon}

		return reviewCandidates(
			ctx,
			r,
			stdin,
			stdout,
			items,
			reviewAction,
			newForkContext(commitsTrunk, all),
		)
	}

	summarizeForks(stdout, len(all))

	return nil
}

// commitsTrunk is used internally by `commits review`'s cascade-abandon to
// know where a fork's private chain ends — not exposed as a --trunk flag on
// `commits` (its candidate revset itself never references trunk(), per
// DESIGN.md), so the default is always used.
const commitsTrunk = defaultTrunkRevset

// forkBucket pairs one commits classification bucket's candidates with the
// fixed Reason its items/legend get tagged with — duplicates
// (classify.ReasonGitCommitDuplicate), no-description, has-description, in
// that highest-confidence-first order. A single ordered []forkBucket (see
// classifyForks) is threaded through forkReviewItems/buildForksLegend/
// combineForks/forksPreviewRevset instead of three separate parameters, so
// adding, removing, or reordering a bucket only touches classifyForks.
type forkBucket struct {
	reason     classify.Reason
	candidates []classify.Candidate
}

// bucketByReason returns the candidates of the bucket tagged reason, or
// nil if there isn't one — used where only one specific bucket is needed
// (e.g. forksPreviewRevset's duplicates-only union) without depending on
// buckets' positional order.
func bucketByReason(buckets []forkBucket, reason classify.Reason) []classify.Candidate {
	for _, b := range buckets {
		if b.reason == reason {
			return b.candidates
		}
	}

	return nil
}

// forkReviewItems builds the review.Item list for `commits review`, one
// bucket at a time in the order classifyForks produced them (highest
// confidence first — see classify.GitCommitDuplicates), each bucket
// oldest-first within itself (per DESIGN.md: highest-confidence stale
// forks come first). None are pre-marked — an anonymous fork is never
// certain to be safe. Marking a candidate abandons its full private chain
// back to trunk, not just the head — see forkKeepRevset. all is the full
// combined candidate set (see combineForks) — passed in rather than
// recomputed here, since every call site already has it for other reasons
// (the detail-pane context fetcher, the summary count).
func forkReviewItems(buckets []forkBucket, all []classify.Candidate, trunk string) []review.Item {
	items := make([]review.Item, 0, len(all))

	for _, b := range buckets {
		items = append(items, forkItemsForReason(b.candidates, b.reason, all, trunk)...)
	}

	return items
}

// forkItemsForReason builds review.Items for one bucket, oldest-first,
// tagged with the bucket's fixed reason rather than re-deriving it from
// HasDescription() (needed so the duplicate bucket's items get
// ReasonGitCommitDuplicate even though its candidates are almost always
// also description-less). allForks is the full combined set, needed by
// forkKeepRevset to protect sibling ancestries regardless of which bucket
// they're in.
func forkItemsForReason(
	candidates []classify.Candidate,
	reason classify.Reason,
	allForks []classify.Candidate,
	trunk string,
) []review.Item {
	sorted := make([]classify.Candidate, len(candidates))
	copy(sorted, candidates)
	classify.SortOldestFirst(sorted)

	items := make([]review.Item, 0, len(sorted))

	for _, c := range sorted {
		entry := classify.BuildLegend([]classify.Candidate{c}, reason)[0]
		entry.DiffStat = ageAndDiffStat(c)

		keep := forkKeepRevset(trunk, allForks, c.ChangeID)
		items = append(items, review.Item{
			IDs:       []string{classify.PrivateChainRevset(c.ChangeID, keep)},
			Candidate: c,
			Legend:    entry,
		})
	}

	return items
}

// combineForks flattens buckets back into one slice, in bucket order
// (duplicates first) — used wherever the full candidate set is needed
// (forkKeepRevset's sibling protection, the detail-pane context fetcher,
// the summary count).
func combineForks(buckets []forkBucket) []classify.Candidate {
	total := 0
	for _, b := range buckets {
		total += len(b.candidates)
	}

	all := make([]classify.Candidate, 0, total)
	for _, b := range buckets {
		all = append(all, b.candidates...)
	}

	return all
}

// forkKeepRevset returns what a fork candidate's private-chain abandon must
// never touch: trunk, every bookmark, @, and every OTHER fork candidate's
// ancestry — the last part matters because fork heads are never bookmarked,
// so two stacks sharing a private base commit would otherwise both think
// they own it; protecting sibling candidates' ancestry means the shared
// base survives unless every fork built on it is marked in the same
// session, rather than being abandoned out from under whichever one wasn't
// marked.
func forkKeepRevset(trunk string, forks []classify.Candidate, selfID string) string {
	others := make([]string, 0, len(forks))

	for _, c := range forks {
		if c.ChangeID != selfID {
			others = append(others, c.ChangeID)
		}
	}

	return classify.KeepRevset(trunk, "bookmarks()", others...)
}

// newForkContext builds the review TUI's detail-pane fetcher for `commits`:
// the candidate's own `jj show`, a warning if abandoning it would rebase
// descendants (jj does this automatically, but the user should see it's
// about to happen), and the private chain that marking it would abandon
// alongside the head itself.
func newForkContext(trunk string, forks []classify.Candidate) review.ContextFetcher {
	return func(ctx context.Context, r jj.Runner, c classify.Candidate) (string, error) {
		show, err := jj.Show(ctx, r, c.ChangeID)
		if err != nil {
			return "", fmt.Errorf("showing candidate %s: %w", c.ChangeID, err)
		}

		descendants, err := jj.Log(ctx, r, classify.DescendantsRevset(c.ChangeID),
			`self.change_id().shortest() ++ "\n"`)
		if err != nil {
			return "", fmt.Errorf("checking descendants of %s: %w", c.ChangeID, err)
		}

		if strings.TrimSpace(descendants) != "" {
			show += "\nDescendants that will be rebased onto the parent:\n" + descendants
		}

		keep := forkKeepRevset(trunk, forks, c.ChangeID)

		chain, err := jj.Log(ctx, r, classify.PrivateChainRevset(c.ChangeID, keep),
			`self.change_id().shortest() ++ "\n"`)
		if err != nil {
			return "", fmt.Errorf("checking private chain of %s: %w", c.ChangeID, err)
		}

		show += "\nWill abandon:\n" + chain

		return show, nil
	}
}

// summarizeForks tells the user what `commits review` would let them do, since the
// preview graph + legend above don't spell out the pending action itself.
func summarizeForks(out io.Writer, count int) {
	if count == 0 {
		fprintln(out, "No anonymous commits found.")

		return
	}

	fprintf(
		out,
		"%d anonymous commit(s) found. Run `commits review` to decide what to do with them.\n",
		count,
	)
}

// buildForksLegend labels each fork by its bucket (duplicate / no
// description / has description, in that highest-confidence-first order,
// per classifyForks) and attaches an age+diffstat summary — the main
// signal for deciding whether an abandoned fork is safe to abandon.
func buildForksLegend(buckets []forkBucket) []classify.LegendEntry {
	total := 0
	for _, b := range buckets {
		total += len(b.candidates)
	}

	legend := make([]classify.LegendEntry, 0, total)

	for _, b := range buckets {
		legend = append(legend, forkLegendForReason(b.candidates, b.reason)...)
	}

	return legend
}

func forkLegendForReason(
	candidates []classify.Candidate,
	reason classify.Reason,
) []classify.LegendEntry {
	entries := classify.BuildLegend(candidates, reason)
	for i, c := range candidates {
		entries[i].DiffStat = ageAndDiffStat(c)
	}

	return entries
}

// forksPreviewRevset previews everything AnonymousForks()/
// AnonymousForksNoDescription() would (matching --no-description-only's
// existing scope for those two buckets), plus every duplicate explicitly
// unioned in by change id — duplicates are shown regardless of
// --no-description-only, since a duplicate is safe independent of whether
// it happens to have a description.
func forksPreviewRevset(buckets []forkBucket, noDescriptionOnly bool) string {
	base := classify.AnonymousForks()
	if noDescriptionOnly {
		base = classify.AnonymousForksNoDescription()
	}

	var b strings.Builder

	b.WriteString(base)

	for _, c := range bucketByReason(buckets, classify.ReasonGitCommitDuplicate) {
		fmt.Fprintf(&b, " | %s", classify.ChangeIDRevset(c.ChangeID))
	}

	return b.String()
}

// ageAndDiffStat renders a legend entry's DiffStat field for `commits`:
// how long ago the fork was committed, alongside its diffstat — age is
// the main "does anyone still care about this" signal, so it leads.
func ageAndDiffStat(c classify.Candidate) string {
	return c.Age(time.Now()) + ", " + c.DiffStatSummary()
}

// bookmarkLegend builds legend entries for candidates exactly like
// classify.BuildLegend, additionally stamping each one's age into the
// LegendEntry.DiffStat field — `bookmarks` doesn't otherwise set DiffStat
// (a merged bookmark's diff isn't itself actionable, per LegendEntry's own
// doc comment), so it carries only age here, unlike ageAndDiffStat's
// age-plus-diffstat for `commits`.
func bookmarkLegend(
	candidates []classify.Candidate,
	reason classify.Reason,
) []classify.LegendEntry {
	entries := classify.BuildLegend(candidates, reason)
	now := time.Now()

	for i, c := range candidates {
		entries[i].DiffStat = c.Age(now)
	}

	return entries
}

func mergedBookmarks(
	ctx context.Context, r jj.Runner, trunk string, protectedGlobs []string,
) ([]classify.Candidate, error) {
	out, err := jj.Log(ctx, r, classify.MergedBookmarks(trunk), classify.Template)
	if err != nil {
		return nil, fmt.Errorf("querying merged bookmarks: %w", err)
	}

	candidates, err := classify.ParseCandidates(out)
	if err != nil {
		return nil, fmt.Errorf("parsing merged bookmarks: %w", err)
	}

	return filterProtected(candidates, protectedGlobs), nil
}

// classifyForks queries the commits candidate set and partitions it into
// forkBuckets, highest confidence first: duplicates (exact content match
// against a kept sibling — see classify.GitCommitDuplicates), no
// description, has description. The duplicate check always runs over the
// *full* candidate set (classify.AnonymousForks()) regardless of
// noDescriptionOnly — a duplicate is safe independent of whether it
// happens to have a description — and noDescriptionOnly only narrows the
// remaining (non-duplicate) candidates, matching CommitsCmd.
// NoDescriptionOnly's existing scope for those two buckets. The candidate
// and kept-history queries are independent of each other, so they run
// concurrently rather than back-to-back.
func classifyForks(
	ctx context.Context, r jj.Runner, noDescriptionOnly bool,
) ([]forkBucket, error) {
	var candidates, kept []classify.Candidate

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		out, err := jj.Log(gctx, r, classify.AnonymousForks(), classify.TemplateWithDuplicateKey)
		if err != nil {
			return fmt.Errorf("querying anonymous forks: %w", err)
		}

		candidates, err = classify.ParseCandidates(out)
		if err != nil {
			return fmt.Errorf("parsing anonymous forks: %w", err)
		}

		return nil
	})

	g.Go(func() error {
		out, err := jj.Log(gctx, r, classify.KeptHistory(), classify.TemplateWithDuplicateKey)
		if err != nil {
			return fmt.Errorf("querying kept history: %w", err)
		}

		kept, err = classify.ParseCandidates(out)
		if err != nil {
			return fmt.Errorf("parsing kept history: %w", err)
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("classifying anonymous forks: %w", err)
	}

	duplicates, rest := classify.GitCommitDuplicates(candidates, kept)

	var noDescription, hasDescription []classify.Candidate

	for _, c := range rest {
		if noDescriptionOnly && c.HasDescription() {
			continue
		}

		if c.HasDescription() {
			hasDescription = append(hasDescription, c)
		} else {
			noDescription = append(noDescription, c)
		}
	}

	return []forkBucket{
		{reason: classify.ReasonGitCommitDuplicate, candidates: duplicates},
		{reason: classify.ReasonNoDescription, candidates: noDescription},
		{reason: classify.ReasonHasDescription, candidates: hasDescription},
	}, nil
}

// filterProtected drops any candidate with at least one local bookmark
// name matching a --protected glob.
func filterProtected(candidates []classify.Candidate, globs []string) []classify.Candidate {
	if len(globs) == 0 {
		return candidates
	}

	patterns := compileProtectedGlobs(globs)

	kept := make([]classify.Candidate, 0, len(candidates))

	for _, c := range candidates {
		if !anyBookmarkProtected(c.LocalBookmarks, patterns) {
			kept = append(kept, c)
		}
	}

	return kept
}

// compileProtectedGlobs compiles each --protected pattern once, so
// filterProtected doesn't recompile the same patterns for every candidate.
func compileProtectedGlobs(globs []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(globs))

	for _, glob := range globs {
		patterns = append(patterns, globToRegexp(glob))
	}

	return patterns
}

// globToRegexp translates a glob pattern (only `*`, matching any run of
// characters, is a wildcard) into an anchored regular expression matching
// the whole string. This intentionally isn't path.Match/filepath.Match:
// both of those (a) treat `/` as a path separator that `*` never crosses,
// which is wrong here — bookmark names routinely contain `/` (e.g.
// "alice/feature-x") with no path-segment semantics intended, so
// "release/*" must protect "release/v2/hotfix" too — and (b) can fail on
// a malformed bracket expression, an error path.Match's caller used to
// silently discard, treating a typo'd --protected pattern as "matches
// nothing" instead of failing loudly. Only recognizing `*` as special
// means every other character (including "[") is quoted literally via
// regexp.QuoteMeta and always produces a valid, always-compilable regex —
// eliminating that failure mode rather than handling it.
func globToRegexp(glob string) *regexp.Regexp {
	parts := strings.Split(glob, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}

	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

func anyBookmarkProtected(names []string, patterns []*regexp.Regexp) bool {
	for _, name := range names {
		for _, re := range patterns {
			if re.MatchString(name) {
				return true
			}
		}
	}

	return false
}

func applyMerged(ctx context.Context, r jj.Runner, out io.Writer, names []string) error {
	if len(names) == 0 {
		fprintln(out, "No bookmarks to delete.")

		return nil
	}

	opID, err := deleteBookmarkBatch(ctx, r, names)
	if err != nil {
		return err
	}

	fprintf(
		out,
		"Deleted %d bookmark(s): %v\nUndo with: jj op revert %s\n",
		len(names),
		names,
		opID,
	)

	return nil
}

// deleteBookmarkBatch is applyMerged's non-interactive bulk-delete core,
// factored out so runTui's browse.Options.Apply (the TUI-reached apply
// shortcut) shares it rather than reimplementing it — the only difference
// between the two callers is what happens to the result (applyMerged prints
// it immediately; the browse shortcut hands the op id back to the browse
// model to report after the TUI exits).
func deleteBookmarkBatch(ctx context.Context, r jj.Runner, names []string) (string, error) {
	if err := jj.BookmarkDelete(ctx, r, names); err != nil {
		return "", fmt.Errorf("deleting merged bookmarks: %w", err)
	}

	opID, err := jj.LastOpID(ctx, r)
	if err != nil {
		return "", fmt.Errorf("looking up resulting op id: %w", err)
	}

	return opID, nil
}

// runTui launches jj-trim's TUI-first front end (internal/browse) for bare
// `jj-trim`: a live candidate view (commits mode by default) with a mode
// toggle and an in-flow filters overlay. It builds its item sets via the
// *same* functions the CLI dispatch path above already calls
// (mergedBookmarks, heuristicBookmarks, bookmarkReviewItems,
// anonymousForks, forkReviewItems, ...) — nothing here reimplements
// classification, querying, or applying, only how the inputs are gathered
// differs from the Kong-flag-driven path.
func requireTerminal(stdin io.Reader, stdout io.Writer) error {
	stdinFile, ok := stdin.(*os.File)
	if !ok {
		return tty.ErrNotInteractive
	}

	stdoutFile, ok := stdout.(*os.File)
	if !ok {
		return tty.ErrNotInteractive
	}

	if err := tty.Require(stdinFile, stdoutFile); err != nil {
		return fmt.Errorf("checking tty: %w", err)
	}

	return nil
}

func runTui(ctx context.Context, r jj.Runner, stdin io.Reader, stdout io.Writer) error {
	if err := requireTerminal(stdin, stdout); err != nil {
		return fmt.Errorf("checking tui prerequisites: %w", err)
	}

	cfg := trimconfig.Config{Trunk: defaultTrunkRevset}

	opts := browse.Options{
		Bookmarks: bookmarksBrowseSession,
		Commits:   commitsBrowseSession,
	}

	result, err := browse.Run(ctx, r, cfg, opts, stdin, stdout)
	if err != nil {
		return fmt.Errorf("tui session: %w", err)
	}

	printReviewResult(stdout, result)

	return nil
}

// bookmarksBrowseSession builds internal/browse's bookmarks-mode Session
// from cfg — the classification half of runBookmarks, without the
// preview-print/action dispatch: the browse screen's live list already is
// the preview, so there's no separate preview step to run here.
func bookmarksBrowseSession(
	ctx context.Context, r jj.Runner, cfg trimconfig.Config,
) (browse.Session, error) {
	trunk := cfg.Trunk
	if trunk == "" {
		trunk = defaultTrunkRevset
	}

	staleAfter := defaultStaleAfter
	if cfg.StaleAfter != nil {
		staleAfter = *cfg.StaleAfter
	}

	merged, err := mergedBookmarks(ctx, r, trunk, cfg.Protected)
	if err != nil {
		return browse.Session{}, err
	}

	probablyMerged, stale, err := heuristicBookmarks(ctx, r, trunk, cfg.Protected, staleAfter)
	if err != nil {
		return browse.Session{}, err
	}

	return browse.Session{
		Action: review.Action{
			Verb:  verbDelete,
			Past:  pastDeleted,
			Apply: jj.BookmarkDelete,
			CascadeAction: &review.Action{
				Verb:  verbAbandon,
				Past:  pastAbandoned,
				Apply: jj.Abandon,
			},
		},
		Items: bookmarkReviewItems(merged, probablyMerged, stale, trunk),
		Fetch: newBookmarkContext(trunk),
	}, nil
}

// commitsBrowseSession builds internal/browse's commits-mode Session from
// cfg — the classification half of runCommits, same caveat as
// bookmarksBrowseSession re: no separate preview step.
func commitsBrowseSession(
	ctx context.Context, r jj.Runner, cfg trimconfig.Config,
) (browse.Session, error) {
	buckets, err := classifyForks(ctx, r, cfg.NoDescriptionOnly)
	if err != nil {
		return browse.Session{}, err
	}

	all := combineForks(buckets)

	return browse.Session{
		Action: review.Action{Verb: verbAbandon, Past: pastAbandoned, Apply: jj.Abandon},
		Items:  forkReviewItems(buckets, all, commitsTrunk),
		Fetch:  newForkContext(commitsTrunk, all),
	}, nil
}

// reviewCandidates launches the shared review TUI (see internal/review)
// for items, applying action.Apply to whatever ends up marked. Used by
// both `bookmarks review` and `commits review`.
func reviewCandidates(
	ctx context.Context,
	r jj.Runner,
	stdin io.Reader,
	stdout io.Writer,
	items []review.Item,
	action review.Action,
	fetch review.ContextFetcher,
) error {
	if err := requireTerminal(stdin, stdout); err != nil {
		return fmt.Errorf("checking review prerequisites: %w", err)
	}

	result, err := review.Run(ctx, r, items, action, fetch, stdin, stdout)
	if err != nil {
		return fmt.Errorf("review session: %w", err)
	}

	printReviewResult(stdout, result)

	return nil
}

// printReviewResult reports what a review session did. A bookmarks-review
// session with both Action- and CascadeAction-marked items runs two
// independent batches (ref delete, then abandon), so result.OpIDs may have
// up to two entries — one "Undo with" line per op that actually ran. jj op
// revert (not jj op undo, which doesn't exist as a jj subcommand — only
// the top-level `jj undo`, which takes no operation id and always targets
// the single most recent operation) reverts specifically that operation
// regardless of what else has happened since.
func printReviewResult(out io.Writer, result review.Result) {
	if len(result.OpIDs) == 0 {
		fprintf(out, "Nothing done.\n")

		return
	}

	fprintf(out, "%d item(s) processed.\n", len(result.Applied))

	for _, opID := range result.OpIDs {
		fprintf(out, "Undo with: jj op revert %s\n", opID)
	}
}

// fprintln/fprintf write CLI-facing status text. Write failures here (a
// broken pipe on stdout/stderr) aren't actionable — there's nothing useful
// left to do with the error — so they're deliberately discarded rather
// than propagated.
// Write failures here aren't actionable (broken pipe/closed fd) — discarded intentionally.
func fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

// Write failures here aren't actionable (broken pipe/closed fd) — discarded intentionally.
func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
