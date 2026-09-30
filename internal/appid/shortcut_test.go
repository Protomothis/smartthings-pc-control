package appid

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fakeExe creates an empty file to point shortcuts at.
func fakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readBack(t *testing.T, path string) link {
	t.Helper()
	var l link
	if err := withCOM(func() (err error) { l, err = readLink(path); return }); err != nil {
		t.Fatalf("read back %s: %v", path, err)
	}
	return l
}

func TestEnsureShortcutCreatesAndReadsBack(t *testing.T) {
	root := t.TempDir()
	exe := fakeExe(t, root, "stpc.exe")
	dir := filepath.Join(root, "Start Menu", "Programs") // does not exist yet

	res, err := EnsureShortcut(dir, exe)
	if err != nil || res != Created {
		t.Fatalf("first Ensure = %q, %v; want created", res, err)
	}
	path := filepath.Join(dir, "SmartThings PC Control.lnk")
	l := readBack(t, path)
	if !samePath(l.target, exe) || l.args != "gui" || !samePath(l.workDir, root) || l.aumid != "Protomothis.SmartThingsPCControl" {
		t.Errorf("shortcut = %+v", l)
	}

	// A second call only reads: the file is left byte for byte as it was.
	before, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	res, err = EnsureShortcut(dir, exe)
	if err != nil || res != Unchanged {
		t.Fatalf("second Ensure = %q, %v; want unchanged", res, err)
	}
	after, _ := os.ReadFile(path)
	st2, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !st.ModTime().Equal(st2.ModTime()) {
		t.Error("an already correct shortcut was rewritten")
	}
}

func TestEnsureShortcutRepairs(t *testing.T) {
	root := t.TempDir()
	oldExe := fakeExe(t, root, "old.exe")
	sub := filepath.Join(root, "moved")
	os.Mkdir(sub, 0o755)
	newExe := fakeExe(t, sub, "stpc.exe")
	dir := filepath.Join(root, "Programs")
	path := filepath.Join(dir, ShortcutFile)

	if _, err := EnsureShortcut(dir, oldExe); err != nil {
		t.Fatal(err)
	}
	// The exe moved (self-update to another folder, reinstall elsewhere).
	if res, err := EnsureShortcut(dir, newExe); err != nil || res != Repaired {
		t.Fatalf("moved exe: %q, %v; want repaired", res, err)
	}
	if l := readBack(t, path); !samePath(l.target, newExe) || !samePath(l.workDir, sub) {
		t.Errorf("after move: %+v", l)
	}

	// Another AUMID on the shortcut (an older build, a hand-made link).
	if err := withCOM(func() error {
		return writeLink(path, link{target: newExe, args: "gui", workDir: sub, aumid: "SmartThings PC Control"})
	}); err != nil {
		t.Fatal(err)
	}
	if l := readBack(t, path); l.aumid != "SmartThings PC Control" {
		t.Fatalf("setup: aumid %q", l.aumid)
	}
	if res, err := EnsureShortcut(dir, newExe); err != nil || res != Repaired {
		t.Fatalf("wrong AUMID: %q, %v; want repaired", res, err)
	}
	if l := readBack(t, path); l.aumid != AUMID {
		t.Errorf("aumid after repair = %q", l.aumid)
	}

	// Other arguments.
	if err := withCOM(func() error {
		return writeLink(path, link{target: newExe, args: "gui --minimized", workDir: sub, aumid: AUMID})
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := EnsureShortcut(dir, newExe); err != nil || res != Repaired {
		t.Fatalf("other args: %q, %v; want repaired", res, err)
	}

	// Not a shortcut at all.
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := EnsureShortcut(dir, newExe); err != nil || res != Repaired {
		t.Fatalf("garbage file: %q, %v; want repaired", res, err)
	}
	if l := readBack(t, path); !l.matches(wantLink(newExe)) {
		t.Errorf("after garbage: %+v", l)
	}
	if res, _ := EnsureShortcut(dir, newExe); res != Unchanged {
		t.Errorf("after repair the next call = %q, want unchanged", res)
	}
}

func TestLinkWithoutAUMIDIsRepaired(t *testing.T) {
	// The shortcut a user makes with Explorer carries no AUMID.
	l := link{target: `C:\x\stpc.exe`, args: "gui", workDir: `C:\x`}
	if l.matches(wantLink(`C:\x\stpc.exe`)) {
		t.Error("a shortcut without AUMID counts as correct")
	}
	if !wantLink(`C:\X\STPC.EXE`).matches(wantLink(`c:\x\stpc.exe`)) {
		t.Error("paths should compare without case")
	}
}

func TestRemoveShortcut(t *testing.T) {
	root := t.TempDir()
	exe := fakeExe(t, root, "stpc.exe")
	if _, err := EnsureShortcut(root, exe); err != nil {
		t.Fatal(err)
	}
	if err := RemoveShortcut(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ShortcutFile)); !os.IsNotExist(err) {
		t.Errorf("shortcut still there: %v", err)
	}
	if err := RemoveShortcut(root); err != nil {
		t.Errorf("removing a missing shortcut: %v", err)
	}
}

func TestIdentity(t *testing.T) {
	// Changing either orphans every user's shortcut and toast history.
	if AUMID != "Protomothis.SmartThingsPCControl" || ShortcutFile != "SmartThings PC Control.lnk" {
		t.Errorf("AUMID %q, shortcut %q", AUMID, ShortcutFile)
	}
}

func TestEnsureShortcutNeedsExe(t *testing.T) {
	if _, err := EnsureShortcut(t.TempDir(), ""); err == nil {
		t.Error("an empty exe was accepted")
	}
}
