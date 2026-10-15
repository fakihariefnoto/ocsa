// Command ocsa reports on, and reclaims, the disk used by a local OpenCode
// installation.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fakih/ocsa/internal/actions"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/report"
	"github.com/fakih/ocsa/internal/storage"
	"github.com/fakih/ocsa/internal/ui"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "ui"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("ocsa "+cmd, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "override the OpenCode data directory")
	yes := fs.Bool("yes", false, "apply changes (without this, mutating commands are dry runs)")
	force := fs.Bool("force", false, "ignore the reclaim threshold")
	asJSON := fs.Bool("json", false, "emit JSON")
	limit := fs.Int("limit", 20, "rows to show")
	sortBy := fs.String("sort", "bytes", "sort sessions by bytes|updated|messages|cost")
	out := fs.String("out", "", "write the report to this file")
	keepLog := fs.Int64("keep-bytes", 1<<20, "log bytes to retain when cleaning")
	toolOutDays := fs.Int("tool-output-days", 7, "prune tool output older than N days")
	snapKeep := fs.Int("keep", storage.SnapshotKeepRecent(), "snapshots to keep")
	_ = fs.Bool("md", false, "markdown report (the default)")

	// Go's flag package stops at the first non-flag argument, which silently
	// drops flags written after a positional (`ocsa autovacuum full --yes`).
	// Split them out and let the flags come first.
	flags, positionals := splitArgs(args, valueFlags)
	if err := fs.Parse(append(flags, positionals...)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	p := paths.Resolve()
	if *dataDir != "" {
		p = p.WithDataDir(*dataDir)
	}

	switch cmd {
	case "ui":
		return runUI(p)
	case "scan":
		return runScan(p, *asJSON)
	case "vacuum":
		res, err := actions.Vacuum(p, actions.VacuumOptions{
			Yes: *yes, Force: *force, TruncateLog: *keepLog == 0,
		})
		return emitResult(res, err, *asJSON)
	case "incremental", "incr":
		res, err := actions.Incremental(p)
		return emitResult(res, err, *asJSON)
	case "autovacuum", "auto":
		mode := "incremental"
		if fs.NArg() > 0 {
			mode = fs.Arg(0)
		}
		res, err := actions.AutoVacuum(p, mode, actions.VacuumOptions{Yes: *yes})
		return emitResult(res, err, *asJSON)
	case "clean":
		res, err := actions.Clean(p, actions.CleanOptions{
			Yes:          *yes,
			Log:          true,
			KeepLog:      *keepLog,
			ToolOutput:   true,
			ToolOutAge:   time.Duration(*toolOutDays) * 24 * time.Hour,
			Snapshots:    fs.NArg() > 0 && fs.Arg(0) == "snapshots",
			SnapshotKeep: *snapKeep,
		})
		return emitResult(res, err, *asJSON)
	case "orphans":
		res, err := actions.SweepOrphans(p, *yes)
		return emitResult(res, err, *asJSON)
	case "sessions":
		return runSessions(p, *asJSON, *sortBy, *limit)
	case "report":
		return runReport(p, *out)
	case "version", "--version":
		fmt.Println("ocsa " + version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		return 1
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

// valueFlags are the flags that consume the next argument, needed to split a
// command line into flags and positionals without a parser.
var valueFlags = map[string]bool{
	"data-dir": true, "limit": true, "sort": true, "out": true,
	"keep-bytes": true, "tool-output-days": true, "keep": true,
}

// splitArgs returns the flags and the positional arguments, in that order, so
// that flag.Parse sees every flag regardless of where the user wrote it.
func splitArgs(args []string, takesValue map[string]bool) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !isFlag(a) {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue // --flag=value is self-contained
		}
		if takesValue[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positionals
}

func runUI(p paths.Paths) int {
	prog := tea.NewProgram(ui.New(p), tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func scanOrDie(p paths.Paths) *storage.Scan {
	s, err := storage.Run(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan failed:", err)
		os.Exit(3)
	}
	return s
}

func runScan(p paths.Paths, asJSON bool) int {
	s := scanOrDie(p)
	if asJSON {
		if err := report.JSON(os.Stdout, s); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	report.Text(os.Stdout, s)
	return 0
}

func runSessions(p paths.Paths, asJSON bool, by string, limit int) int {
	s := scanOrDie(p)
	sess := s.Sessions
	switch by {
	case "updated":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Updated.After(sess[j].Updated) })
	case "messages":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Messages > sess[j].Messages })
	case "cost":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Cost > sess[j].Cost })
	default:
		sort.Slice(sess, func(i, j int) bool { return sess[i].Bytes > sess[j].Bytes })
	}
	if limit > 0 && len(sess) > limit {
		sess = sess[:limit]
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sess); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	for _, si := range sess {
		gone := ""
		if si.DirGone {
			gone = "  [directory gone]"
		}
		fmt.Printf("%10s  %-44s %s%s\n",
			paths.ByteSize(si.Bytes), truncate(si.Title, 44), si.Directory, gone)
	}
	return 0
}

func runReport(p paths.Paths, out string) int {
	s := scanOrDie(p)
	if err := os.WriteFile(out, []byte(report.Markdown(s)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("wrote", out)
	return 0
}

func emitResult(r actions.Result, err error, asJSON bool) int {
	if asJSON {
		_ = report.ResultJSON(os.Stdout, r)
	} else {
		report.ResultText(os.Stdout, r)
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, actions.ErrNothingToDo):
		return 2
	case errors.Is(err, actions.ErrPrecondition):
		return 3
	case errors.Is(err, actions.ErrVerification):
		return 4
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func usage() {
	fmt.Print(`ocsa - OpenCode storage analytics

usage: ocsa [command] [flags]

commands:
  ui                     interactive dashboard (default)
  scan                   print the storage summary        [--json]
  sessions               list sessions by size            [--sort bytes|updated|messages|cost] [--limit N] [--json]
  vacuum                 rewrite the database in place    [--yes] [--force]
  incremental            return free pages incrementally
  autovacuum <mode>      set none|full|incremental       [--yes]
  clean [snapshots]      truncate log, prune tool output, optionally snapshots [--yes]
  orphans                find rows whose parent is gone  [--yes]
  report --out FILE      write a markdown report

flags:
  --data-dir DIR         use a different data directory
  --yes                  apply changes (otherwise dry run)
  --force                ignore the reclaim threshold
  --json                 machine-readable output

exit codes: 0 ok  1 usage  2 nothing to do  3 precondition failed  4 verification failed
`)
}
