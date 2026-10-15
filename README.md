# OCSA — OpenCode Storage Analytics

Shows where a local OpenCode installation's disk goes, and reclaims it.

Built after finding a 10.75 GB database holding 562 MB of live data, of which
only 176 MB was still in use.

```sh
go build -o ocsa .
./ocsa              # dashboard
./ocsa scan         # same thing, non-interactive
```

## Why it is worth running

A real install, before and after a first pass:

| | Before | After |
|---|---|---|
| `opencode.db` | 10.75 GB | 562 MB |
| free pages | 10.16 GB | 0 B |
| disk free | 6.2 GB (99% full) | 16 GB (42%) |

The tool's most useful finding is not the free space though. The database
carries **two representations of the same conversations**:

```
GENERATIONS
  v1 (legacy)      379.5 MB  105873 rows  last write 2026-09-22  FROZEN
  v2 (current)     176.2 MB   24170 rows  last write 2026-10-15  live
```

The v1 tables stopped being written months ago but are still 68% of the file.
They are **not** free space — only deleting rows reclaims them, which is a
decision about data, not about disk. OCSA reports them separately and never
folds them into the reclaimable total.

## Commands

```
ocsa                     interactive dashboard (default)
ocsa scan                print the summary                [--json]
ocsa sessions            list by size                     [--sort bytes|updated|messages|cost|folder] [--group] [--limit N]
ocsa projects            storage per project and folder   [--limit N] [--json]
ocsa vacuum              rewrite the database in place    [--yes] [--force]
ocsa incremental         return free pages cheaply        (needs auto_vacuum=incremental)
ocsa autovacuum <mode>   set none|full|incremental        [--yes]
ocsa clean [snapshots]   truncate log, prune tool output  [--yes]
ocsa orphans             rows whose parent is gone        [--yes]
ocsa report --out FILE   write a markdown report
ocsa preview [screen]    print one screen: dash, projects, sessions, grouped

--data-dir DIR   use a different installation
--yes            apply changes; without it, mutating commands are dry runs
```

Exit codes: `0` ok, `1` usage, `2` nothing to do, `3` precondition failed,
`4` verification failed.

## In the dashboard

The dashboard is a menu: `↑`/`↓` move, `enter` runs the highlighted action.
Single-key shortcuts still work for anyone who knows them.

```
│ ACTIONS                                                                              │
│   ▸  Vacuum database               rewrite in place, returns free pages  [v]         │
│     Incremental vacuum             cheap, needs auto_vacuum=incremental  [i]         │
│     auto_vacuum = incremental      makes cheap reclaiming possible  [a]              │
│     auto_vacuum = full             zero maintenance, small cost on deletes  [f]      │
│     Clean log and tool output      truncate the log, drop old spills  [c]            │
│     Prune snapshots                breaks revert for old sessions  [k]               │
│     Browse projects                storage by project and folder  [p]                │
│     Browse sessions                every session by size  [s]                        │
│     Write report                   reports/YYYY-MM-DD.md  [r]                        │
│     Quit                             [q]                                             │
```

Mutating entries ask for confirmation first, and report what they verified
afterwards.

### Browsing storage by project and folder

`p` lists every project with its session count, payload bytes, folder and last
use. `enter` opens that project's sessions. `s` lists all sessions, `g` toggles
between largest-first and grouped-by-folder with per-folder subtotals:

```
│   /Users/fakih/Projects/anofac/knalin   24.2 MB · 18 sessions                        │
│      20.1 MB  Copy plan to .plan and execute                  28d ago                │
│     910.6 KB  Map inbox, channels, templates (@explore suba…  1mo ago                │
```

Sessions whose folder no longer exists are marked, in both views — that is how
stale sessions surface.

## Safe against a running OpenCode

`vacuum` is the interesting case. It rewrites the file in place, and does so
while OpenCode is running:

- it never copies or moves files, so there is no window where a message can be
  written to a swapped-out inode
- `busy_timeout` (5 min) makes it wait for the write lock rather than fail; if
  it still cannot get it, SQLite rolls the whole thing back and nothing changed
- it checks `integrity_check` before, and `integrity_check`, `foreign_key_check`
  and per-table row counts after; a mismatch is reported as a failure, not a
  success

## Two things that are easy to get wrong

**The WAL.** `VACUUM` leaves a write-ahead log as large as the database — 567 MB
in the case above. Without a `wal_checkpoint(TRUNCATE)` immediately after, you
reclaim roughly half of what you thought. It is part of `vacuum`, not an
afterthought.

**Root cause.** The default `auto_vacuum=0` means SQLite never shrinks the file,
so freed pages accumulate until someone runs a vacuum. Set `INCREMENTAL` to make
`ocsa incremental` able to drop them in milliseconds, or `FULL` to have SQLite
do it automatically on commit.

## Development

```sh
go test ./...
go vet ./...
```

Tests build their own fixture databases in `t.TempDir()` and never touch a real
install. Regression tests worth knowing about:

- `TestPerSessionBytesCountsOneGeneration` — v1 and v2 tables share message ids;
  summing both double counts every conversation
- `TestSplitArgs` / `TestValueFlagsMatchTheFlagSet` — Go's `flag` package stops at
  the first positional, which silently ignored `--data-dir`; the value-flag set
  is now read off the FlagSet so a new flag cannot desync it
- `TestMutatingActionsConfirmFirst` — no action writes without a confirmation,
  and cancelling returns where it left off
- `TestVacuumVerifiesRowCounts`, `TestFrozenGenerationIsNotCountedAsFreeSpace`
