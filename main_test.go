package main

import (
	"flag"
	"reflect"
	"testing"
)

// TestValueFlagsMatchTheFlagSet is the regression this design exists for: a
// hand-maintained list of value flags drifted, so --height was silently treated
// as boolean and `ocsa preview grouped --height 26` failed to parse.
func TestValueFlagsMatchTheFlagSet(t *testing.T) {
	fs, _ := newFlagSet("preview")
	vf := valueFlagsOf(fs)

	for _, name := range []string{"data-dir", "limit", "sort", "out", "keep-bytes",
		"tool-output-days", "keep", "width", "height"} {
		if !vf[name] {
			t.Errorf("flag --%s takes a value but is not listed as such", name)
		}
	}
	for _, name := range []string{"yes", "force", "json", "group", "md"} {
		if vf[name] {
			t.Errorf("boolean flag --%s is listed as taking a value", name)
		}
	}

	// Every flag must be classified consistently with its own type: the map
	// lists value-taking flags, and boolean flags must stay out of it.
	fs.VisitAll(func(f *flag.Flag) {
		isBool := false
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			isBool = true
		}
		if isBool == vf[f.Name] {
			t.Errorf("flag --%s: takesValue=%v but isBool=%v", f.Name, vf[f.Name], isBool)
		}
	})

	// The end-to-end shape that used to break.
	flags, pos := splitArgs([]string{"grouped", "--height", "26"}, valueFlagsOf(fs))
	if len(pos) != 1 || pos[0] != "grouped" {
		t.Errorf("positionals = %v, want [grouped]", pos)
	}
	if len(flags) != 2 || flags[0] != "--height" || flags[1] != "26" {
		t.Errorf("flags = %v, want [--height 26]", flags)
	}
}

// TestSplitArgs is the regression for a dangerous bug: Go's flag package stops
// parsing at the first non-flag argument, so `ocsa autovacuum full --yes` used
// to ignore --yes and, worse, ignore --data-dir.
// testValueFlags mirrors the real set closely enough for splitArgs cases.
var testValueFlags = map[string]bool{
	"data-dir": true, "limit": true, "sort": true, "out": true,
	"keep-bytes": true, "tool-output-days": true, "keep": true,
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFlags []string
		wantPos   []string
	}{
		{
			name:      "flags after a positional",
			args:      []string{"incremental", "--yes", "--data-dir", "/tmp/x"},
			wantFlags: []string{"--yes", "--data-dir", "/tmp/x"},
			wantPos:   []string{"incremental"},
		},
		{
			name:      "flags before a positional",
			args:      []string{"--yes", "--data-dir", "/tmp/x", "full"},
			wantFlags: []string{"--yes", "--data-dir", "/tmp/x"},
			wantPos:   []string{"full"},
		},
		{
			name:      "self-contained values",
			args:      []string{"--data-dir=/tmp/x", "full"},
			wantFlags: []string{"--data-dir=/tmp/x"},
			wantPos:   []string{"full"},
		},
		{
			name:      "boolean flag followed by a positional",
			args:      []string{"--yes", "snapshots"},
			wantFlags: []string{"--yes"},
			wantPos:   []string{"snapshots"},
		},
		{
			name:      "no flags at all",
			args:      []string{"snapshots"},
			wantFlags: nil,
			wantPos:   []string{"snapshots"},
		},
		{
			name:      "negative-looking value stays with its flag",
			args:      []string{"--limit", "5", "--json"},
			wantFlags: []string{"--limit", "5", "--json"},
			wantPos:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, pos := splitArgs(tc.args, testValueFlags)
			if len(flags) == 0 {
				flags = nil
			}
			if len(pos) == 0 {
				pos = nil
			}
			if !reflect.DeepEqual(flags, tc.wantFlags) {
				t.Errorf("flags = %v, want %v", flags, tc.wantFlags)
			}
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
		})
	}
}

func TestRunUsageErrors(t *testing.T) {
	if code := run([]string{"nonsense-command"}); code != 1 {
		t.Errorf("unknown command exit code = %d, want 1", code)
	}
	if code := run([]string{"version"}); code != 0 {
		t.Errorf("version exit code = %d, want 0", code)
	}
}

// TestVersionAndHelpAreHandledBeforeFlagParsing: a package manager runs
// `ocsa --version` immediately after install, and a bare flag used to be parsed
// as a subcommand's flag and fail.
func TestVersionAndHelpAreHandledBeforeFlagParsing(t *testing.T) {
	for _, arg := range []string{"--version", "-v", "version"} {
		if code := run([]string{arg}); code != 0 {
			t.Errorf("%s exit code = %d, want 0", arg, code)
		}
	}
	for _, arg := range []string{"--help", "-h", "help"} {
		if code := run([]string{arg}); code != 0 {
			t.Errorf("%s exit code = %d, want 0", arg, code)
		}
	}
}

// TestVersionIsInjectable guards the ldflags contract goreleaser relies on: the
// version must be a variable, not a constant.
func TestVersionIsInjectable(t *testing.T) {
	old := version
	version = "1.2.3"
	defer func() { version = old }()
	if version != "1.2.3" {
		t.Error("version is not assignable, so -ldflags -X main.version= will not work")
	}
}
