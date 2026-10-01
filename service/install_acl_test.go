package service

import (
	"strings"
	"testing"
)

func TestLockInstallDirMessages(t *testing.T) {
	if msg, warn := lockInstallDir(""); msg != "" || warn {
		t.Errorf("unknown dir: got %q, %v", msg, warn)
	}
	// A temp dir lives in a user profile: never tightened, and the log
	// says why instead of failing the start.
	msg, warn := lockInstallDir(t.TempDir())
	if !warn || !strings.Contains(msg, "left unchanged") || !strings.Contains(msg, "user-profile") {
		t.Errorf("profile dir: got %q, %v", msg, warn)
	}
}
