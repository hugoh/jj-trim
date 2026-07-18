# jj-trim: Go implementation & testing design

> Items still needing a decision are marked **OPEN:** — `grep -n 'OPEN:'
> DESIGN.md IMPLEMENTATION.md` to find all of them across both docs.

## Scope

`DESIGN.md` settles the product-level decisions: Go as the implementation
language, a non-destructive-by-default CLI, and a Tooling & CI setup
modeled on `../hrd`. This doc is the next layer down — how the Go program
itself is structured and tested.

`../hrd` (same author, working Go CLI) was surveyed in detail as a
reference point: CLI bootstrapping, subprocess dispatch, backend
abstraction, parallel runner, testing fixtures, TUI. It is used here as
**one input, not a template** — every choice below is evaluated against
jj-trim's actual shape rather than copied, and several of hrd's choices are
explicitly rejected with reasons (see "Explicit non-adoptions").

The key shape difference driving most of these calls: hrd is a
**multi-repo, multi-backend dispatcher** with a `hrd git ...` / `hrd jj
...` / `hrd shell ...` subcommand tree and a full-screen TUI dashboard.
jj-trim is **two subcommand groups** (`bookmarks`, `commits`, mapping 1:1
onto Part 1/Part 2 — see DESIGN.md's CLI surface, each nesting
`preview`/`apply`/`review` leaves), operating on one repo, sharing a
single interactive review TUI (`review`, `internal/review/`).
Several of hrd's patterns exist specifically to solve problems jj-trim
doesn't have.

## CLI framework: Kong, not urfave/cli/v3

| Framework                      | Verdict for jj-trim | Why                                                                                                                                                                                                                                                                                                                                     |
| ------------------------------ | ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Kong**                       | **Chosen**          | Declarative struct-tag flags give type-safe binding (`Apply bool \`help:"..."\``) instead of stringly-typed lookups (`cmd.Bool("apply")`); a flags-only CLI is exactly Kong's sweet spot. Trivially testable — construct the struct directly, no app-bootstrap ceremony needed in tests. Effectively zero transitive dependency weight. |
| Cobra                          | Rejected            | Proven at scale (kubectl, `gh`, Hugo) but built for deep nested subcommand trees and pairs naturally with Viper-style config layering — both solve problems jj-trim doesn't have. Heavier than the CLI surface justifies.                                                                                                               |
| `urfave/cli/v3` (hrd's choice) | Rejected            | Its value is structuring many nested subcommands with shared dispatch logic (`hrd git`, `hrd jj`, `hrd shell`, plus TUI). jj-trim's `bookmarks`/`commits` is a shallow, 2-leaf split, not the deep multi-level dispatch tree urfave's machinery earns its weight on.                                                                    |
| stdlib `flag`                  | Rejected            | Genuinely simplest, but loses usage-text generation, subcommand dispatch, and typed slice/glob flags (`--protected <glob,...>`) that Kong gives for free; not worth hand-rolling for the flag count in DESIGN.md's CLI surface.                                                                                                         |

Kong's `cmd:""` struct tag handles jj-trim's subcommand tree natively — no
extra dependency needed for that, and the framework choice above is
otherwise unaffected. `bookmarks`/`commits` each nest their own
`preview`/`apply`/`review` leaf subcommands rather than exposing
`--apply`/`--review` as boolean flags — see cli.go's doc comments for why
(structural mutual exclusivity: nothing stops two boolean mode-select
flags from both being passed at once, but only one subcommand can ever be
selected). `default:"1"` on `Preview` makes it the implicit subcommand
when none is given, so `jj-trim bookmarks` with no further argument still
resolves to a safe preview — same non-destructive-by-default invariant as
before, just expressed structurally instead of via a flag's absence:

```go
type CLI struct {
    Repository string `help:"Path to repository to operate on" short:"R"`
    Fetch      bool   `help:"Run jj git fetch first"`

    Bookmarks BookmarksCmd `cmd:"" help:"Delete bookmarks already merged into trunk"`
    Commits   CommitsCmd   `cmd:"" help:"Clean up abandoned anonymous commits"`
}

type BookmarksCmd struct {
    Protected  []string      `help:"Bookmark name globs never deleted" short:"p"`
    Trunk      string        `help:"Override trunk() revset" short:"t"`
    StaleAfter time.Duration `help:"Age threshold for the 'stale, no remote' heuristic"`

    Preview struct{} `cmd:"" default:"1" help:"Show what would be deleted (default)"`
    Apply   struct{} `cmd:"" help:"Delete merged bookmarks"`
    Review  struct{} `cmd:"" help:"Interactive walk over merged/probably-merged/stale bookmarks"`
}

type CommitsCmd struct {
    NoDescriptionOnly bool `help:"Restrict to description(\"\")"`

    Preview struct{} `cmd:"" default:"1" help:"Show what would be abandoned (default)"`
    Review  struct{} `cmd:"" help:"Interactive walk over anonymous fork candidates"`
}
```

`Repository` and `Fetch` are the only flags that stay global: both apply
regardless of which subcommand runs. `Protected`/`Trunk`/`StaleAfter` sit
on `BookmarksCmd` (not the top-level `CLI`) since `commits`'s candidate
revset never references `trunk()` and has no bookmark-glob or staleness
concept — a scoping fix this split surfaced, not just a mechanical flag
move. Kong flattens each ancestor struct's flags into every leaf
subcommand's own flag set, so e.g. `bookmarks apply --trunk=...` sees
`--trunk` despite it being declared on the parent `BookmarksCmd`, not on
the `Apply` leaf itself.

After `parser.Parse(args)`, `kong.Context.Command()` returns the full
space-separated path (e.g. `"bookmarks apply"`, `"commits preview"`).
Dispatch splits on the first space and switches on the group name, then
passes the leaf action string down to `runBookmarks`/`runCommits` — simple
enough that jj-trim doesn't need Kong's interface-based `Run(...)`
dispatch convention.

**Shell completion is not built into Kong.** The standard companion is
[`willabides/kongplete`](https://github.com/willabides/kongplete) (built
on `posener/complete`), which introspects Kong's own `Model()` to generate
bash/zsh/fish completion scripts — an additional dependency, not something
Kong provides for free. Not adopted yet; see Future ideas in DESIGN.md.

## Package layout

```text
main.go                  // entrypoint; maps errors to exit codes
internal/jj/              // Runner interface + real exec.CommandContext implementation
internal/classify/        // pure: revset construction, -T template output parsing
internal/review/          // shared interactive review TUI (bookmarks + commits)
internal/preview/         // builds & streams the annotated `jj log` preview
```

### `main.go`

Exit codes, simplified from hrd's three-way scheme since jj-trim has no
"ran across many repos, some failed" concept:

- `0` — success (including "nothing to trim").
- `1` — a `jj` invocation failed or another runtime error occurred.
- `2` — usage error (bad flags).

### `internal/jj/` — the subprocess seam

```go
type Runner interface {
    Run(ctx context.Context, args ...string) (stdout string, err error)
    Stream(ctx context.Context, w io.Writer, args ...string) error
}
```

One real implementation backed by `exec.CommandContext`, treating a
non-zero exit as an `error` (see "non-zero exit" row below for why this
diverges from hrd). Typed, narrow methods sit on top of `Runner` rather
than exposing "run anything":

```go
func Log(ctx context.Context, r Runner, revset, template string) (string, error)
func LogPreview(ctx context.Context, r Runner, w io.Writer, revset string, color bool) error
func Show(ctx context.Context, r Runner, changeID string) (string, error)
func BookmarkDelete(ctx context.Context, r Runner, names []string) error
func Abandon(ctx context.Context, r Runner, changeIDs []string) error
func GitFetch(ctx context.Context, r Runner) error
```

`Run` captures stdout into a string (used by `Log`/`Show`/the JSONL query
path below) for cases where the caller needs to parse the result. `Stream`
exists separately for `LogPreview`: it pipes jj's stdout straight to a
caller-supplied `io.Writer` rather than buffering and returning a string,
since the preview's whole point is showing jj's own rendered graph output
unmodified, not parsing it. `LogPreview` always appends `--no-pager` (see
`internal/preview`, below, for why) and an explicit `--color=always` or
`--color=never` based on the `color bool` the caller computed — `internal/jj`
itself never inspects a terminal; that decision is made once, where the
real `os.Stdout` is in scope (see `internal/preview`), and passed down.

A fake `Runner` (map of expected args → canned stdout/error, plus a
canned-bytes write for `Stream`) backs unit tests for everything built on
top of it, without a real `jj` binary or subprocess involved.

### `internal/classify/` — pure logic, highest test-value package

Revset construction (Part 1's `merged` query, Part 2's
`heads(mutable()) ~ bookmarks() ~ @` plus the no-description/age/
already-merged sub-filters from DESIGN.md) and parsing of jj's output into
Go structs (`ChangeID`, `Description`, `CommitDate`, ...). No `Runner`, no
subprocess, anywhere in this package — string and struct manipulation
only, which makes it the cheapest package to test exhaustively and the one
place a coverage target near 100% is reasonable to ask for.

**Output format: JSONL via jj's own `json()` template function, not a
hand-rolled delimiter scheme.** jj's templating language ships exactly
this machine-readable-output mechanism — `json(value)` and the
`.escape_json()` string method (`jj help -k templates`), with a documented
alias pattern (`'json:x' = 'json(x) ++ "\n"'`) for emitting one JSON object
per line. `internal/jj.Log` (and friends) should pass a `-T` template built
around `json(self)` (or an explicit object of the specific fields needed —
change ID, description, commit date, bookmark names) and parse the result
line-by-line with `encoding/json`. This avoids inventing a delimiter
unlikely to collide with arbitrary commit-message content — jj already
solved that escaping problem and exposes it for exactly this use case;
there's no reason to re-solve it.

### `internal/review/` — shared interactive review TUI

**Revised from an earlier plain-stdin-prompt design** (see DESIGN.md's
"Why this isn't a plain prompt loop, after all") once the UX grew
non-linear navigation and a live tally — neither fits a sequential
`read line, act, discard` loop. Built on
[Bubbletea](https://github.com/charmbracelet/bubbletea) (`charm.land/bubbletea/v2`

- `charm.land/bubbles/v2` + `charm.land/lipgloss/v2` — the same versions
  `../hrd`'s own `internal/tui/` already pins), and used identically by both
  `bookmarks review` and `commits review`:

```go
// Item is one thing the review flow can act on.
type Item struct {
    IDs        []string           // passed to Action.Apply if marked with Action's decision
    CascadeIDs []string           // passed to CascadeAction.Apply if marked with the cascade decision
    Candidate  classify.Candidate // backs ContextFetcher
    Legend     classify.LegendEntry
}

// Action is a batch operation applied to every item marked with a given
// decision when the user confirms.
type Action struct {
    Verb string // "delete" / "abandon" — also derives this action's mark key (first letter, lowercased)
    Past string // "deleted" / "abandoned"
    Apply func(ctx context.Context, r jj.Runner, ids []string) error
    CascadeAction *Action // second per-item choice, offered via a distinct mark key; nil = only one choice
}

type ContextFetcher func(ctx context.Context, r jj.Runner, c classify.Candidate) (string, error)

// Result.OpIDs has one entry per batch that actually ran — up to two for
// a bookmarks-review session with both delete- and abandon-marked items.
func Run(ctx context.Context, r jj.Runner, items []Item, action Action,
    fetch ContextFetcher, in io.Reader, out io.Writer) (Result, error)
```

`run.go` builds `[]Item`/`Action`/`ContextFetcher` per command. `bookmarks
review`'s `Action` is `jj.BookmarkDelete` (ref-only, `Verb: "delete"`) with
a `CascadeAction` of `jj.Abandon` (`Verb: "abandon"`) whose `CascadeIDs`
is a single revset expression — `classify.PrivateChainRevset(changeID,
classify.KeepRevset(...))` — covering the bookmark's own commit plus every
ancestor not needed elsewhere (trunk, any other bookmark, `@`); since
`::x` includes `x` itself and jj already cascades bookmark deletion when
the commit it points at is abandoned, marking the cascade choice removes
the ref as a side effect with no separate `BookmarkDelete` call.
`commits review`'s `Action` is `jj.Abandon` with no `CascadeAction` (only
one choice), and its `Item.IDs` is _also_ a `PrivateChainRevset` expression
rather than a literal change id — marking a fork head now abandons its
whole private chain in one batch, not just the head (an earlier version
abandoned only the marked change id, which meant a 5-commit private stack
needed 5 separate `commits review` passes to fully clean). See DESIGN.md's
Part 2 Action section and `internal/classify.KeepRevset`/`PrivateChainRevset`.

**Every item starts unmarked**, including the certain `merged` bucket —
`review` always requires an explicit per-item choice; `apply` is the
separate non-interactive subcommand for bulk-deleting `merged` without a
review step. An earlier draft pre-marked `merged` items by default, which
made the TUI's first screen look like something was already selected
before the user acted — see DESIGN.md's Visualization & review UX section.

**Bubbletea model** (`internal/review/model.go`): a `bubbles/list.Model`
with a custom delegate, paired with a `bubbles/viewport.Model` for the
selected item's lazily-fetched, cached detail text, plus a footer tally.
Decision is a 3-state enum (`decisionPending`/`decisionMarked`/
`decisionMarkedCascade`) rather than a plain bool, but the mark _keys_
aren't hardcoded to specific letters: `Action.markKey()` derives one from
`Verb`'s first letter lowercased (`review.Action.markKey`), so a row's
letter marker is always the key that produces it (uppercased `D`/`A`
replacing the old fixed `✓`/`·` scheme), and `commits review`'s single
action (no `CascadeAction`) only ever binds one key, unchanged in shape
from before this feature existed. `newModel` panics if `Action.Verb` and
`CascadeAction.Verb` ever share a first letter — a startup-time guard
against a silently-colliding binding, not a runtime possibility today
(`"delete"`/`"abandon"` differ) but cheap insurance for future actions.
`u` unmarks a row unconditionally from either state, in addition to (not
instead of) pressing the row's own mark key twice.

Two screens: `screenList` (navigate with ↑/↓/j/k via the list's own key
handling; `enter` advances to confirm — moved here from being a mark-key
alias, since `commits review`'s own action verb is "abandon" and would
otherwise collide with a fixed `a`-for-advance binding) and
`screenConfirm` (lists what's marked under each action; `enter` runs each
action's batch call — `Action.Apply` first if it has marked items, then
`CascadeAction.Apply` if it does, order irrelevant to correctness since
cascade abandon already subsumes any ref deletion of its own — and quits;
`esc` returns to the list without applying anything).

**One cancellation path, not two.** `q`/`Esc`/Ctrl-C all quit immediately
from any screen, applying nothing — simpler than the old design's
`q`-means-apply-so-far / Ctrl-C-means-apply-nothing split, since the batch
apply calls are now reachable only from the confirm screen's explicit
`enter` keypress. There's no partial-progress state left to disambiguate.

**Layout sizing bug found during this session, worth flagging for anyone
extending the model**: the list/detail panes must be sized from the
actual `tea.WindowSizeMsg` height, not fixed constants — an earlier draft
used a flat `listH := 10` regardless of item count or terminal height,
which silently overflowed a 30-row test terminal (two items still
reserved 10 list rows + 18 detail rows) and scrolled the header off the
top of the rendered frame. `handleWindowSize` now sizes the list to
`min(len(items), maxList)` and gives the remainder to the detail pane.

**Caller checks for a real TTY before launching the program at all** —
`golang.org/x/term.IsTerminal(os.Stdin.Fd())` (same package hrd already
depends on) — and returns a clear error immediately if stdin isn't a TTY,
since Bubbletea also needs a real terminal and would otherwise just hang
waiting for input that can never arrive.

### `internal/preview/` — graph preview

Builds the `jj log -r <revset>` invocation per DESIGN.md's "reuse jj log
itself" decision and calls `jj.LogPreview` to stream jj's own stdout
straight through — using jj's **default**, unmodified template.
Deliberately does **not** parse or re-render the graph, and deliberately
does **not** attempt to inject per-commit annotations via a custom `-T`
template either: jj's template language has no way to look up a
classification result `internal/classify` already computed in Go, so doing
that would mean re-deriving `merged`/`no description`/`anonymous fork`
membership a second time inside jj's templating language. Instead,
`internal/preview` prints the plain graph, then a short **legend**
underneath built directly from `internal/classify`'s structured candidate
data (change-id prefix → reason), entirely in Go, no template involved.

Also owns the color decision: checks `golang.org/x/term.IsTerminal(os.Stdout.Fd())`
once and passes the resulting bool down into `jj.LogPreview`, which turns
it into an explicit `--color=always`/`--color=never` flag (see
`internal/jj`, above) — and always requests `--no-pager`, since
`internal/preview`'s output is immediately followed by the legend and,
for Part 2, the `review` TUI; an engaged jj pager would interrupt
that handoff.

## Testing strategy

### Unit tests — `internal/classify`, `internal/review`

Table-driven, `testify` assertions (kept from hrd — this is Go testing
hygiene, not something tied to hrd's specific problem). `internal/classify`
needs no fakes at all since it's pure.

`internal/review`'s Bubbletea model is tested with
[`teatest`](https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest/v2)
(`../hrd/internal/tui`'s own testing approach) against a fake `jj.Runner`:
construct the model via an exported `NewModelForTest` (production code
uses the unexported `newModel`/`Run` path), drive it with
`tm.Send(tea.KeyPressMsg{...})`, then assert on `fake.Calls` and the final
`Result` rather than on terminal output. **Mid-session `teatest.WaitFor`
substring checks are unreliable and were dropped after hitting exactly
this in this session**: Bubbletea's renderer transmits only the _diff_
between frames, so a single-character change (the tally's `"0"` becoming
`"1"`) never appears as a contiguous substring in the raw output stream —
only a full initial paint or a full screen transition (list ↔ confirm) is
safe to substring-match. Tests instead send a full key sequence and assert
on the outcome once `tm.WaitFinished` returns.

### Integration tests — `internal/jj`

Real temporary jj repos via `t.TempDir()` + `jj git init`, built with
small fixture-builder helpers in the style of hrd's `setupXXX` functions —
e.g. `repoWithMergedBookmark(t)`, `repoWithAnonymousFork(t, withDescription
bool)`, `repoWithProtectedBookmark(t)`, plus three added for the cascade
feature: `repoWithBookmarkAncestorOfTrunk(t)` (a bookmark that's a strict
ancestor of trunk, not equal to it — the case cascade must degrade to a
no-op for), `repoWithPrivateBookmark(t)` (a bookmark on two commits never
merged, to assert the private chain resolves correctly), and
`repoWithSharedForkBase(t)` (two anonymous fork heads sharing a private
base commit, to assert marking one doesn't abandon what the other still
needs) — each scripting a known, named graph shape via real `jj` commands
so the fixture itself can't drift from what `jj` actually produces.

**Runtime-skipped, not build-tag-gated**: `t.Skip()` if `jj` isn't found on
`PATH`, exactly like hrd's `initJJRepo` helper. This is simpler than
partitioning a separate `-tags=integration` suite, and CI always has `jj`
available (mise installs it per DESIGN.md's Tooling & CI section), so the
skip path never actually triggers there — these tests run as a normal part
of `go test ./...`. (DESIGN.md's Tooling & CI section has been corrected to
match this.)

### Golden / end-to-end tests: `testscript`, not a hand-rolled `-update` flag

Use [`rogpeppe/go-internal/testscript`](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript)
rather than hand-rolling golden-file comparison. It's the package extracted
from the Go toolchain's own test infrastructure (`cmd/go`'s tests),
also used by Cobra and widely elsewhere — exactly built for "script a
sequence of real subprocess invocations, diff the output," which is this
tier exactly. A script under `testdata/script/` reads like:

```text
exec jj git init
exec jj describe -m 'first change'
exec jj bookmark create feature -r @
exec jj new main
exec jj-trim commits preview
cmp stdout golden-preview.txt
```

This gets golden-file comparison (`cmp`), scripted multi-step setup, and
exit-code/stdout/stderr assertions for free, replacing both the bespoke
`-update`-flag golden-diffing code this section originally proposed
hand-rolling, and potentially the Go-coded fixture-builder helpers for
simple end-to-end scenarios — though `internal/jj`'s own integration tests
(above) stay Go-coded where the assertion is about the `Runner` call
sequence itself, not just final stdout, which `testscript` doesn't reach
into.

### Coverage targets

Mirrors hrd's `.testcoverage.yml` component-override mechanism, remapped
to jj-trim's actual package split rather than copying hrd's numbers
(90% total / 100% on `backends/`). HH's call: **95% for critical packages,
80% for the whole program**, concretely:

```yaml
# .testcoverage.yml
profile: cover.out

threshold:
  total: 80

override:
  - path: internal/classify/
    threshold: 95

exclude:
  paths:
    - main\.go$ # entrypoint/exit-code mapping only, validated by golden tests
```

`internal/classify` is the "critical package" the 95% applies to — it's
the pure, cheap-to-test-exhaustively logic from `internal/classify/`,
above. `internal/review` and `internal/jj` get no override and fall under
the 80% total: `internal/review`'s state-machine logic is covered by its
own unit tests against a fake `jj.Runner` (see Unit tests, below), and
`internal/jj` is covered by its integration tests against real temporary
jj repos (see Integration tests, below) rather than a separate unit-test
push — both count toward the same `cover.out` profile regardless of
which test tier produced them. `main.go` is excluded outright, matching
hrd's own `.testcoverage.yml` (`exclude.paths: - main\.go$`) — it's just
exit-code mapping, validated by the golden/e2e `testscript` tier instead
of unit branch coverage.

## Explicit non-adoptions (from hrd)

- **`errgroup`-based parallel multi-repo runner** — jj-trim operates on one
  repo at a time; there's no fan-out concept in the design to parallelize.
- **Backend plugin registry** (git/jj abstraction) — jj-trim only ever
  targets jj; already noted in DESIGN.md's Landscape section.
- **`--` separator pre-parsing** for pass-through subcommand args —
  jj-trim doesn't expose "run an arbitrary jj command through me"; every
  jj invocation is internally constructed by `internal/jj`'s typed methods.
- **Bubbletea TUI — reversed, see `internal/review/` above.** This was
  originally on the non-adoption list (reasoning: hrd's TUI is a
  full-screen multi-pane dashboard justified by real multi-repo
  complexity, while jj-trim's review was "a linear one-candidate-at-a-time
  prompt" not worth a TUI framework for). That framing stopped holding once
  the review UX grew back-navigation and a live tally, which a plain
  stdin loop can't do cleanly — see DESIGN.md's "Why this isn't a plain
  prompt loop, after all" for the full reasoning. jj-trim now depends on
  the same `charm.land/bubbletea`/`bubbles`/`lipgloss` versions hrd pins,
  but not hrd's own `internal/tui/` code (command bar, mouse, persistent
  state, filtering) — jj-trim's TUI is a narrower list + detail + tally +
  confirm screen.
- **TOML config (`go-toml/v2`)** — DESIGN.md already defers config-file
  support (persisted `--protected` globs, review exclude list) to Future
  ideas; no reason to take the dependency before there's a concrete config
  shape to parse.
- **Non-zero exit as data, not error** (hrd's `RunResult{ExitCode}`
  pattern) — fits hrd's "run this across N repos, report which failed"
  model, where failure is a result to display. jj-trim invokes `jj` for
  its own internal correctness (`jj bookmark delete`, `jj abandon`); a
  non-zero exit there is a real failure to surface and abort on, not a
  status line to report alongside successes.
