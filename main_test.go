package main

import (
	"reflect"
	"testing"
)

// TestSplitArgs is the regression for a dangerous bug: Go's flag package stops
// parsing at the first non-flag argument, so `ocsa autovacuum full --yes` used
// to ignore --yes and, worse, ignore --data-dir.
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
			flags, pos := splitArgs(tc.args, valueFlags)
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
