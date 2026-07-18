package jj_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/require"
)

// mergedBookmarkName is the bookmark name shared by every real-repo fixture
// in this file that needs a bookmark already merged into (an ancestor of)
// trunk.
const mergedBookmarkName = "merged"

// requireJJ skips the test if the jj binary isn't on PATH. Runtime-skipped,
// not build-tag-gated: CI always has jj via mise, so the skip never
// triggers there.
func requireJJ(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not found on PATH")
	}
}

// runIn runs jj with args inside dir and requires it to succeed.
func runIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	//nolint:gosec // test helper, args are test-controlled fixtures
	cmd := exec.CommandContext(context.Background(), "jj", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "jj %v: %s", args, out)

	return string(out)
}

// initRepo creates a temp repo with a git repo initialized and a "main"
// bookmark pointing at root.
func initRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	runIn(t, dir, "git", "init")
	runIn(t, dir, "describe", "-m", "root change")
	runIn(t, dir, "bookmark", "create", "main", "-r", "@")

	return dir
}

// repoWithMergedBookmark creates a temp repo where bookmark "merged"
// points at a commit that's an ancestor of trunk (main).
func repoWithMergedBookmark(t *testing.T) string {
	t.Helper()

	dir := initRepo(t)
	runIn(t, dir, "new", "-m", "feature work")
	runIn(t, dir, "bookmark", "create", mergedBookmarkName, "-r", "@")
	runIn(t, dir, "bookmark", "set", "main", "-r", "@")

	return dir
}

// repoWithAnonymousFork creates a temp repo with an anonymous mutable head
// (no bookmark) off of trunk, optionally with a description.
func repoWithAnonymousFork(t *testing.T, withDescription bool) string {
	t.Helper()

	dir := initRepo(t)
	runIn(t, dir, "new", "main")

	// A non-empty fork: jj auto-abandons empty, undescribed working-copy
	// commits when the working copy moves away, which would silently
	// erase the very fixture this test needs to exist.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fork.txt"), []byte("wip\n"), 0o600))

	if withDescription {
		runIn(t, dir, "describe", "-m", "half-finished idea")
	}

	// Move the working copy away from the fork, so the fork is a mutable
	// head distinct from @ (heads(mutable()) ~ bookmarks() ~ @ excludes @
	// itself).
	runIn(t, dir, "new", "main")

	return dir
}

// repoWithProtectedBookmark creates a temp repo with a bookmark that
// should never be classified merged: it sits exactly on trunk itself.
func repoWithProtectedBookmark(t *testing.T) string {
	t.Helper()

	return initRepo(t)
}

// dirRunner is a Runner backed by the real jj binary, scoped to a fixed
// working directory, for integration tests against a temp repo.
type dirRunner struct {
	dir string
}

var _ jj.Runner = dirRunner{}

func (d dirRunner) Run(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer

	//nolint:gosec // integration test helper, args are test-controlled
	cmd := exec.CommandContext(ctx, "jj", args...)
	cmd.Dir = d.dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("jj %v: %w: %s", args, err, stderr.String())
	}

	return stdout.String(), nil
}

func (d dirRunner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	var stderr bytes.Buffer

	//nolint:gosec // integration test helper, args are test-controlled
	cmd := exec.CommandContext(ctx, "jj", args...)
	cmd.Dir = d.dir
	cmd.Stdout = w
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("jj %v: %w: %s", args, err, stderr.String())
	}

	return nil
}

// bookmarkDeleteSetup creates a repo with a merged bookmark, then deletes
// it. Both the repo dir and the runner are returned so callers can assert
// the bookmark is gone and optionally op-revert the deletion.
func bookmarkDeleteSetup(t *testing.T) (string, jj.Runner) {
	t.Helper()

	dir := repoWithMergedBookmark(t)
	r := dirRunner{dir: dir}
	require.NoError(t, jj.BookmarkDelete(context.Background(), r, []string{mergedBookmarkName}))

	return dir, r
}

func TestLog_RealRepo_MergedBookmark(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := repoWithMergedBookmark(t)
	r := dirRunner{dir: dir}

	out, err := jj.Log(context.Background(), r, "bookmarks(exact:\"merged\")",
		`self.change_id().shortest()`)
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(out))
}

func TestLog_RealRepo_AnonymousFork(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := repoWithAnonymousFork(t, false)
	r := dirRunner{dir: dir}

	out, err := jj.Log(context.Background(), r, "heads(mutable()) ~ bookmarks() ~ @",
		`self.change_id().shortest()`)
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(out))
}

func TestLog_RealRepo_ProtectedTrunkBookmark(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := repoWithProtectedBookmark(t)
	r := dirRunner{dir: dir}

	// main sits exactly on trunk; the ~ trunk() exclusion must keep it out
	// of the merged set (see DESIGN.md's correctness note).
	out, err := jj.Log(context.Background(), r, "bookmarks() & ::trunk() ~ trunk()",
		`self.change_id().shortest()`)
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(out))
}

func TestBookmarkDelete_RealRepo(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, _ := bookmarkDeleteSetup(t)

	out := runIn(t, dir, "bookmark", "list")
	require.NotContains(t, out, mergedBookmarkName)
}

// TestBookmarkDelete_RealRepo_OpRevert_RestoresBookmark guards against a
// real incident this session: a `bookmarks apply` smoke test against a
// real repo actually deleted a bookmark, and the fix relied on the exact
// op id `applyMerged`/`printReviewResult` print in "Undo with: jj op
// revert <id>" being one `jj op revert` can actually restore from — a
// claim only ever checked by hand until now.
func TestBookmarkDelete_RealRepo_OpRevert_RestoresBookmark(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, r := bookmarkDeleteSetup(t)
	require.NotContains(t, runIn(t, dir, "bookmark", "list"), mergedBookmarkName)

	opID, err := jj.LastOpID(context.Background(), r)
	require.NoError(t, err)
	require.NotEmpty(t, opID)

	runIn(t, dir, "op", "revert", opID)

	require.Contains(t, runIn(t, dir, "bookmark", "list"), mergedBookmarkName,
		"jj op revert on the id jj-trim prints must actually restore the deleted bookmark")
}

func TestAbandon_RealRepo(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := repoWithAnonymousFork(t, true)
	r := dirRunner{dir: dir}

	out, err := jj.Log(context.Background(), r, "heads(mutable()) ~ bookmarks() ~ @",
		`self.change_id()`)
	require.NoError(t, err)

	changeID := strings.TrimSpace(strings.Split(out, "\n")[0])
	require.NotEmpty(t, changeID)

	require.NoError(t, jj.Abandon(context.Background(), r, []string{changeID}))

	out = runIn(t, dir, "log", "-r", "heads(mutable()) ~ bookmarks() ~ @", "--no-graph",
		"-T", `self.change_id()`)
	require.NotContains(t, out, changeID)
}

func TestGitFetch_RealRepo(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	// A repo with no remote configured: `jj git fetch` should fail cleanly
	// rather than hang, which is enough to exercise the error path.
	dir := t.TempDir()
	runIn(t, dir, "git", "init")

	r := dirRunner{dir: dir}
	err := jj.GitFetch(context.Background(), r)
	require.Error(t, err)
}

func TestLogPreview_RealRepo(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := repoWithMergedBookmark(t)
	r := dirRunner{dir: dir}

	var buf strings.Builder

	err := jj.LogPreview(context.Background(), r, &buf, "all()", false)
	require.NoError(t, err)
	require.NotEmpty(t, buf.String())
}

// repoWithBookmarkAncestorOfTrunk creates a temp repo where "merged" is a
// strict ancestor of trunk (main) — unlike repoWithMergedBookmark, main is
// advanced one more commit past the bookmark, so "merged" != trunk. This is
// the case DESIGN.md's cascade safety-tier note relies on: a bookmark
// that's part of trunk's own history should have an empty private chain.
func repoWithBookmarkAncestorOfTrunk(t *testing.T) (string, string) {
	t.Helper()

	dir := initRepo(t)
	runIn(t, dir, "new", "-m", "feature work")
	runIn(t, dir, "bookmark", "create", mergedBookmarkName, "-r", "@")
	changeID := strings.TrimSpace(runIn(t, dir, "log", "--no-graph", "-r", "@", "-T", "change_id"))
	runIn(t, dir, "new", "-m", "more trunk work")
	runIn(t, dir, "bookmark", "set", "main", "-r", "@")
	runIn(t, dir, "new", "main")

	return dir, changeID
}

// repoWithPrivateBookmark creates a temp repo where "private" sits on two
// commits never merged into trunk (main).
func repoWithPrivateBookmark(t *testing.T) (string, string) {
	t.Helper()

	dir := initRepo(t)
	runIn(t, dir, "new", "main", "-m", "private work 1")
	runIn(t, dir, "new", "-m", "private work 2")
	runIn(t, dir, "bookmark", "create", "private", "-r", "@")
	changeID := strings.TrimSpace(runIn(t, dir, "log", "--no-graph", "-r", "@", "-T", "change_id"))
	runIn(t, dir, "new", "main")

	return dir, changeID
}

// repoWithSharedForkBase creates a temp repo with two anonymous fork heads
// (a, b) that share a private, unbookmarked base commit.
func repoWithSharedForkBase(t *testing.T) (string, string, string, string) {
	t.Helper()

	dir := initRepo(t)
	runIn(t, dir, "new", "main", "-m", "shared base")
	base := strings.TrimSpace(runIn(t, dir, "log", "--no-graph", "-r", "@", "-T", "change_id"))
	runIn(t, dir, "new", "-m", "fork a")
	headA := strings.TrimSpace(runIn(t, dir, "log", "--no-graph", "-r", "@", "-T", "change_id"))
	runIn(t, dir, "new", base, "-m", "fork b")
	headB := strings.TrimSpace(runIn(t, dir, "log", "--no-graph", "-r", "@", "-T", "change_id"))
	runIn(t, dir, "new", "main")

	return dir, headA, headB, base
}

func changeIDs(t *testing.T, out string) []string {
	t.Helper()

	var ids []string

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line != "" {
			ids = append(ids, line)
		}
	}

	return ids
}

func TestPrivateChainRevset_RealRepo_BookmarkOnTrunkAncestry_DegradesToEmpty(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, changeID := repoWithBookmarkAncestorOfTrunk(t)
	r := dirRunner{dir: dir}

	keep := classify.KeepRevset("main", "bookmarks()")
	out, err := jj.Log(context.Background(), r, classify.PrivateChainRevset(changeID, keep),
		`self.change_id() ++ "\n"`)
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(out),
		"a bookmark that's already an ancestor of trunk must have an empty private chain")
}

// bookmarksExceptSelf mirrors run.go's bookmarksExceptSelf: bookmarks()
// with name excluded, so a bookmark's own commit doesn't self-protect its
// private chain (::(bookmarks()) would otherwise always include it).
func bookmarksExceptSelf(name string) string {
	return fmt.Sprintf(`bookmarks() ~ bookmarks(exact:%q)`, name)
}

// TestCascadeAbandon_RealRepo_MergedBookmark_AlwaysDeletesRef reproduces a
// real bug found in production use: a bookmark already merged into trunk
// (an ancestor of it) has an empty private chain (see
// TestPrivateChainRevset_RealRepo_BookmarkOnTrunkAncestry_DegradesToEmpty),
// so jj.Abandon on that empty revset is a genuine no-op — it never touches
// the bookmark's own commit, so jj's own "delete the bookmark that pointed
// at an abandoned commit" behavior never fires. Marking such a bookmark
// with review's cascade/abandon key used to run *only* jj.Abandon on the
// (empty) private chain, silently leaving the bookmark in place no matter
// how many times the user repeated the action. internal/review's applyCmd
// now always runs the primary jj.BookmarkDelete for cascade-marked items
// too (see model.go's applyCmd doc comment) — this exercises that exact
// combination (BookmarkDelete + an empty-chain Abandon) against a real jj
// repo and confirms the bookmark actually disappears.
func TestCascadeAbandon_RealRepo_MergedBookmark_AlwaysDeletesRef(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, changeID := repoWithBookmarkAncestorOfTrunk(t)
	r := dirRunner{dir: dir}

	keep := classify.KeepRevset("main", bookmarksExceptSelf(mergedBookmarkName))
	chain := classify.PrivateChainRevset(changeID, keep)

	out, err := jj.Log(context.Background(), r, chain, `self.change_id() ++ "\n"`)
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(out), "sanity check: this bookmark's private chain is empty")

	// The exact pair of calls applyCmd now issues for a cascade-marked
	// item: the primary delete always runs, then the (here, no-op) abandon.
	require.NoError(t, jj.BookmarkDelete(context.Background(), r, []string{mergedBookmarkName}))
	require.NoError(t, jj.Abandon(context.Background(), r, []string{chain}))

	list := runIn(t, dir, "bookmark", "list")
	require.NotContains(t, list, mergedBookmarkName,
		"the bookmark must be gone even though its private chain was empty")
}

func TestPrivateChainRevset_RealRepo_PrivateHistory_ResolvesChain(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, changeID := repoWithPrivateBookmark(t)
	r := dirRunner{dir: dir}

	keep := classify.KeepRevset("main", bookmarksExceptSelf("private"))
	out, err := jj.Log(context.Background(), r, classify.PrivateChainRevset(changeID, keep),
		`self.change_id() ++ "\n"`)
	require.NoError(t, err)

	ids := changeIDs(t, out)
	require.Len(t, ids, 2, "the bookmark's own commit plus its one private ancestor")
	require.Contains(t, ids, changeID)
}

func TestAbandon_RealRepo_PrivateChain_RemovesBookmarkAsSideEffect(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, changeID := repoWithPrivateBookmark(t)
	r := dirRunner{dir: dir}

	keep := classify.KeepRevset("main", bookmarksExceptSelf("private"))
	require.NoError(t, jj.Abandon(context.Background(), r, []string{
		classify.PrivateChainRevset(changeID, keep),
	}))

	out := runIn(t, dir, "bookmark", "list")
	require.NotContains(t, out, "private",
		"abandoning the chain that includes the bookmark's own commit must remove the ref too")
}

func TestPrivateChainRevset_RealRepo_SharedForkBase_ProtectedUnlessBothMarked(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir, headA, headB, base := repoWithSharedForkBase(t)
	r := dirRunner{dir: dir}

	// Marking only fork A: keep must include fork B's ancestry, so the
	// shared base survives.
	keepProtectingB := classify.KeepRevset("main", "bookmarks()", headB)
	out, err := jj.Log(context.Background(), r, classify.PrivateChainRevset(headA, keepProtectingB),
		`self.change_id() ++ "\n"`)
	require.NoError(t, err)
	require.NotContains(t, changeIDs(t, out), base,
		"the shared base must not be abandoned while the other fork still depends on it")

	// Marking both forks: neither protects the other, so the shared base
	// is no longer needed by anything and both chains include it.
	keepNeither := classify.KeepRevset("main", "bookmarks()")
	outA, err := jj.Log(context.Background(), r, classify.PrivateChainRevset(headA, keepNeither),
		`self.change_id() ++ "\n"`)
	require.NoError(t, err)
	require.Contains(t, changeIDs(t, outA), base)
}

// TestGitCommitDuplicate_RealRepo reproduces the exact scenario
// classify.GitCommitDuplicates exists to catch: running `git commit`
// directly in a colocated repo (instead of `jj commit`/`jj describe`)
// leaves jj's own prior working-copy commit behind as an orphaned
// anonymous fork, sibling to the new commit git created — same parent,
// byte-identical diff, but never described. This is the delicate part
// worth a real jj binary (not a fake Runner): it depends on
// self.parents().map(...) and hash(self.diff().git()) actually being
// deterministic and comparable across two independently-queried commits.
func TestGitCommitDuplicate_RealRepo(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := t.TempDir()
	runIn(t, dir, "git", "init", "--colocate")
	runIn(t, dir, "describe", "-m", "root change")
	runIn(t, dir, "bookmark", "create", "main", "-r", "@")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("a\n"), 0o600))
	runIn(t, dir, "commit", "-m", "first")

	// Edit the new (empty) working-copy commit's content, then commit it
	// with a raw `git commit` — bypassing jj entirely — rather than
	// `jj commit`/`jj describe`. A jj command (`status` here) has to run
	// in between: colocation only snapshots the edit into jj's own
	// tracked working-copy commit when jj itself runs, and without that
	// snapshot jj's still-empty working-copy commit just gets silently
	// discarded when the working copy moves on — leaving no orphan behind
	// at all, since there'd be nothing to compare against.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("a\nb\n"), 0o600))
	runIn(t, dir, "status")

	gitAdd := exec.CommandContext(context.Background(), "git", "add", "-A")
	gitAdd.Dir = dir
	out, err := gitAdd.CombinedOutput()
	require.NoError(t, err, string(out))

	gitCommit := exec.CommandContext(context.Background(), "git", "commit", "-m", "raw git commit")
	gitCommit.Dir = dir
	out, err = gitCommit.CombinedOutput()
	require.NoError(t, err, string(out))

	r := dirRunner{dir: dir}

	forksOut, err := jj.Log(
		context.Background(), r, classify.AnonymousForks(), classify.TemplateWithDuplicateKey,
	)
	require.NoError(t, err)

	forks, err := classify.ParseCandidates(forksOut)
	require.NoError(t, err)

	keptOut, err := jj.Log(
		context.Background(), r, classify.KeptHistory(), classify.TemplateWithDuplicateKey,
	)
	require.NoError(t, err)

	kept, err := classify.ParseCandidates(keptOut)
	require.NoError(t, err)

	duplicates, rest := classify.GitCommitDuplicates(forks, kept)
	require.Len(t, duplicates, 1,
		"the orphaned pre-git-commit working-copy commit must be detected as a duplicate")
	require.Empty(t, rest)
	require.False(t, duplicates[0].HasDescription(),
		"the orphaned commit is jj's own working-copy commit, which is never described")
}
