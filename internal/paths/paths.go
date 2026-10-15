// Package paths resolves the locations of a local OpenCode installation.
package paths

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Paths holds every location OCSA needs to look at.
type Paths struct {
	Home       string
	Data       string
	DB         string
	Log        string
	LogFile    string
	Snapshot   string
	ToolOutput string
	Shell      string
	Repos      string
	Cache      string
	State      string
}

// Resolve asks the opencode CLI where it keeps things, falling back to the
// XDG defaults when the binary is unavailable.
func Resolve() Paths {
	p := Paths{}
	if kv := debugPaths(); kv != nil {
		p.Home = kv["home"]
		p.Data = kv["data"]
		p.DB = kv["db"]
		p.Log = kv["log"]
		p.Repos = kv["repos"]
		p.Cache = kv["cache"]
		p.State = kv["state"]
	}
	if p.Data == "" {
		p.Data = filepath.Join(dataHome(), "opencode")
	}
	if p.DB == "" {
		p.DB = filepath.Join(p.Data, "opencode.db")
	}
	if p.Log == "" {
		p.Log = filepath.Join(p.Data, "log")
	}
	if p.Repos == "" {
		p.Repos = filepath.Join(p.Data, "repos")
	}
	p.LogFile = filepath.Join(p.Log, "opencode.log")
	p.Snapshot = filepath.Join(p.Data, "snapshot")
	p.ToolOutput = filepath.Join(p.Data, "tool-output")
	p.Shell = filepath.Join(p.Data, "shell")
	return p
}

// WithDataDir rebases every path onto a different data directory, for testing
// against fixtures or a copy of an install.
func (p Paths) WithDataDir(dir string) Paths {
	p.Data = dir
	p.DB = filepath.Join(dir, "opencode.db")
	p.Log = filepath.Join(dir, "log")
	p.LogFile = filepath.Join(p.Log, "opencode.log")
	p.Snapshot = filepath.Join(dir, "snapshot")
	p.ToolOutput = filepath.Join(dir, "tool-output")
	p.Shell = filepath.Join(dir, "shell")
	p.Repos = filepath.Join(dir, "repos")
	return p
}

func debugPaths() map[string]string {
	out, err := exec.Command("opencode", "debug", "paths").Output()
	if err != nil {
		return nil
	}
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			kv[fields[0]] = fields[1]
		}
	}
	if len(kv) == 0 {
		return nil
	}
	return kv
}

func dataHome() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(h, ".local", "share")
}

// ByteSize renders a byte count the way the UI and reports show it.
func ByteSize(n int64) string {
	if n < 0 {
		return "-" + ByteSize(-n)
	}
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return trim(v) + " " + units[i]
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func trim(v float64) string {
	s := itoa(int64(v*10 + 0.5)) // one decimal place, integer math
	if len(s) == 1 {
		return "0." + s
	}
	return s[:len(s)-1] + "." + s[len(s)-1:]
}
