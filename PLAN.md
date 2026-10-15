# OCSA — OpenCode Storage Analytics

A Go TUI that shows where OpenCode's disk usage goes and frees it.

Status: **plan**. Every number and behaviour below was measured on a real
install (`~/.local/share/opencode`, opencode v2.0.28, macOS) on 2026-10-15.

---

## 1. What we learned (this is the whole design)

The install held **10.75 GB** for **562 MB** of live data — 94.5% free pages.
Fixing it took three facts, and they invert most of the obvious design:

1. **In-place `VACUUM` is safe against a running OpenCode.**
   `sqlite3 opencode.db "PRAGMA busy_timeout=300000; VACUUM;"` completed in
   **19.6 s** while the server held the DB open, and the live session did not
   notice. 10.75 GB → 562 MB.
   → No restart. No file swap. No "quit everything first" dance.

2. **A file swap is wrong.** We built a careful `VACUUM INTO` + swap + rollback
   watcher first. It needed the server stopped, and it silently could never fire
   because it waited on process names — while five stale `opencode --auto` TUIs
   from 16 days earlier sat around forever. In-place vacuum deleted all of that
   complexity. **The tool must never move files; it rewrites in place.**

3. **`VACUUM` leaves a WAL as big as the database.** Post-vacuum:
   `opencode.db` 562 MB, `opencode.db-wal` **567 MB**. A
   `PRAGMA wal_checkpoint(TRUNCATE)` immediately after drops it to 0.
   Skip this and you reclaim half of nothing.

Two more, worth encoding in the tool rather than in a README:

4. **`auto_vacuum=0` is why it ballooned.** Setting `INCREMENTAL` and re-running
   `VACUUM` took 2.6 s and added only 179 pointer-map pages (+0.7 MB, 0.1%).
   `INCREMENTAL` needs `PRAGMA incremental_vacuum` called to actually drop pages;
   `FULL` does it on commit with a small per-delete cost. The tool should offer
   both, and explain the difference in one line rather than pretending one is
   strictly better.

5. **The real "is it in use?" signal is the open file handle, not the process
   list.** `lsof -nP opencode.db` — one PID, the server. LUIs hold nothing.

6. **The database holds two copies of the same conversations, and one is
   frozen.** Found while building the scanner: v1 `message` and v2
   `session_message` share message ids (3815 of 3822 on one session), so summing
   both tables double counts every conversation — the first version of the
   per-session estimate was exactly 2× too large. Worse, the split is not even:
   the v1 trio (`session`, `message`, `part`) was **last written 2026-09-22**
   and is 379.5 MB of the 562 MB file, while the live v2 tables are 176 MB.
   That 379.5 MB is not free space, so a vacuum cannot touch it — only deleting
   rows can, which is a data decision. The tool reports it as a separate
   "frozen generation" and keeps it out of the reclaimable total.

Measured baseline, for regression fixtures:

| | Value |
|---|---|
| `opencode.db` before / after | 10.75 GB → 562 MB |
| `freelist_count` before / after | 2,480,028 → 0 |
| `page_size` / `page_count` | 4096 / 2,625,302 → 144,012 |
| Live content | `message` 208 MB, `session_message` 170 MB, `part` 158 MB |
| Sessions / projects | 74 / 28 (54 in the v1 `session` mirror) |
| Rows | 20,381 messages, 85,438 parts, 23,964 session_messages, 149 todos |
| Other artifacts | `snapshot/` 196 MB, `log/opencode.log` 41 MB, `tool-output/` 472 KB |
| Dir total | 10.6 GB → 1.3 GB |
| Disk free | 6.2 GB (99%) → 16 GB (42%) |

---

## 2. The manual runbook

For anyone who just wants space back, no tool needed:

```bash
D=~/.local/share/opencode

# 1. reclaim (in place, live-safe)
sqlite3 "$D/opencode.db" "PRAGMA busy_timeout=300000; VACUUM;"

# 2. shrink the WAL that step 1 inflated
sqlite3 "$D/opencode.db" "PRAGMA wal_checkpoint(TRUNCATE);"

# 3. stop it recurring
sqlite3 "$D/opencode.db" "PRAGMA busy_timeout=300000; PRAGMA auto_vacuum=INCREMENTAL; VACUUM;"
sqlite3 "$D/opencode.db" "PRAGMA wal_checkpoint(TRUNCATE);"
```

That is the tool's core. Everything else is presentation and safety rails.

---

## 3. The tool

`ocsa` — one binary. Default invocation opens the TUI; everything is also
scriptable.

### 3.1 TUI

```
┌─ OCSA ─ OpenCode Storage ──────────────────────── ~/.local/share/opencode ─┐
│                                                                            │
│  DATABASE                                                                  │
│    ●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●●○○○○○○○○○  562 MB            │
│    live 562 MB   free 0 B   file 562 MB   WAL 0 B                          │
│    auto_vacuum INCREMENTAL        integrity ok                             │
│                                                                            │
│  RECLAIMABLE                                                               │
│    database        0 B     already clean                                   │
│    snapshot/     196 MB    22 worktree snapshots                           │
│    log            41 MB    single 41 MB file, never rotated               │
│    tool-output   472 KB    12 files                                        │
│    ─────────────────────   ─────────────────────────────────────────────── │
│    TOTAL         237 MB    press [c] to clean                              │
│                                                                            │
│  DISK                                                                      │
│    free 16 GB / 460 GB      was 6.2 GB before the last vacuum             │
│                                                                            │
│  [v] vacuum   [i] incremental   [a] auto_vacuum   [c] clean   [s] sessions │
│  [p] projects [r] report        [q] quit                                   │
└────────────────────────────────────────────────────────────────────────────┘
```

- Bars with `lipgloss` gradient fills; no color when `NO_COLOR` is set.
- Every number monospace-aligned in a right-hand column.
- Header always shows the resolved data dir — never guess which install you're
  looking at.
- `v` shows an inline confirmation with the projected size, then a live elapsed
  timer, because vacuum is 20 s and silence looks like a hang.

### 3.2 Screens

| Screen | Key | Content |
|---|---|---|
| Dashboard | — | the above |
| Sessions | `s` | `id`, `title`, `directory`, est. bytes, messages, cost, updated, dir-exists |
| Projects | `p` | project → dirs → sessions → est. bytes → last used |
| Clean | `c` | checkbox list: snapshots, log, tool-output, orphan rows |
| Report | `r` | markdown to stdout / `reports/YYYY-MM-DD.md` |

Session list is sortable (`S` to cycle: bytes, updated, messages, cost) with a
fuzzy filter (`/`). Est. bytes are payload sums, labelled as estimates —
`dbstat` is per-table, not per-session, so per-session can only ever be an
estimate. Say so in the UI rather than implying precision.

### 3.3 CLI

```
ocsa                    # TUI (default)
ocsa scan               # one-screen summary, non-interactive
ocsa scan --json        # machine-readable
ocsa vacuum [--yes]     # in-place + checkpoint + verify
ocsa autovacuum incremental|full [--yes]
ocsa clean [--snapshots|--log|--tool-output] [--yes]
ocsa sessions [--sort bytes] [--json]
ocsa delete <session-id>... [--yes]      # shells out to `opencode session delete`
ocsa report [--md|--json]
```

Mutating commands: **dry run unless `--yes`**, always printing the projected
before/after. Exit codes: `0` ok, `1` usage, `2` nothing to do, `3` precondition
failed, `4` verification failed after a mutation.

---

## 4. Freeing space — exact mechanics

### 4.1 `vacuum`

```
1. resolve data dir               (opencode debug paths, else XDG default)
2. if !--force and reclaimable < 5% of file  → exit 2 ("already clean")
3. record before: page_size, page_count, freelist_count, per-table row counts
4. wal_checkpoint(TRUNCATE)                     -- flush pending frames
5. integrity_check                              -- abort on anything but "ok"
6. busy_timeout = 300000; VACUUM                -- in place, live-safe
7. wal_checkpoint(TRUNCATE)                     -- mandatory, see §1.3
8. integrity_check + foreign_key_check          -- abort → restore nothing, report
9. compare row counts to step 3; any mismatch    → exit 4, loud warning
10. TRUNCATE/rotate log if --log
11. print: 10.75 GB → 562 MB, reclaimed 10.2 GB, took 19.6 s
```

Never `VACUUM INTO`, never move a file, never require OpenCode to be down. If
someone else holds a write lock, `busy_timeout` waits; if it still fails, SQLite
rolls back and nothing changed.

### 4.2 `clean`

| Target | Action | Risk |
|---|---|---|
| `snapshot/` | delete dirs whose worktree no longer exists, or `--keep-last N`, or `--older-than` | breaks revert for those sessions — confirm explicitly |
| `log/opencode.log` | truncate, or rotate to `.1` with `--keep-bytes` | none |
| `tool-output/` | delete spilled results older than N days | old tool calls re-read as missing |
| orphan rows | `message`/`part`/`session_message` whose parent is gone | none; audit found zero |

### 4.3 Never

- never touch `auth.json`
- never read `account`, `control_account`, `credential` values into a report
- never delete a directory the tool did not create
- never follow a symlink out of the data dir
- never run `VACUUM` while a *second* `ocsa` is also vacuuming (single-writer lock; SQLite handles it, but say why it's slow)

---

## 5. Go architecture

`github.com/charmbracelet/bubbletea` + `lipgloss` + `bubbles` (table, spinner,
progress). SQLite: `modernc.org/sqlite` — pure Go, no CGO, one static binary.

```
ocsa/
  PLAN.md
  README.md
  main.go
  internal/
    paths/       data-dir resolution, artifact inventory
    db/          open (read-write), pragmas, integrity/verify, dbstat, row counts
    storage/     scan, reclaim model, sessions/projects queries, orphan detection
    actions/     vacuum, autovacuum, clean, delete  (the only writers)
    report/      markdown + json emitters
    ui/          bubbletea model, views, keybinds, styles
```

Rules:

- `db` opens with `_pragma=busy_timeout(300000)` and `_pragma=journal_mode(WAL)`.
- Only `actions` writes. `storage` and `report` are read-only, so `scan` can run
  against a live install with zero risk.
- Every action returns a `Result{Before, After, Reclaimed, Elapsed, Verified}` so
  the UI and the JSON output show the same thing.
- Long actions run as `tea.Cmd` goroutines that emit progress messages; the
  invariant is the UI never blocks and always shows elapsed time.

Reclaim model:

```
db_file_bytes    = page_count * page_size
db_live_bytes    = db_file_bytes - freelist_count * page_size
db_reclaimable   = freelist_count * page_size
snapshot_bytes   = walk(snapshot/)
log_reclaimable  = size(log) - keep_bytes
orphan_bytes     = payload bytes of rows with a missing parent
total            = db_reclaimable + prunable_snapshots + log_reclaimable + orphan_bytes
```

Orphan checks (all zero on the audited install, must stay zero):

```sql
message.session_id        NOT IN (SELECT id FROM session)
part.session_id           NOT IN (SELECT id FROM session)
part.message_id           NOT IN (SELECT id FROM message)
session_message.session_id NOT IN (SELECT id FROM session_v2)
session_v2.project_id     NOT IN (SELECT id FROM project)
```

Per-session estimate (labelled as such in the UI):

```sql
SUM(LENGTH(data)) FROM session_message WHERE session_id = ?
  + SUM(LENGTH(data)) FROM part           WHERE session_id = ?
  + SUM(LENGTH(data)) FROM message        WHERE session_id = ?
```

Two generations of tables exist. Only **one is written at a time** — checked
empirically, see §1.6. Count the live one; the frozen one is real data and is
reported separately. v1 `session`/`message`/`part` and v2
`session_v2`/`session_message`. The fields that matter for this tool are
`session_v2.directory`, `.title`, `.cost`, `.model`, `.time_updated`,
`project.worktree`, and `project_directory.directory`. Read column lists from
`PRAGMA table_info` rather than assuming them, so a rename upstream degrades
instead of breaking.

---

## 6. Safety rails, ranked

| Risk | Guard |
|---|---|
| Corruption | `integrity_check` before and after; counts compared; SQLite rolls back a failed `VACUUM` anyway |
| Half-reclaimed (the WAL trap) | `wal_checkpoint(TRUNCATE)` is part of `vacuum`, not an afterthought |
| Deleting something wanted | dry run + `--yes`; snapshot dirs need an explicit flag |
| Secrets in reports | credential tables never queried for values |
| Wrong install | header always prints the resolved data dir |
| Silent no-op | `exit 2` when there is nothing worth reclaiming |

---

## 7. Build order

1. **paths + db + scan** — resolve dir, inventory artifacts, table sizes,
   freelist math, `scan --json`. Immediately useful, read-only.
2. **vacuum** — §4.1 exactly, CLI first. This is the whole product; ship it early.
3. **TUI dashboard** — dashboard + vacuum with confirmation and progress.
4. **sessions + projects screens** — queries, sorting, estimates.
5. **autovacuum** — `INCREMENTAL`/`FULL` with the one-line trade-off shown.
6. **clean** — log, tool-output, snapshots, orphans.
7. **report + delete** — markdown, and `opencode session delete` wrapper.

Tests: build fixture DBs with known free pages, orphans, and dead directories;
assert `Result` numbers. Never point a test at a real install. Each action gets
an idempotency check: running `vacuum` twice must report ~0 reclaimed the second
time.

---

## 8. Open questions

1. `INCREMENTAL` + a nag, or `FULL` and forget it? The tool can measure both and
   recommend from observed churn over time.
2. Auto-checkpoint after vacuum also when another process holds the DB and the
   checkpoint blocks — wait (current plan) or warn and move on?
3. Surface the 196 MB of snapshots as a first-class reclaim item, or leave it
   behind a flag because deleting it silently breaks revert?
4. Ship as a standalone binary, or as an `opencode` plugin exposing `ocsa` via
   the app's own command surface?
5. **Can the frozen v1 generation be deleted?** 379.5 MB, 68% of the database,
   not written since 2026-09-22, and every v1 session exists in v2. Deleting it
   looks like the single largest win left, but it needs proof that OpenCode
   never reads those tables — for revert, for old session display, or for a
   migration it might still run. Not something a storage tool should decide on
   its own.

---

## 9. Built

Phases 1–6 are implemented in this repository as of `0.1.0`, with one deviation
worth recording: the plan's §4.1 step order was written before implementation and
survived, but two things were added because building found bugs.

| Phase | Status |
|---|---|
| 1 paths + db + scan | done — `ocsa scan`, `--json` |
| 2 vacuum | done — in place, checkpointed, verified; `--yes`/`--force` |
| 3 TUI dashboard | done — dashboard, confirm, progress, result screens |
| 4 sessions + projects | done — `ocsa sessions --sort`, TUI session list |
| 5 autovacuum | done — `ocsa autovacuum none\|full\|incremental` |
| 6 clean | done — log, tool-output, snapshots, orphans |
| 7 report + delete | report done; `delete` deliberately not built |

Two additions the plan did not anticipate, both from bugs found while testing:

- **Generation detection.** The plan said both generations were live. They are
  not, and the per-session estimate was double counting because of it. Detection
  is now automatic (newest `time_created` wins) and the frozen generation is
  reported outside the reclaimable total.
- **Schema drift tolerance.** Column lists are read from `PRAGMA table_info`
  rather than assumed, because a fixture without a `model` column broke the
  scanner outright.

`delete` was left out on purpose. Deleting sessions is irreversible, it is a
curation decision rather than a storage one, and `opencode session delete`
already exists. Shipping a second way to lose history did not seem worth it.
