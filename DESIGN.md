# jj-trim: design doc

> Items still needing a decision are marked **OPEN:** — `grep -n 'OPEN:'
> DESIGN.md IMPLEMENTATION.md` to find all of them across both docs.

## Problem

jj bookmarks have no auto-cleanup mechanism: once merged into trunk, or once
their remote counterpart is deleted, they linger forever cluttering
`jj bookmark list` / `jj log`. Separately, jj makes it trivial to fork off
experimental work (`jj new`) without naming it — these anonymous heads also
accumulate, some abandoned with no description, some half-finished. Both are
the same underlying problem (commit-graph clutter) and the same underlying
fix (find safe-to-remove tips, show them clearly, remove on confirmation).
`git-trim` solves the bookmark half for git; jj's primitives (`trunk()`,
`mutable()`, the operation log, native graph log) make a more complete
solution possible for jj.

## Landscape

No purpose-built equivalent of `git-trim` exists for jj as of this writing.
Checked: the official [community tools page](https://docs.jj-vcs.dev/latest/community_tools/)
(17 tools — GUIs, TUIs, IDE plugins, diff editors; none describe bookmark or
commit cleanup), [awesome-jj](https://github.com/Necior/awesome-jj), and a
crates.io/GitHub search for `jj-trim`/`jj-clean`/`jj-tidy` (no hits).

What does exist, and how it changes this doc's scope:

- **jj already auto-deletes "stray" bookmarks on fetch.** `jj git fetch`
  removes local bookmarks whose tracked remote counterpart was deleted
  upstream. This is exactly Part 1's `stray` bucket — jj covers it natively,
  so that classification is closer to "nothing to do" than a feature to
  build. Worth re-scoping Part 1 to focus on `merged`, where jj has no
  built-in equivalent.
- **`jj abandon` cascades bookmark deletion.** Abandoning a commit deletes
  any bookmark pointing at it. This means Part 2's action step doesn't need
  separate bookmark-cleanup logic for forks that happen to carry a
  bookmark — abandoning the commit is sufficient.
- **[tommymorgan/jj-tools](https://github.com/tommymorgan/jj-tools)** does
  narrow, single-purpose cleanup: it deletes its own auto-generated
  temporary bookmarks once their associated stacked PR merges. It doesn't
  do general merged-bookmark detection, doesn't touch remote-tracking
  bookmarks beyond its own, and has no commit-fork cleanup.
- **No tool addresses anonymous/no-description commit-fork cleanup at all**
  (Part 2 of this doc). That appears to be a genuine gap, not just an
  underserved corner.

Net effect: the real unclaimed territory is (a) general `merged`-bookmark
detection across an arbitrary repo (not scoped to one tool's own bookmarks),
with graph-based preview, and (b) anonymous-fork discovery/cleanup. The
"stray" bucket and "abandon cascades bookmarks" behavior are already solved
by jj itself and should be treated as building blocks this tool composes,
not gaps it fills.

## Goals

- Identify bookmarks safe to delete because they're merged into trunk
  (the gap jj doesn't already cover — see Landscape).
- Identify anonymous commit forks safe to abandon: not on any bookmark, not
  ancestors of `@` or trunk, optionally filtered by description/age.
- Preview exactly what will be removed using jj's own graph log, before
  acting.
- Be at least as safe as `git-trim`, while taking advantage of jj's
  operation log (`jj undo` / `jj op revert`) to justify lower-friction
  defaults than git's branch model allows.

## Non-goals (v1)

- Replacing `jj bookmark` or `jj abandon` subcommands — this tool composes
  them.
- Rewriting jj's revset/log rendering — reuse it as-is.

## Part 1: Bookmark cleanup

Re-scoped per [Landscape](#landscape): jj already auto-deletes `stray`
bookmarks (tracked remote deleted upstream) as a side effect of
`jj git fetch`, so that's not a gap to fill — it's a prerequisite step
(`jj-trim` should just run, or instruct the user to run, `jj git fetch`
first). The actual gap, and this tool's primary job in Part 1, is `merged`
detection: jj has no built-in "delete bookmarks already merged into trunk"
command, unlike `stray` which is already automatic.

### Concepts mapped from git-trim

| git-trim concept                  | jj equivalent                                                    | Notes                                                                                                                                                           |
| --------------------------------- | ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--bases` (detect default branch) | `trunk()` revset                                                 | Built into jj; no heuristic needed                                                                                                                              |
| "merged" branch                   | `bookmark & ::trunk() ~ trunk()`                                 | **Primary target.** True ancestor check; doesn't catch squash-merges. The `~ trunk()` exclusion is load-bearing — see the correctness note under Classification |
| "stray" branch (upstream deleted) | tracked remote bookmark whose `@remote` ref disappeared on fetch | **Not a gap** — `jj git fetch` already does this. Treated as a precondition, not built                                                                          |
| "diverged" branch                 | conflicted bookmark                                              | Out of v1 scope — see Future ideas                                                                                                                              |
| `--protected` globs               | same — glob match against bookmark name                          | Direct port                                                                                                                                                     |
| branch deletion                   | `jj bookmark delete` vs `jj bookmark forget`                     | merged+tracks-remote → `delete`; merged local-only → `forget`                                                                                                   |
| single upstream per branch        | bookmark can track N remotes                                     | Only matters if `diverged` handling is added later                                                                                                              |

### Classification

1. `merged`: commit is an ancestor of `trunk()`, **excluding `trunk()`'s own
   commit**. Provably safe — the only bucket `bookmarks apply` deletes
   without any review step.
2. `probably-merged` (heuristic): not an ancestor of trunk, but the
   bookmark's own commit description appears verbatim somewhere in trunk's
   history — the signature of a GitHub-style squash-merge, which creates a
   brand-new commit on trunk (so the ancestor check misses it) while
   typically preserving the original commit message(s) in the merge/squash
   commit body. Checked once per `bookmarks` run against the full trunk
   history (`internal/jj.TrunkHistory`), not per-bookmark — cheap, no extra
   `jj` subprocess calls per candidate.
3. `stale` (heuristic): not an ancestor, no message match, but old (past
   `--stale-after`, default 90 days) **and** not tracking any remote. Age
   alone isn't a safety signal — an old bookmark could still be genuine
   unfinished work — so it's gated on "never pushed" too, to distinguish
   forgotten local scratch work from a bookmark still tracking an open PR.
4. `protected`: matches a `--protected` glob — reported, never touched.
5. `keep`: none of the above.

Found against a real repo during this session: 16 of 17 non-trunk
bookmarks in a personal project weren't literal ancestors, yet at least
one (`build/cpd`) was confirmed squash-merged by finding its exact commit
message inside a `Merge pull request` commit already in trunk. The
ancestor check alone badly under-detects for anyone using squash-merge PRs
— `probably-merged`/`stale` exist specifically to close that gap.

**Safety tier, deliberately asymmetric**: `merged` is the only bucket
`apply` touches (provably safe, no false-positive risk). `probably-merged`
and `stale` are heuristics — real, but not proof — so they only ever widen
what's _shown_ in the preview/legend and the `review` TUI; getting from
"flagged" to "deleted" always requires a human look via `review` (or
manual `jj bookmark delete` once you've eyeballed it), never the bulk
`apply` subcommand. Within `review`, deleting the bookmark ref and
abandoning the commit chain it points at are two distinct, explicit
per-item choices — see "What does trimming actually do" below — never
bundled or automatic.

**Correctness note**: jj's `::x` revset includes `x` itself ("ancestors of
`x`, including the commits in `x` itself" — `jj help -k revsets`). A naive
`bookmark & ::trunk()` therefore matches the bookmark(s) sitting exactly
_on_ trunk — e.g. `main` itself, since `main` is trivially an ancestor of
its own commit. Without the `~ trunk()` exclusion, `bookmarks apply` would
classify `main` as `merged` and delete it. This was caught during a design
audit, not exercised by any test — found by re-reading the revset, not by
running the tool — so it's worth double-checking once `internal/classify`
has tests: a fixture with a bookmark sitting exactly on `trunk()` should be
the very first table-driven case for the `merged` classifier.

`stray` is intentionally absent from this list — run `jj git fetch` to get
that cleanup from jj itself before running `jj-trim`.

## Part 2: Anonymous commit-fork cleanup

### Candidate revset

Anonymous head candidates: `heads(mutable()) ~ bookmarks() ~ @`
(mutable heads, minus anything a bookmark points to, minus the working-copy
commit itself, which is never a cleanup candidate).

Within that candidate set, sub-filter for review:

- **Git-bypass duplicate** (`classify.ReasonGitCommitDuplicate`, highest
  confidence, shown first): the fork's full diff is byte-identical to a
  commit already reachable from `@` or a bookmark (`classify.KeptHistory()`
  — `(::@ | bookmarks()) ~ root()`) with the same parent(s). Detected via
  two `-T` template fields — `self.parents().map(|p| p.change_id())` and
  `hash(self.diff().git())` — compared as one key
  (`Candidate.DuplicateKey()`, parents sorted so merge-commit parent order
  doesn't matter) by `classify.GitCommitDuplicates`. This is almost always
  the artifact of running `git commit` directly in a colocated repo instead
  of `jj commit`/`jj describe`: jj's own prior working-copy commit is left
  behind as an orphaned sibling of the new commit git created — same
  parent, same content, never described. Provably safe to abandon
  regardless of whether the kept sibling itself has a description, since
  the content survives verbatim either way. Confirmed empirically against a
  real, actively-used repo (`~/Code/hrd`): 11 of 31 anonymous-fork
  candidates there were exact duplicates by this measure. Still
  interactive-only, through `commits review`/`commits preview` — no new
  non-interactive bulk-apply path for `commits` (that policy, discussed
  below, is unchanged).
- **No description**: `description("")` intersected with the candidate set
  (minus anything already classified as a git-bypass duplicate above) —
  almost certainly throwaway/forgotten work, next-highest-confidence
  cleanup target.
- **Has description**: kept but never auto-removed — requires explicit
  review since it represents named/intentional work that just never got a
  bookmark.
- **Age**: sort/filter by commit date (via `-T` template, not a revset
  primitive) to surface the oldest, least-likely-to-be-active forks first,
  within each of the buckets above.

### Action

Default action for confirmed candidates is `jj abandon`, never silent —
always shown in the preview graph first (see Visualization), and per
[the non-destructive-by-default decision](#decisions)
nothing is abandoned without the user explicitly opting in per-candidate via
`commits review` (see CLI surface). No remote interaction is involved
here, so there's no delete/forget distinction.

**Marking a candidate abandons its whole private commit chain, not just
the head.** The candidate revset above (`heads(mutable()) ~ bookmarks() ~
@`) only ever surfaces the _tip_ of a stack — an earlier version of this
tool abandoned exactly the marked change id and nothing else, which meant
a 5-commit private stack needed 5 separate `commits review` passes to
fully clean (each abandon exposes the next commit down as a new head, only
caught on a later run). Fixed by computing, per candidate, the revset
`::head ~ keep` — every ancestor of the marked head that isn't already
covered by `keep` — and passing that whole expression to a single `jj
abandon` call. `keep` is `::trunk() | ::bookmarks() | ::@`, plus every
_other_ fork candidate's own ancestry unioned in: fork heads are never
bookmarked, so without this last part, two stacks sharing a private base
commit would each think they own it, and abandoning one would delete
commits the other still needs. With it, the shared base survives unless
every fork built on it is marked in the same session — see
`internal/classify.KeepRevset`/`PrivateChainRevset`.

## Visualization & review UX

### Preview graph (both parts)

Primary preview mechanism: **reuse `jj log` itself**, scoped to the
candidate revset, e.g.:

```text
jj log -r '(bookmarks() & ::trunk() ~ trunk()) | (heads(mutable()) ~ bookmarks() ~ @)'
```

This gets jj's native ASCII DAG, colors, and commit metadata for free —
candidates are just a revset passed to the existing renderer, not a custom
UI. This static graph is sufficient for Part 1 (bookmark merges are
low-ambiguity: a bookmark is either an ancestor of trunk or it isn't).

**Annotation: a separate legend, not an inline jj template.** An earlier
draft of this section proposed annotating _why_ each commit is a candidate
(`merged`, `no description`, `anonymous fork`) inline via a `-T` template
override. That doesn't actually work cleanly: jj's template language has
no way to look up a classification result `jj-trim` already computed in Go
— the template would have to _re-derive_ `merged`/`stray`/`no-description`
itself via revset membership tests inside jj's templating language,
duplicating `internal/classify`'s logic in a second language. Instead:
print the **unannotated** `jj log` graph (default template, full color),
followed by a short legend `jj-trim` prints itself, mapping each candidate's
change-id prefix (as it appears in the graph) to its reason — built
directly from the classification data already in hand, no template
trickery, no reparsing of jj's output.

**Color and pager**: `jj-trim` always passes `--no-pager` to the preview
invocation — it owns the terminal flow from here (the legend, and the
`review` TUI, both follow immediately), so jj's interactive pager would
only get in the way. Color is decided explicitly by `jj-trim`
checking whether _its own_ stdout is a terminal, and passing
`--color=always` or `--color=never` to jj accordingly — rather than
relying on jj's own auto-detection of a file descriptor that may be
captured/piped by `jj-trim`'s subprocess plumbing. This keeps coloring
correct regardless of how the subprocess output is captured internally.

### Why a static graph isn't enough, for either part

Part 1 was originally thought "binary and low-risk enough to judge at a
glance" — but the `probably-merged`/`stale` heuristic buckets (see
Classification) mean `bookmarks` candidates now carry real uncertainty too,
not just Part 2's anonymous forks. Both parts need the same thing: context
(what's actually in this commit, why was it flagged), the ability to look
at several candidates and _compare_ before committing to anything, and a
way to change your mind mid-review. A one-shot annotated list doesn't give
you that.

### Interactive review flow: one shared TUI for both parts

**Revised from the original plain-stdin-loop design** (see "Why this
isn't a plain prompt loop, after all" below for why). `bookmarks review`
and `commits review` launch the same interactive flow, built on
[Bubbletea](https://github.com/charmbracelet/bubbletea) (the same
`charm.land` stack already proven in `../hrd`'s own TUI):

1. **List screen**: a navigable, always-revisitable list of every
   candidate (for `bookmarks`: `merged` + `probably-merged` + `stale`; for
   `commits`: the anonymous-fork set, oldest first), each row showing its
   legend line (bookmark name/change id, reason, diffstat) with a marker
   for its current decision. A detail pane below shows the selected
   candidate's context — `jj show` output, plus (for `commits`, and for
   `bookmarks`' cascade choice) a preview of exactly what would be
   abandoned alongside it. A footer shows a live tally and key hints.
   - **Nothing starts marked, ever, in `review` — not even the certain
     `merged` bucket.** `review` means "make an explicit choice for
     everything you see"; `apply` is the separate, non-interactive
     subcommand for bulk-deleting the certain bucket without a review
     step at all. An earlier draft of this design pre-marked `merged`
     bookmarks by default (reasoning that `apply` would delete them
     unconditionally anyway), but that made the TUI's very first screen
     look like something had already been selected before the user did
     anything — corrected here.
   - **`bookmarks review` offers two distinct per-item choices, not one**:
     delete the bookmark ref only (matching `apply`'s scope), or delete +
     abandon the private commit chain it points at. `commits review` has
     only one choice (abandon), since a fork candidate has no ref to
     consider separately. The two keys that mark a row are never
     hardcoded — they're derived from the action's own verb, first letter
     lowercased (`review.Action.Verb`/`CascadeAction.Verb`), so the key a
     user reads in the footer is always the key that produces it:
     `bookmarks review` binds `d` (delete) and `a` (abandon);
     `commits review` binds only `a` (abandon). A marked row shows the
     same letter, uppercased, in place of the pending `·` — what's on the
     row is literally the key that put it there. A dedicated `u` key
     clears a row's decision unconditionally from either state; pressing
     a row's own mark key again also clears it (same "toggle twice to
     change your mind" behavior as before); pressing the _other_ action's
     key while already marked switches the row to that state instead of
     stacking.
   - Navigating (↑/↓, j/k) and marking are independent actions on the
     same always-visible list — this **is** the "go back and change your
     mind" navigation: there's no separate back-key, because nothing is
     ever really "behind" you.
2. **Confirm screen** (`enter`): lists exactly what's currently marked
   under each action, and requires an explicit second `enter` to actually
   run the batch operation(s) — `esc` goes back to the list without
   applying anything. This is the "either cancel or apply" gate: nothing
   is written to the repo from the list screen itself, only from this
   explicit confirmation.
3. Applying runs as **one independent batch call per action that has
   marked items** — e.g. `bookmarks review` with both delete- and
   abandon-marked rows runs a `jj bookmark delete <n1> <n2> ...` batch and
   a separate `jj abandon <chain1> <chain2> ...` batch, in either order
   (an abandon batch that includes a bookmark's own commit already removes
   that bookmark as a side effect — jj cascades bookmark deletion when the
   commit it points at is abandoned — so the two batches never touch the
   same ref twice). Each batch lands as its own `jj op log` entry — `jj op
   revert <opid>` reverts a specific one regardless of what's happened
   since (unlike bare `jj undo`, which always targets only the single most
   recent operation).
4. Show a **confirmation popup** — what was just applied (or the error, if
   the batch failed) and, on success, one "Undo with: jj op revert ..."
   line per batch that ran — rather than exiting. Any key dismisses it: on
   success, dismissing prunes the just-applied items from the list (they're
   done, not merely still-marked) and returns to the list screen; on
   failure, the list is left untouched so the item can be retried. A
   session can therefore review and apply several batches before actually
   quitting, rather than ending after the first one — the earlier design
   (see git history) always exited once a batch ran, which made "abandon
   ten forks" ten separate `commits review` invocations. The session's
   final `Result` (used for the "Undo with" lines `run.go` prints after the
   TUI exits) accumulates across every batch applied, not just the last.

**Interrupt semantics — cleaner than the old design.** `q`/`Esc`/Ctrl-C all
mean the same thing from the list or confirm screens: quit, apply nothing.
This resolves an awkwardness in an even earlier design, where `q` meant
"quit-and-apply-so-far" and had to be kept carefully distinct from Ctrl-C's
"abort, apply nothing" — two similar-sounding keys with different blast
radii. With an explicit confirm screen as the only path to the batch-apply
call, there's no in-between state left to disambiguate on those two
screens: either you reach the confirm screen and hit `enter`, or you
don't, and nothing happens. The one screen where `q`/Ctrl-C _don't_ mean
"nothing happened" is the applied confirmation popup itself: by the time
it's showing, the batch already ran (or failed) — quitting immediately
from there (without dismissing first) still reports that batch in the
final result, same as if it had been dismissed; the only difference
dismissal makes is letting the session continue instead of ending it.

**`review` requires a real interactive terminal**, same as before. If
stdin isn't a TTY when `review` is invoked, `jj-trim` fails fast with a
clear error before launching the TUI, rather than blocking on a program
that can never receive input (e.g. stdin redirected from `/dev/null` or a
pipe in a script).

### Why this isn't a plain prompt loop, after all

The original design (see git history) chose a plain stdin `[a/s/k/d/n/q]`
prompt loop specifically to avoid Bubbletea's weight, reasoning that a
"linear one-candidate-at-a-time prompt" didn't need a full TUI framework.
That reasoning held right up until two requirements were added: **going
back to reconsider an earlier candidate**, and a **persistent, live tally**
visible throughout the session. Neither is achievable cleanly with a
linear stdin loop — "going back" would mean re-prompting for candidates
already decided, with no way to show where you are in that history, and a
"live" tally requires redrawing state continuously, which is exactly what
a raw sequential `read line, act, discard` loop can't do without turning
into an ad hoc partial reimplementation of a TUI anyway. Once the actual
requirements include non-linear navigation and persistent on-screen state,
a real TUI framework stops being overkill and starts being the right tool
— the same judgment call DESIGN.md already made in the other direction for
`hrd`'s dashboard (deep enough state to justify Bubbletea) versus jj-trim's
original one-shot loop (not deep enough, at the time). `../hrd`'s own TUI
(`internal/tui/`) was reused as the reference implementation for the
Bubbletea/`bubbles`/`lipgloss` idioms and the `teatest`-based testing
approach — jj-trim's TUI itself is far narrower (no command bar, mouse,
filtering, or persistent state file), just a list + detail pane + tally +
confirm screen.

[Landscape](#landscape) also surfaced `jj-fzf` and `lazyjj` as an
alternative interaction model (fzf-based/lazygit-style pickers); still
worth comparing against later (see Future ideas), but the in-process TUI
above is the v1 answer.

### TUI-first front end: bare `jj-trim` (internal/browse)

Bare `jj-trim` (no subcommand) is the primary, documented entry point:
it launches straight into the same list/detail/confirm screen `bookmarks
review`/`commits review` already use, defaulted to bookmarks mode with
today's default settings — no settings gate or menu screen first. The
browse screen's live list **is** the preview; there's no separate one-shot
preview step in this path. On top of `internal/review`'s existing screen,
`internal/browse` adds two chrome keys, handled before delegating
everything else (navigation, marking, confirm, apply, quit) to review's own
model unchanged:

- `tab` — toggle between bookmarks mode and commits mode, re-running
  classification with the current settings. Each mode keeps its own item
  set and action definition (bookmarks: delete + cascade-abandon; commits:
  abandon only) exactly as today; toggling just swaps which is on screen.
- `f` — open a filters overlay (trunk / protected globs / stale-after for
  bookmarks; no-description-only for commits). Saving re-classifies and
  refreshes whichever mode is active. `-R`/`--fetch` aren't editable here:
  fetch runs once before the browse session starts regardless of mode, and
  repository selects which `jj.Runner` is even in play, so neither has an
  in-session value to override.

There is no bulk-apply shortcut in the TUI (an earlier draft added one,
bound to `A`, mirroring `bookmarks apply`'s non-interactive certain-bucket
delete — removed as confusing in practice: two different "run without
review" affordances, one via a menu keypress and one via a separately
typed CLI command, wasn't worth the redundancy). `bookmarks apply` remains
available from the CLI for scripting; the TUI's only path to actually
deleting/abandoning anything is review's own mark-and-confirm flow.

Considered and set aside for v1: a single list merging bookmarks and
commits candidates together (grouped by reason) instead of a mode toggle —
it would need `internal/review`'s `Action` to become per-item instead of
per-session (some rows would need delete+abandon, others abandon-only, in
the same session). See Future ideas.

`internal/browse` never reimplements classification, querying, or
applying — it builds its two modes' item sets via the exact same functions
the CLI dispatch path below calls (`mergedBookmarks`, `heuristicBookmarks`,
`bookmarkReviewItems`, `anonymousForks`, `forkReviewItems`, ...), and reuses
`internal/review`'s model directly as an embedded child — the CLI
subcommands below and the TUI front end are two ways to reach the same
underlying code, not two implementations of it. Recovering the embedded
child's session outcome (op ids for the "Undo with: jj op revert ..."
message) after browse's own (outer) `tea.Program` exits needs one small
seam: `internal/review` exports a `FinishedSession` interface
(`Outcome() (Result, error)`) that its otherwise-unexported model
implements, since `browse.Run`'s `finalModel` is always browse's own model
type, never review's.

## CLI surface (sketch)

Two subcommand groups, `bookmarks` (Part 1) and `commits` (Part 2), each
nesting their own `preview`/`apply`/`review` leaf subcommands — rather
than one flat command with a `--commits`/`--apply`/`--review` flag pile,
or `--apply`/`--review` as boolean flags on `bookmarks`/`commits`
themselves. Subcommands make the three modes mutually exclusive by
construction (a boolean-flag pile doesn't stop `--apply --review` both
being passed at once), and let each leaf's `--help` show only the flags
relevant to it (`--protected`/`--trunk`/`--stale-after` are meaningless
for `commits` — Part 2 has no bookmark-glob or staleness concept and its
candidate revset doesn't reference `trunk()` at all — so they live under
`bookmarks` only). `preview` is the implicit default subcommand (Kong's
`default:"1"` tag), so a bare `jj-trim bookmarks` still previews — same
non-destructive-by-default invariant as a flag-based design, just
expressed structurally. Nothing is deleted or abandoned without
`bookmarks apply` or `bookmarks review` / `commits review`.

```text
jj-trim [-R|--repository <path>] [--fetch] [<command>]

  Bare `jj-trim` (no <command>) launches the TUI-first front end —
  see "TUI-first front end" above. Everything below remains available
  for scripting/CI, unaffected by the TUI.

  -R, --repository <path>   Repo to operate on (default: current directory,
                             same ancestor-search jj itself does). Passed
                             through as jj's own `-R` flag on every
                             invocation.
      --fetch                Run `jj git fetch` first, so jj's own
                              stray-bookmark cleanup applies before this
                              tool's merged-bookmark pass runs

Commands:
  bookmarks preview   Show what would be deleted (default)
  bookmarks apply     Delete merged (certain) bookmarks only
  bookmarks review    Interactive walk over merged/probably-merged/stale
                       bookmarks (see Visualization & review UX) — the
                       only way the heuristic buckets ever get deleted

    -p, --protected <glob,...>   Bookmarks never deleted (default: none)
    -t, --trunk <revset>         Override trunk() (default: trunk())
        --stale-after <duration>  Age threshold for the "stale, no remote"
                                  heuristic (default: 2160h / 90 days)

  commits preview   Show what would be abandoned (default)
  commits review    Interactive walk over anonymous fork candidates (see
                     Visualization & review UX) — the only way commits
                     candidates get abandoned; there is no non-interactive
                     bulk-apply for commits, only for bookmarks

        --no-description-only   Restrict to description("")

  install-completions   Install bash/zsh/fish shell completions (see
                         github.com/willabides/kongplete) — detects the
                         user's login shell and writes the appropriate
                         `complete` snippet to stdout.
      --man              Print a man page (see github.com/alecthomas/mango-kong)
                          and exit
      --version          Print version and exit
```

**`--protected` defaults to empty.** HH's call: no built-in defense-in-depth
bookmark-name list. The `~ trunk()` fix (see Classification) already
covers the actual correctness risk — trunk's own bookmark can't be
classified `merged` regardless of what `--protected` is set to — and the
remaining surface is already non-destructive by default (Decision 2: no
flag means nothing happens at all). A defense-in-depth default list would
be protecting against a danger that's already closed off twice over.
Matches `git-trim`'s own precedent (`--protected` has no `[default: ...]`
in `git trim --help`, unlike its `--bases`/`--delete` flags).

## Decisions

1. **Delivery form: standalone CLI, not necessarily Rust.** HH's call:
   standalone over a jj-native alias (matches the earlier reasoning — a
   thin binary, not constrained by what jj's alias/template system can
   express).

   On "why Rust": nothing here requires it. Every operation this design
   proposes — `jj log -r <revset>`, `jj show`, `jj bookmark delete`,
   `jj abandon` — goes through the public, stable jj CLI as a subprocess,
   not through jj's internals. The one path that _would_ pull Rust in is
   `jj-lib` (the crate the `jj` CLI itself is built on, published on
   crates.io, meant to eventually support GUIs/TUIs/servers too) — but
   jj's own architecture docs say explicitly that "a lot of thought has
   gone into making the library crate's API easy to use, but not much has
   gone into 'details' such as which collection types are used, or which
   symbols are exposed" — i.e. it's not presented as a stabilized
   embeddable API yet. Given this design's only interface to jj is
   revsets + a handful of subcommands (all expressible over the CLI),
   there's no reason to take on that instability. Revisit only if a future
   feature needs introspection `-T` templates can't express.

   **Language comparison**, scored against this tool's actual workload
   (build/parse revset strings, shell out, parse `-T template` output,
   render a graph the user already trusts from `jj log`, run a short
   interactive prompt loop or wrap `fzf`):

   | Language            | End-user install                                                                                                                                                                  | Dev-side dependency mgmt                                                                                               | Cross-compilation                                                                                    | Fit for the workload                                                                                                                                                                                                                  |
   | ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | **Go**              | Single static binary; `go install` for devs, prebuilt binaries (e.g. via `goreleaser`) for everyone else; trivial Homebrew tap                                                    | Go modules — minimal ceremony, no lockfile drama, no venv-equivalent to manage                                         | Built in, no toolchain (`GOOS`/`GOARCH` env vars, no cross/zig needed) — easiest of all options here | Good: `os/exec` + `encoding/json` cover subprocess+template-parsing cleanly; CLI libraries (cobra) are mature; weaker ecosystem for rich interactive prompts than Node, fine for the simple stepper sketched above                    |
   | **Rust**            | Single static binary; `cargo install`, prebuilt binaries via `cargo-dist`                                                                                                         | Cargo — solid, but slower iteration (compile times) for a tool this size                                               | Possible but needs more setup than Go (`cross`, target toolchains, musl for static linux binaries)   | Good, but no advantage over Go for this workload specifically, and `jj-trim` living in Rust invites (wrongly) assuming it should use `jj-lib` — see API-stability note above                                                          |
   | **Python**          | Needs an interpreter; `pipx`/`uv tool install` make this tolerable today, but it's not a single artifact unless bundled (`pyinstaller`/`shiv`), which adds its own packaging step | `pip`/`uv` — fine for devs, but end-user installs inherit whatever Python-version/venv friction the user's machine has | No native cross-compilation; bundlers are per-platform                                               | Fine: easiest language to prototype the revset/template string-munging in, `subprocess` + `json` are simple — but the install story is the weak point here, not the implementation                                                    |
   | **Node/TypeScript** | Same shape as Python — needs a runtime unless bundled (`bun build --compile`/`pkg`), which works but is an extra step                                                             | npm/pnpm — workable, but real dependency-tree weight for a tool this small                                             | Bundlers support it, similar effort to Python's                                                      | Best ecosystem for the **interactive review UX** specifically (`ink`, `inquirer` give a polished stepper almost for free) — but that's the one part of this design more amenable to wrapping `fzf` anyway, which is language-agnostic |
   | **Bash (+ `fzf`)**  | Zero install beyond the script itself + requiring `fzf`/`jj` on `PATH` — literally `curl \| sh` territory, matches `jj-fzf`'s own distribution model                              | None — no package manager, no lockfile, nothing to manage                                                              | N/A — shell is the lowest common denominator across platforms (modulo Windows/WSL)                   | Weakest fit as the logic grows: revset strings and `-T` template output need careful quoting, and anything beyond "build a command, run it, pipe to `fzf`" gets fragile in bash fast                                                  |

   **Decided: Go.** It matches Rust's single-static-binary distribution
   story (the property that actually matters for an install-and-forget CLI
   tool) while being faster to write and trivially cross-compilable, with
   no `jj-lib`-shaped temptation to reach for internals this design
   doesn't need.
2. **Default aggressiveness: non-destructive by default.** HH's call.
   Applied as: Part 1 (`jj-trim`) prints the annotated preview graph and
   does nothing else unless `bookmarks apply` is invoked. Part 2 has no
   non-interactive apply path at all — `commits review` is required, and
   even inside that flow nothing executes until the walk is confirmed at
   the end (see Visualization & review UX). This is intentionally
   stricter than git-trim's `--no-confirm`-to-opt-out model: here
   destructive action requires opting in, not opting out.
3. **Squash-merge detection: partially in v1, revised.** A message-match
   heuristic (`probably-merged`, see Classification) ships in v1 for Part
   1 bookmarks, since real usage this session showed the ancestor check
   alone badly under-detects for squash-merge workflows (16 of 17
   non-trunk bookmarks in a real repo weren't ancestors, yet at least one
   was confirmably squash-merged). It's explicitly a heuristic, not proof —
   see the Classification safety-tier note — so it doesn't change
   `apply`'s blast radius, only what `review` surfaces. A stronger
   content/patch-id comparison (`git cherry`-style) remains deferred, for
   both Part 1 and Part 2's "already merged content" sub-filter — see
   Future ideas.

## Tooling & CI

Modeled on `hugoh/hrd` (`../hrd`), an existing Go CLI of mine with a
working `mise` + `hk` + GitHub Actions setup. Reuse its shape directly;
the two differences are noted inline below.

- **`mise`** as the single entrypoint for tool versions and tasks (Go,
  golangci-lint, goreleaser, cocogitto, gotestsum, hk, rumdl, zizmor,
  dprint, ghalint, gitleaks, actionlint — pinned in `[tools]`, plus
  `go-test-coverage` and `govulncheck` as `go:`-installed tools). Tasks to
  port as-is: `lint` (mod tidiness, dead code, `golangci-lint`, goreleaser
  config check), `fix`, `test` (gotestsum + coverage profile), `coverage`
  (`go-test-coverage` against a threshold file), `ci` (lint + test +
  coverage + build), `full-check` (the pre-completion loop), `build`,
  `format` (`golangci-lint fmt` + `dprint fmt`), `tidy`, `depup`. **Skip**
  hrd's `gen`/`readmecheck` tasks (those exist because hrd generates
  `README.md` from a template via `cmd/genreadme`; not needed unless
  `jj-trim` grows the same template-driven README pattern). **Correction
  from an earlier draft of this section**: hrd's `test-int` task passes
  `-tags=integration` to `go test`, but no `//go:build integration` tag
  actually exists anywhere in hrd's source — that flag is inert, gating
  nothing. The real mechanism is a runtime `t.Skip()` when the `jj`/`git`
  binary isn't on `PATH` (see `initJJRepo()`), and the tests run as part of
  the normal `internal/...` suite either way. `jj-trim` should adopt the
  _real_ mechanism, not the vestigial flag: real temporary jj repos via
  `t.TempDir()` + `jj git init`, runtime-skipped if `jj` is missing, run as
  part of plain `go test ./...` — no separate `test-int` task or build tag
  needed, since CI always has `jj` available via `mise`. See
  `IMPLEMENTATION.md`'s testing strategy for the concrete fixture-builder
  pattern.
- **`hk`** (`hk.pkl`) as the git-hooks runner, same linter set: gomod-tidy,
  actionlint, merge-conflict/case-conflict/large-files/executables checks,
  gitleaks, ghalint, rumdl (markdown), dprint, zizmor (workflow security
  linting), shellcheck, conventional-commit (via `cog check`), duplication
  (`cpd`), goreleaser config check. Same four hook groups: `pre-commit`
  (stash + all linters), `pre-push` (linters + full `mise ci`),
  `fix`, `check`, plus `commit-msg` enforcing Conventional Commits.
- **`.github/workflows/ci.yml`**: same three-job shape — `hk` (runs `hk
  check --all`), `goci` (Go-version-pinned cache + `mise ci` + Codecov
  upload), `release` (cocogitto auto-bump + `goreleaser release` gated on
  a tag existing at `HEAD`, with a dry-run validation path on PRs). Same
  pinned-by-SHA action references as hrd (`actions/checkout`,
  `jdx/mise-action`, `actions/cache`, `codecov/codecov-action`,
  `cocogitto/cocogitto-action`) for supply-chain hygiene — `zizmor` lints
  for this directly.
- **`.goreleaser.yml`**: cross-compiled static binaries (`CGO_ENABLED=0`;
  linux/darwin/windows/freebsd × amd64/arm64/386/arm, with the same
  darwin/freebsd arch exclusions hrd uses), `nfpms` for deb/rpm, and a
  Homebrew cask via a tap repo — directly reusable, this is exactly the
  "single static binary, trivial Homebrew install" story the language
  comparison above is banking on for Go.
- **`.golangci.yml`**: `default: all` linters with hrd's specific disable
  list (`depguard`, `exhaustruct`, `godoclint`, `gomodguard`, `ireturn`,
  `noinlineerr`, `paralleltest`, `testpackage`, `varnamelen`, `wsl`) and
  test-file exclusions (`err113`, `noctx`, `varnamelen`, `goconst`,
  `gosec`) as the starting point — revisit only if `jj-trim`'s code hits a
  rule that's a poor fit (e.g. if the `review` TUI's subprocess
  orchestration trips `noctx` legitimately).
- **`.testcoverage.yml` / `codecov.yml`**: port the mechanism (profile-based
  threshold check via `go-test-coverage`, Codecov project status), but
  hrd's specific numbers (90% total, 100% on `backends/`) are tuned to
  hrd's structure — set `jj-trim`'s threshold once there's code to measure,
  don't copy the number blindly. The `component_management` /
  path-based override pattern (hrd: 100% on `backends/`) maps naturally to
  `jj-trim` if Part 1/Part 2 land in clearly separated packages — e.g. a
  stricter threshold on the revset-building/classification logic than on
  the interactive-prompt/`fzf`-wrapper code, which is harder to unit test.
- **Misc configs to port as-is**: `.renovaterc.json` (automerge linters,
  minor, and digests; monthly schedule; 7-day minimum release age),
  `cog.toml` (Conventional Commits config — `tag_prefix = "v"`, no
  changelog/bump-commit since `goreleaser`'s changelog covers that),
  `dprint.json` (JSON/Markdown/TOML/YAML formatting), `.markdownlint.json`
  (`MD013` line-length off, since prose-heavy docs like this one don't
  want hard wrapping), `.jscpd.json` (copy-paste detection thresholds),
  `.gitignore` (standard Go gitignore).
- **What hrd has that doesn't obviously port**: the `backends/` package
  split (hrd's git/jj multi-backend abstraction — `jj-trim` only ever
  talks to jj, no abstraction needed).
- **What hrd's `bubbletea`/`charm.land` TUI dependencies turned out to
  port after all**: the `review` flow was originally scoped as a plain
  stdin prompt loop specifically to avoid this weight (see "Why this isn't
  a plain prompt loop, after all" under Visualization & review UX for why
  that call was reversed). `jj-trim` now depends on the same
  `charm.land/bubbletea`, `charm.land/bubbles`, `charm.land/lipgloss`
  versions `../hrd` already pins, and reuses its `teatest`-based testing
  pattern — but not hrd's own `internal/tui/` code (command bar, mouse
  support, persistent state, filtering): jj-trim's TUI is a much narrower
  list + detail pane + tally + confirm screen.

## Future ideas (not v1)

- Squash-merge detection (diff/patch-id comparison against trunk, similar
  to `git cherry`) for both Part 1 and Part 2 — see Decisions.
- `diverged` bookmark handling (conflicted bookmarks) — riskier, likely
  needs explicit per-bookmark confirmation regardless of global default.
- Multi-remote-aware stray detection refinements.
- Config file support analogous to git-trim's `trim.*` git-config keys, via
  jj's own config layering.
- `fzf`-based picker for `review`, evaluated against the current
  Bubbletea TUI (see Visualization & review UX).
- Shell completion via [`willabides/kongplete`](https://github.com/willabides/kongplete)
  (bash/zsh/fish) — Kong doesn't provide this itself; kongplete hooks into
  Kong's own `Model()` introspection. Not adopted yet since it's an
  additional dependency with no concrete request driving it yet.
