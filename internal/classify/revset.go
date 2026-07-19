// Package classify builds the revsets and parses the jj output that
// determine which bookmarks and commits are cleanup candidates. It never
// shells out itself — internal/jj owns all subprocess execution.
package classify

import (
	"fmt"
	"strings"
)

// MergedBookmarks returns the revset for bookmarks that are ancestors of
// trunk, excluding trunk's own commit (used by the `bookmarks` command).
//
// The `~ trunk` exclusion is load-bearing: jj's `::x` revset syntax
// includes x itself, so without it a bookmark sitting exactly on trunk
// (e.g. main) would be misclassified as merged and deleted under --apply.
func MergedBookmarks(trunk string) string {
	return fmt.Sprintf("bookmarks() & ::(%s) ~ (%s)", trunk, trunk)
}

// UnmergedBookmarks returns the complement of MergedBookmarks: bookmarks
// that are NOT ancestors of trunk. This is the pool the `bookmarks`
// command's heuristic lenses (ProbablyMerged, Stale) examine, since a
// squash-merged or long-abandoned bookmark never becomes a graph-ancestor
// of trunk even though it's just as safe to clean up.
//
// `~ immutable()` excludes bookmarks whose own tip commit jj won't let
// cascade-abandon touch — typically a remote-tracked branch jj protects
// as published even though its content already landed in trunk under a
// different commit (a squash-merge whose remote branch was never
// cleaned up). Without this, jj-trim offers such a bookmark for cascade
// and jj then rejects the whole cascade batch outright, taking every
// other bookmark's cleanup down with it (cascade-abandon's private-chain
// revset always includes the candidate's own commit — see
// PrivateChainRevset).
func UnmergedBookmarks(trunk string) string {
	return fmt.Sprintf("bookmarks() ~ ::(%s) ~ immutable()", trunk)
}

// AnonymousForks returns the revset for the `commits` command's candidate
// set: mutable heads that aren't pointed to by any bookmark and aren't the
// working-copy commit itself.
func AnonymousForks() string {
	return "heads(mutable()) ~ bookmarks() ~ @"
}

// AnonymousForksNoDescription intersects AnonymousForks with commits that
// have no description at all, the highest-confidence cleanup target within
// the `commits` command.
func AnonymousForksNoDescription() string {
	return fmt.Sprintf(`(%s) & description("")`, AnonymousForks())
}

// KeptHistory returns the revset for everything already reachable from the
// working copy or a bookmark — i.e. content that survives regardless of
// what happens to anonymous forks. Used to detect anonymous forks whose
// full diff duplicates a sibling already here (see GitCommitDuplicates) —
// almost always the artifact of running `git commit` directly in a
// colocated repo, which leaves jj's own working-copy commit behind as an
// orphaned sibling of the new commit git created.
func KeptHistory() string {
	return "(::@ | bookmarks()) ~ root()"
}

// ChangeIDRevset wraps a literal change id in jj's change_id() revset
// function rather than interpolating it bare. A bare change-id symbol is
// ambiguous — and the whole containing revset fails outright — when that
// id happens to be divergent (two or more visible commits sharing it,
// e.g. left behind by an `jj op restore`); change_id() resolves to the
// union of every commit under the id instead, so a single divergent
// candidate can't break batch construction for every other candidate in
// the same session. Every call site that splices a Candidate.ChangeID (or
// any other literal change id) into a revset string should go through
// this rather than interpolating it directly.
func ChangeIDRevset(id string) string {
	return fmt.Sprintf("change_id(%s)", id)
}

// DescendantsRevset returns the revset for the mutable descendants of
// changeID, excluding changeID itself — used by the review stepper to
// show what would be rebased before abandoning a candidate.
func DescendantsRevset(changeID string) string {
	id := ChangeIDRevset(changeID)

	return fmt.Sprintf("descendants(%s) & mutable() ~ %s", id, id)
}

// KeepRevset returns the set of commits a private-chain abandon must never
// touch: ancestors of trunk, of bookmarksExpr (the caller supplies this
// rather than a hardcoded `bookmarks()` since the bookmarks-review cascade
// call site needs its own candidate's name(s) excluded from that set, or a
// bookmark trivially self-protects its own chain and cascade always
// degrades to a no-op), of the working copy, and of any extraRoots (e.g.
// other fork candidates' heads, so two stacks sharing a private base don't
// clobber each other when only one is marked in the same review session).
func KeepRevset(trunk, bookmarksExpr string, extraRoots ...string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "::(%s) | ::(%s) | ::@", trunk, bookmarksExpr)

	for _, root := range extraRoots {
		fmt.Fprintf(&b, " | ::(%s)", ChangeIDRevset(root))
	}

	return b.String()
}

// PrivateChainRevset returns the revset for candidateID plus every
// ancestor of it that isn't already covered by keep (see KeepRevset) — the
// commits safe to abandon alongside candidateID. jj's `::x` syntax includes
// x itself, so when candidateID is already an ancestor of trunk (the
// common case for a literally-merged bookmark) this evaluates to empty:
// cascade correctly degrades to a no-op abandon with no special-casing.
func PrivateChainRevset(candidateID, keep string) string {
	return fmt.Sprintf("::(%s) ~ (%s)", ChangeIDRevset(candidateID), keep)
}
