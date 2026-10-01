package systool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathIsAbsoluteUnderTheWindowsDirectory(t *testing.T) {
	dir := WindowsDir()
	if !filepath.IsAbs(dir) {
		t.Fatalf("WindowsDir = %q, not absolute", dir)
	}
	for _, tool := range []string{Cmd, Netsh, PowerShell, SC, Shutdown, Timeout, Wevtutil} {
		p := Path(tool)
		if !strings.EqualFold(p, filepath.Join(dir, "System32", tool)) {
			t.Errorf("Path(%q) = %q", tool, p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// A tool is never looked up on PATH: a folder put first on PATH must not
// change what runs.
func TestCommandIgnoresPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, c := range [][]string{
		Command(Netsh, "advfirewall").Args,
		CommandContext(context.Background(), SC, "query").Args,
	} {
		if !filepath.IsAbs(c[0]) {
			t.Errorf("argv[0] %q is not absolute", c[0])
		}
	}
	cmd := Command(Netsh)
	if cmd.Err != nil || !strings.EqualFold(cmd.Path, Path(Netsh)) {
		t.Errorf("Command(Netsh): path %q, err %v", cmd.Path, cmd.Err)
	}
}
