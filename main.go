// Command ocsa reports on, and reclaims, the disk used by a local OpenCode
// installation.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// options holds every CLI flag, so the flag set and its consumption stay in
// one place and can be exercised by tests.
type options struct {
	dataDir     *string
	yes         *bool
	force       *bool
	asJSON      *bool
	limit       *int
	sortBy      *string
	group       *bool
	width       *int
	height      *int
	out         *string
	keepLog     *int64
	toolOutDays *int
	snapKeep    *int
}

func newFlagSet(cmd string) (*flag.FlagSet, *options) {
	fs := flag.NewFlagSet("ocsa "+cmd, flag.ContinueOnError)
	o := &options{}
	o.dataDir = fs.String("data-dir", "", "override the OpenCode data directory")
	o.yes = fs.Bool("yes", false, "apply changes (without this, mutating commands are dry runs)")
	o.force = fs.Bool("force", false, "ignore the reclaim threshold")
	o.asJSON = fs.Bool("json", false, "emit JSON")
	o.limit = fs.Int("limit", 20, "rows to show")
	o.sortBy = fs.String("sort", "bytes", "sort sessions by bytes|updated|messages|cost|folder")
	o.group = fs.Bool("group", false, "group sessions by folder")
	o.width = fs.Int("width", 112, "preview width")
	o.height = fs.Int("height", 44, "preview height")
	o.out = fs.String("out", "", "write the report to this file")
	o.keepLog = fs.Int64("keep-bytes", 1<<20, "log bytes to retain when cleaning")
	o.toolOutDays = fs.Int("tool-output-days", 7, "prune tool output older than N days")
	o.snapKeep = fs.Int("keep", storage.SnapshotKeepRecent(), "snapshots to keep")
	fs.Bool("md", false, "markdown report (the default)")
	return fs, o
}

func run(args []string) int {
	cmd := "ui"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}

	fs, o := newFlagSet(cmd)

	// Go's flag package stops at the first non-flag argument, which silently
	// drops flags written after a positional (`ocsa autovacuum full --yes`).
	// Split them out and let the flags come first. The set of flags that take a
	// value is read off the flag set itself, so adding a flag cannot desync it.
	flags, positionals := splitArgs(args, valueFlagsOf(fs))
	if err := fs.Parse(append(flags, positionals...)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	p := paths.Resolve()
	if *o.dataDir != "" {
		p = p.WithDataDir(*o.dataDir)
	}

	switch cmd {
	case "ui":
		return runUI(p)
	case "scan":
		return runScan(p, *o.asJSON)
	case "vacuum":
		res, err := actions.Vacuum(p, actions.VacuumOptions{
			Yes: *o.yes, Force: *o.force, TruncateLog: *o.keepLog == 0,
		})
		return emitResult(res, err, *o.asJSON)
	case "incremental", "incr":
		res, err := actions.Incremental(p)
		return emitResult(res, err, *o.asJSON)
	case "autovacuum", "auto":
		mode := "incremental"
		if fs.NArg() > 0 {
			mode = fs.Arg(0)
		}
		res, err := actions.AutoVacuum(p, mode, actions.VacuumOptions{Yes: *o.yes})
		return emitResult(res, err, *o.asJSON)
	case "clean":
		res, err := actions.Clean(p, actions.CleanOptions{
			Yes:          *o.yes,
			Log:          true,
			KeepLog:      *o.keepLog,
			ToolOutput:   true,
			ToolOutAge:   time.Duration(*o.toolOutDays) * 24 * time.Hour,
			Snapshots:    fs.NArg() > 0 && fs.Arg(0) == "snapshots",
			SnapshotKeep: *o.snapKeep,
		})
		return emitResult(res, err, *o.asJSON)
	case "orphans":
		res, err := actions.SweepOrphans(p, *o.yes)
		return emitResult(res, err, *o.asJSON)
	case "sessions":
		return runSessions(p, *o.asJSON, *o.sortBy, *o.limit, *o.group)
	case "projects":
		return runProjects(p, *o.asJSON, *o.limit)
	case "report":
		return runReport(p, *o.out)
	case "preview":
		which := "dash"
		if fs.NArg() > 0 {
			which = fs.Arg(0)
		}
		return runPreview(p, which, *o.width, *o.height)
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

// valueFlagsOf reports which flags consume the next argument, by asking the
// flag set rather than keeping a second list in sync by hand.
func valueFlagsOf(fs *flag.FlagSet) map[string]bool {
	out := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			return
		}
		out[f.Name] = true
	})
	return out
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

func runSessions(p paths.Paths, asJSON bool, by string, limit int, group bool) int {
	s := scanOrDie(p)
	sess := s.Sessions
	switch by {
	case "updated":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Updated.After(sess[j].Updated) })
	case "messages":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Messages > sess[j].Messages })
	case "cost":
		sort.Slice(sess, func(i, j int) bool { return sess[i].Cost > sess[j].Cost })
	case "folder":
		group = true
	default:
		sort.Slice(sess, func(i, j int) bool { return sess[i].Bytes > sess[j].Bytes })
	}
	if group {
		// Folder order, with a subtotal per folder: the readable way to see
		// where storage sits across projects.
		sort.Slice(sess, func(i, j int) bool {
			if sess[i].Directory != sess[j].Directory {
				return sess[i].Directory < sess[j].Directory
			}
			return sess[i].Bytes > sess[j].Bytes
		})
	} else {
		sort.Slice(sess, func(i, j int) bool { return sess[i].Bytes > sess[j].Bytes })
	}
	if limit > 0 && len(sess) > limit && !group {
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

	current := ""
	var folderBytes int64
	var folderCount int
	flush := func() {
		if current != "" {
			fmt.Printf("%s\n", folderLine(current, folderBytes, folderCount))
		}
	}
	for _, si := range sess {
		if group && si.Directory != current {
			flush()
			current = si.Directory
			folderBytes, folderCount = 0, 0
		}
		if group {
			folderBytes += si.Bytes
			folderCount++
		}
		fmt.Printf("%10s  %-62s %s%s\n", paths.ByteSize(si.Bytes),
			truncate(si.Title, 62), tail(si.Directory), goneMark(si.DirGone))
	}
	flush()
	return 0
}

// runPreview renders one screen and exits, so the layout can be inspected
// (and screenshotted) without a terminal.
func runPreview(p paths.Paths, which string, width, height int) int {
	s := scanOrDie(p)
	fmt.Print(ui.Preview(s, ui.PreviewOptions{Screen: which, Width: width, Height: height}))
	fmt.Println()
	return 0
}

// runProjects prints storage per project, largest first.
func runProjects(p paths.Paths, asJSON bool, limit int) int {
	s := scanOrDie(p)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s.Projects); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Printf("%10s  %5s  %-22s  %-44s  %s\n",
		"SIZE", "SESS", "PROJECT", "FOLDER", "LAST USED")
	n := 0
	for _, pr := range s.Projects {
		if limit > 0 && n >= limit {
			break
		}
		name := pr.Name
		if name == "" {
			name = filepath.Base(strings.TrimRight(pr.Worktree, "/"))
		}
		fmt.Printf("%10s  %5d  %-22s  %-44s  %s%s\n",
			paths.ByteSize(pr.Bytes), pr.Sessions, truncate(name, 22),
			tail(pr.Worktree), pr.LastUsed.Format("2006-01-02"), goneMark(pr.DirGone))
		n++
	}
	return 0
}

func goneMark(gone bool) string {
	if gone {
		return "  [folder gone]"
	}
	return ""
}

func folderLine(dir string, bytes int64, count int) string {
	return fmt.Sprintf("  %s  %s · %d sessions",
		dir, paths.ByteSize(bytes), count)
}

// tail keeps the end of a path, which is the identifying part.
func tail(p string) string {
	if len(p) <= 44 {
		return p
	}
	return "…" + p[len(p)-43:]
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
  sessions               list sessions by size            [--sort bytes|updated|messages|cost|folder] [--limit N] [--group] [--json]
  projects               storage usage per project and folder [--limit N] [--json]
  vacuum                 rewrite the database in place    [--yes] [--force]
  incremental            return free pages incrementally
  autovacuum <mode>      set none|full|incremental       [--yes]
  clean [snapshots]      truncate log, prune tool output, optionally snapshots [--yes]
  orphans                find rows whose parent is gone  [--yes]
  report --out FILE      write a markdown report
  preview [screen]       print one screen: dash, projects, sessions, grouped

flags:
  --data-dir DIR         use a different data directory
  --yes                  apply changes (otherwise dry run)
  --force                ignore the reclaim threshold
  --json                 machine-readable output

exit codes: 0 ok  1 usage  2 nothing to do  3 precondition failed  4 verification failed
`)
}
