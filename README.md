# jj-trim

Clean up merged bookmarks and abandoned anonymous commits in a jj repository.

`jj-trim` is a CLI tool for keeping your jj (Jujutsu) commit graph tidy. It finds
bookmarks whose content is already in trunk, detects anonymous forks that were
never bookmarked, and presents everything in a preview graph or an interactive
TUI — with jj's own `jj op revert` as the safety net.

[![CI](https://github.com/hugoh/jj-trim/actions/workflows/ci.yml/badge.svg)](https://github.com/hugoh/jj-trim/actions/workflows/ci.yml)
[![codecov](https://codecov.io/github/hugoh/jj-trim/graph/badge.svg?token=91HAIC8SER)](https://codecov.io/github/hugoh/jj-trim)
[![Go Report Card](https://goreportcard.com/badge/github.com/hugoh/jj-trim)](https://goreportcard.com/report/github.com/hugoh/jj-trim)

## Features

- **Merged bookmark detection** — bookmarks whose commit is an ancestor of
  `trunk()`. Provably safe, the only bucket `bookmarks apply` touches without a
  review step.
- **Probably-merged (squash-merge) detection** — bookmarks whose commit
  description appears verbatim in a trunk commit, catching the GitHub-style
  squash-merge case the ancestor check alone misses. Never auto-deleted; shown
  in preview and review only.
- **Stale bookmark detection** — old local-only bookmarks past a configurable
  age threshold (default 90 days). Heuristic, never auto-deleted.
- **Anonymous fork discovery** — commits in `heads(mutable())` that aren't on
  any bookmark or the working copy. Sub-classified into git-bypass duplicates
  (content-identical to a kept commit, almost certainly orphaned working-copy
  artifacts), no-description forks, and described forks.
- **Preview graph using jj's own log** — the tool simply passes a revset to
  `jj log` and annotates it with a legend, so what you see is exactly what jj
  sees.
- **Interactive TUI** — Bubbletea-based list + detail pane + confirm flow.
  Toggle between bookmarks and commits mode, mark items for delete or abandon,
  see a live tally, and explicitly confirm before anything is written.
- **Safe by default** — bare `jj-trim` previews only. Nothing is deleted or
  abandoned without `bookmarks apply` or an explicit review session.
- **Composable with jj's own safe guards** — every batch operation prints the
  `jj op revert <op-id>` command to undo exactly that batch.

## Install

### Homebrew (macOS/Linux)

```sh
brew install hugoh/tap/jj-trim
```

### Linux (deb/rpm)

Download the `.deb` or `.rpm` from the [releases page](https://github.com/hugoh/jj-trim/releases) and install with your package manager:

```sh
# Debian/Ubuntu
sudo apt install ./jj-trim_*.deb

# RHEL/Fedora
sudo dnf install ./jj-trim_*.rpm
```

### mise

```sh
mise use -g github:hugoh/jj-trim
```

### Go install

```sh
go install github.com/hugoh/jj-trim@latest
```

### From source

```sh
git clone https://github.com/hugoh/jj-trim
cd jj-trim
go build -o jj-trim .
```

## Quick start

```sh
# Preview merged bookmarks (dry run — no changes)
jj-trim bookmarks preview

# Delete merged bookmarks (provably safe — runs without review)
jj-trim bookmarks apply

# Interactive review of all bookmark candidates (merged + probably-merged + stale)
jj-trim bookmarks review

# Preview anonymous commit forks
jj-trim commits preview

# Interactive review of fork candidates
jj-trim commits review

# Launch the TUI (default, same as bare `jj-trim`)
jj-trim

# Fetch first, then classify (so jj's own stray-bookmark cleanup applies)
jj-trim --fetch bookmarks preview

# Operate on a specific repository
jj-trim -R ~/other-project bookmarks preview
```

## Classification buckets

| Bucket                 | Part      | Certainty                                      | Auto-deletable          |
| ---------------------- | --------- | ---------------------------------------------- | ----------------------- |
| `merged`               | Bookmarks | Provable (ancestor of trunk)                   | Yes (`bookmarks apply`) |
| `probably-merged`      | Bookmarks | Heuristic (message match in trunk)             | No — review only        |
| `stale`                | Bookmarks | Heuristic (old, no remote)                     | No — review only        |
| `git-commit-duplicate` | Commits   | Provable (content-identical to a kept commit)  | No — review only        |
| `no-description`       | Commits   | Heuristic (empty description, high confidence) | No — review only        |
| `has-description`      | Commits   | Weak (named work — needs human look)           | No — review only        |

## Preview & legend

`jj-trim bookmarks preview` runs `jj log -r <revset>` with the default template
(full color, ASCII DAG), then prints a legend mapping each candidate's
change-id prefix to its reason and age:

```text
% jj-trim bookmarks preview
○  yqnoqprn hugoh@
│  (no description)
○  mqtwpolr hugoh@ main
│  ...
◆  rlvkwnso hugoh@ trunk
...

Legend:
  mqtwpolr  merged (2 weeks old)
  yqnoqprn  stale, 94 days old, no remote
```

The same annotated approach works for `commits preview`, with
age-and-diffstat on each legend line.

## Interactive TUI

`jj-trim` (bare, no subcommand) launches the full TUI — a navigable live list
of candidates with a detail pane, mode toggle, and in-session filters:

- **Tab** — toggle between bookmarks mode and commits mode
- **↑/↓** or **j/k** — navigate the candidate list
- **d** (bookmarks mode) — mark a bookmark for delete (ref only)
- **a** — mark for abandon (bookmarks: delete + abandon private chain;
  commits: abandon only)
- **u** — clear a marked item's decision
- **Enter** — open the confirm screen showing exactly what's marked
- **f** — open the filters overlay (trunk, protected globs, stale-after for
  bookmarks; no-description-only for commits)
- **q** / **Esc** / **Ctrl-C** — quit without applying anything

The confirm screen lists every item under its action type and requires a second
**Enter** to actually run the batch operations. After applying, a popup shows
the result along with one `jj op revert <op-id>` per batch that ran.

## Command reference

```text
jj-trim [-R|--repository <path>] [--fetch] [<command>]

  Bare `jj-trim` (no <command>) launches the interactive TUI.

  -R, --repository <path>   Repo to operate on (default: current directory)
      --fetch                Run jj git fetch first

Commands:
  bookmarks preview   Show what would be deleted (default)
  bookmarks apply     Delete merged (certain) bookmarks only
  bookmarks review    Interactive walk over merged/probably-merged/stale
                       bookmarks

    -p, --protected <glob,...>   Bookmarks never deleted (default: none)
    -t, --trunk <revset>         Override trunk() (default: trunk())
        --stale-after <duration>  Age threshold for the "stale, no remote"
                                  heuristic (default: 2160h / 90d)

  commits preview   Show what would be abandoned (default)
  commits review    Interactive walk over anonymous fork candidates

        --no-description-only   Restrict to description("")

  install-completions   Install shell completions (bash/zsh/fish)
```

## Undo

Every batch operation prints the exact command to revert it:

```text
% jj-trim bookmarks apply
Deleted 3 bookmark(s): [experiment/foo, feature/wip, debug/logging]
Undo with: jj op revert 50859f2dfaf3
```

`jj op revert` reverts a specific operation regardless of what has happened
since — unlike bare `jj undo`, which always targets the single most recent
operation.

## Related tools

- [`git-trim`](https://github.com/foriequal0/git-trim) — the inspiration for
  this tool's bookmark-cleanup half.
- [tommymorgan/jj-tools](https://github.com/tommymorgan/jj-tools) — narrow
  cleanup of auto-generated temporary bookmarks. Complements jj-trim rather
  than competing.
- [Jujutsu (jj) VCS](https://github.com/jj-vcs/jj) — jj's own `jj git fetch`
  already handles stray-bookmark cleanup (tracked remote deleted upstream),
  and `jj abandon` cascades bookmark deletion — jj-trim composes these
  primitives rather than reimplementing them.

## Contributing

Contributions are welcome! See the
[implementation design doc](IMPLEMENTATION.md).
