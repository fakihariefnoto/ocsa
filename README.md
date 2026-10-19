# ocsa

**See where OpenCode's disk space went, and take it back.**

OpenCode keeps everything in one SQLite database. That database only ever grows:
SQLite is told never to shrink the file, so pages freed by old conversations sit
there forever. On one Mac this added up to a **10.75 GB file holding 562 MB of
real data** — and 379 MB of that "real data" turned out to be a copy of itself.

`ocsa` shows you the whole picture in a terminal dashboard and cleans it up
without stopping OpenCode.

```
DATABASE

  ████████████████████▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▒░  565.4 MB on disk
  ██ live          178.3 MB   31.5%  the generation OpenCode writes to
  ▓▓ frozen        379.5 MB   67.1%  abandoned tables, last written 2026-09-22
  ▒▒ other           7.6 MB    1.3%  indexes, key/value state, schema
  ░░ reclaimable     8.0 KB    0.0%  free pages a vacuum would return
  3.2× the live data  ·  10.5% of the 5.3 GB free on disk

RECLAIMABLE  125.2 MB
  snapshots, newest 5 kept                                        124.8 MB
  log, 1 MB kept                                                   40.9 MB
```

A reclaimable figure near zero is the *good* state, not a bug: SQLite reuses
freed pages before growing the file, so on a database that is being used
normally there is nothing sitting in the freelist to return. It only piles up
when whole pages are dropped faster than they are reused — session compaction,
or deleting sessions — which is exactly how the file that prompted this tool
reached 10 GB of free pages.

The bar is a composition, not a fill gauge: four things live in that file, and
only one of them is reclaimable. It is why a file can look nearly full and still
be mostly waste — the frozen block is real rows no vacuum can touch. The line
under it answers the question the bar raises: the file is **3.2× the size of the
data actually in use**.

## Install

```sh
brew tap fakihariefnoto/tap
brew install fakihariefnoto/tap/ocsa
```

Or, if you have Go:

```sh
go install github.com/fakihariefnoto/ocsa@latest
```

Or build it yourself:

```sh
git clone https://github.com/fakihariefnoto/ocsa
cd ocsa && make build
```

## Use

Run it with no arguments for the dashboard:

```sh
ocsa
```

Move with `↑`/`↓`, run the highlighted action with `enter`. Nothing writes to
your database until you confirm it, and every action reports what it verified
when it finishes.

The first entry is **Claim storage** — the safe sweep. It works out everything
worth doing, shows you the plan, and does nothing until you agree:

```
CLAIM STORAGE  nothing happens until you press y

  STEP                                               FREES
  + Rewrite the database                              6.1 MB
      drops 6.1 MB of free pages held inside the file
  + Drain the write-ahead log                         1.4 MB
      the rewrite inflates the log; without this half the space stays held
  + Trim the log                                     46.3 MB
      keeps the last 1.0 MB
  · Prune old tool output                                  -
      nothing older than 7 days
  · Prune old snapshots                                    -
      off by default: it would break revert for old sessions

  PROJECTED                                        54.0 MB
  frozen tables (needs row deletion)              379.5 MB

  [s] include snapshots? currently skipped: they hold revert data
  if OpenCode is mid-message, this waits for a gap
```

Skipped steps stay on screen with the reason, because a step that quietly does
not happen is the thing this screen exists to prevent. `s` opts into the one
destructive step (snapshots hold revert data) and the projections update to
match. Then `y` runs exactly that plan, and the result screen lists what each
step actually did — including any step that failed.

```
ACTIONS
  ▸  Claim storage                  the safe sweep: everything except snapshots  [C]
    Vacuum database                 rewrite in place, returns free pages  [v]
    Incremental vacuum              cheap, needs auto_vacuum=incremental  [i]
    auto_vacuum = incremental       makes cheap reclaiming possible  [a]
    auto_vacuum = full              zero maintenance, small cost on deletes  [f]
    Clean log and tool output       truncate the log, drop old spills  [c]
    Prune snapshots                 breaks revert for old sessions  [k]
    Browse projects                 storage by project and folder  [p]
    Browse sessions                 every session by size  [s]
    Write report                    reports/YYYY-MM-DD.md  [r]
```

### Where the space is

`p` lists projects by how much of the database they account for, and `enter`
opens one. `s` lists every session; press `g` to group them by folder with a
subtotal per folder.

```
  /Users/fakih/Projects/anofac/knalin   24.2 MB · 18 sessions
     20.1 MB  Copy plan to .plan and execute                 28d ago
    910.6 KB  Map inbox, channels, templates (@explore suba…  1mo ago
```

Sessions from folders you have since deleted are marked, which is usually the
first sign of clutter worth removing.

### What it does without the dashboard

```sh
ocsa status                # who holds the database, and is it safe to run now
ocsa scan                  # the summary, no TUI
ocsa claim                 # the safe sweep; prints the plan, dry run without --yes
ocsa sessions --group      # sessions by folder
ocsa projects              # storage per project
ocsa vacuum                # dry run; add --yes to run it
ocsa incremental           # cheap reclaim, no rewrite
ocsa autovacuum full       # tell SQLite to shrink itself from now on
ocsa report --out health.md
ocsa preview claim         # render a screen without a terminal
```

Exit codes: `0` ok, `2` nothing to do, `3` refused for safety, `4` verified
failure. `2` is normal — it means the database is already clean.

## The two things worth knowing

**VACUUM is what actually frees the space.** Deleting conversations does not
shrink the file. It marks pages free, and they stay in the file until something
rewrites it. `ocsa vacuum` does that rewrite in place — 10.75 GB → 562 MB took
about 20 seconds.

**The database holds two copies of your history.** A legacy set of tables
(`session`, `message`, `part`) and the current one (`session_v2`,
`session_message`) both contain the same conversations — same message IDs. On
the Mac that prompted this tool, the legacy copy stopped being written in
September and was still 379 MB, about two thirds of the file. That space is *not*
reclaimable by vacuum, because it is not free space: it is real rows nobody has
deleted. `ocsa` reports it separately and never folds it into the reclaimable
total, because deleting it is a decision about your data, not your disk. (See
[PLAN.md](PLAN.md) §8 for the open question of whether it is safe to delete.)

## Is it safe to run against a live OpenCode?

Yes, and it is built around that — including the case where OpenCode is open in
another terminal, mid-conversation.

- it never copies or moves files, so there is no window where a message can be
  written to a file that has been swapped out from under the running process
- it waits up to five minutes for the write lock rather than failing, and if it
  still cannot get it, SQLite rolls the entire operation back — nothing changed
- it checks `integrity_check` before, and `integrity_check`, `foreign_key_check`
  and per-table row counts after. A mismatch is reported as a failure.
- it drains the write-ahead log when it is done. Skipping that step is the
  classic way to reclaim half of what you expected: the log grows to the size of
  the database during a vacuum.
- an advisory lock in the data directory stops two `ocsa` processes from working
  on the same database at once, which would otherwise just look like a hang

`scan`, `sessions`, `projects` and `preview` only ever read.

### It never stops OpenCode

Worth stating plainly, because it is what people expect a tool like this to do:
`ocsa` does not kill, signal or restart anything. The only `os/exec` calls in
the source run `opencode debug paths` and `lsof`, both read-only. There is no
`SIGTERM` anywhere in the tree.

That is a design choice, not an omission. A rewrite does not need OpenCode to be
stopped — SQLite handles concurrent access, and the operation rolls back
completely if it cannot get the lock. Killing a terminal you are working in
would be a far worse outcome than waiting a second for a gap between messages.

If you *do* want it stopped, that is yours to do:

```sh
ocsa status                      # says who holds the database and whether it is busy
opencode service stop            # note: a running TUI may respawn the service
```

### When OpenCode is mid-message

Being open is fine — readers do not block a rewrite. Being *mid-write* is not,
and the process table cannot tell you which it is: a client sitting in a prompt
looks identical to one streaming a response.

So `claim` asks SQLite itself, by trying to take the write lock with a short
timeout and rolling straight back. If someone holds it, the claim waits for a
gap — OpenCode commits between turns, so one turns up in a second or two — and
only then works. If the write never stops, it gives up with exit code `3` and
changes nothing, rather than competing for the lock with a running agent:

```
$ ocsa claim --yes
OpenCode is writing continuously (opencode (pid 59073)); nothing was changed.
Retry, or pass --force to take the write lock anyway.
```

`--force` is there if you know better. It skips the wait, not the safety checks:
verification still happens and still fails loudly.

## Keeping it small

`auto_vacuum` is off by default, which is the root cause of the growth. Set it to
`incremental` and `ocsa incremental` can return free pages in milliseconds
whenever you want, without a rewrite. Set it to `full` and SQLite does it by
itself, at a small cost on deletes. Either way the file stops ballooning.

## Development

```sh
make check      # gofmt, go vet, go test
make run        # dashboard against your real install
make preview S=grouped
make snapshot   # build release artifacts locally, no publish
```

Tests build their own throwaway databases and never touch a real installation.
A few of them exist because of bugs this tool shipped with at some point: one
pins the double-counting of sessions across the two table generations, one
covers `flag`'s habit of ignoring flags written after a positional argument, and
one asserts that no action can write without a confirmation.

## Releasing

Builds, GitHub releases and the Homebrew formula are driven by GoReleaser. The
whole procedure, including the tap token and how to verify a release without
publishing one, is in [RELEASING.md](RELEASING.md).

```sh
make release V=0.1.0     # after ./scripts/release.sh --store-token once
```

## Licence

MIT

## What overlaps with what

`claim` is a superset, not a separate mechanism. It runs the same operations,
in a safe order, with one confirmation:

| Menu action | Inside `claim`? |
|---|---|
| Vacuum database | yes — it is step 1 |
| auto_vacuum = incremental | yes, when there is something worth rewriting |
| Clean log and tool output | yes |
| Prune snapshots | yes, but only if you opt in with `s` |
| auto_vacuum = full | **no** — `claim` picks incremental; full does more work per delete |
| Incremental vacuum | **no** — a rewrite makes this unnecessary |
| Browse projects / sessions, Write report | no — read only |

The individual actions stay because sometimes you want exactly one of them:
re-running `incremental` after a busy week, or setting `full` deliberately. Each
one says in its hint whether `claim` already covers it.
