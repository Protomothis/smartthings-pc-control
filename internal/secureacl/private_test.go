package secureacl

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// checkPrivate asserts path has exactly the private DACL plus the test
// account, protected, and that its content is data.
func checkPrivate(t *testing.T, path, me, data string) {
	t.Helper()
	aces, protected := readACEs(t, path)
	if !protected {
		t.Errorf("%s: DACL is not protected", path)
	}
	want := []ace{
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0, 0x1f01ff, "S-1-5-18"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0, 0x1f01ff, "S-1-5-32-544"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0, 0x20000, "S-1-3-4"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0, 0x1f01ff, me},
	}
	if !sameACESet(aces, want) {
		t.Errorf("%s: ACEs %+v, want %+v", path, aces, want)
	}
	for _, a := range aces {
		switch a.sid {
		case "S-1-5-32-545", "S-1-5-11", "S-1-1-0", "S-1-5-4":
			t.Errorf("%s: still grants %s %#x", path, a.sid, a.mask)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != data {
		t.Errorf("%s: content %q, %v; want %q", path, got, err, data)
	}
}

func sameACESet(a, b []ace) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range b {
		found := false
		for _, y := range a {
			found = found || x == y
		}
		if !found {
			return false
		}
	}
	return true
}

func TestWritePrivateFile(t *testing.T) {
	me := mySID(t)
	extra := "(A;;FA;;;" + me + ")"
	dir := t.TempDir()

	// A new file is created private.
	fresh := filepath.Join(dir, "config.json")
	if err := writePrivateFile(fresh, []byte(`{"secret":"s1"}`), extra); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, fresh, me, `{"secret":"s1"}`)

	// Rewriting keeps it private and replaces the content.
	if err := writePrivateFile(fresh, []byte(`{}`), extra); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, fresh, me, `{}`)

	// A file that inherited "Users: read" (an install before #131) is
	// locked before the new content lands in it.
	old := filepath.Join(dir, "state.json")
	mustWrite(t, old)
	setDACL(t, old, "D:P(A;;0x1200a9;;;BU)(A;;FA;;;"+me+")")
	if err := writePrivateFile(old, []byte("new"), extra); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, old, me, "new")
}

// A crash record (#133) is private from its first byte, and never
// replaces a file that is already there.
func TestCreatePrivateFile(t *testing.T) {
	me := mySID(t)
	extra := "(A;;FA;;;" + me + ")"
	path := filepath.Join(t.TempDir(), "service-20261004-101500-8.txt")
	f, err := createPrivateFile(path, extra)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("header"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	checkPrivate(t, path, me, "header")
	if _, err := createPrivateFile(path, extra); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second create: %v, want fs.ErrExist", err)
	}
}

func TestLockFile(t *testing.T) {
	me := mySID(t)
	extra := "(A;;FA;;;" + me + ")"
	path := filepath.Join(t.TempDir(), "config.json")
	mustWrite(t, path)

	changed, err := lockFile(path, extra)
	if err != nil || !changed {
		t.Fatalf("first lock: changed=%v err=%v", changed, err)
	}
	checkPrivate(t, path, me, "x")
	if changed, err := lockFile(path, extra); err != nil || changed {
		t.Errorf("second lock: changed=%v err=%v", changed, err)
	}

	if _, err := LockFile(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v, want fs.ErrNotExist", err)
	}
	if _, err := LockFile(t.TempDir()); err == nil {
		t.Error("a folder was locked like a file")
	}
}

func TestLockFileRefusesSymlink(t *testing.T) {
	me := mySID(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	mustWrite(t, target)
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks need developer mode or elevation: %v", err)
	}
	before, _ := readACEs(t, target)
	if _, err := lockFile(link, "(A;;FA;;;"+me+")"); err == nil {
		t.Error("a symlink was locked")
	}
	// Writing through it still works like os.WriteFile, and says the file
	// could not be made private.
	err := writePrivateFile(link, []byte("y"), "(A;;FA;;;"+me+")")
	if !errors.Is(err, ErrNotPrivate) {
		t.Errorf("write through a symlink: %v, want ErrNotPrivate", err)
	}
	if after, _ := readACEs(t, target); !equalACEs(before, after) {
		t.Errorf("symlink target DACL changed: %+v -> %+v", before, after)
	}
}

// TestApplyKeepsPrivateFiles: the folder lockdown at every service start
// must not reset config.json to inherit "Users: read" again, but still
// resets a file whose own DACL lets more in than the folder does.
func TestApplyKeepsPrivateFiles(t *testing.T) {
	me := mySID(t)
	root, files := installTree(t, me)
	extra := "(A;OICI;FA;;;" + me + ")"
	if _, err := apply(root, extra); err != nil {
		t.Fatal(err)
	}
	config := files[1]
	if err := writePrivateFile(config, []byte("secret"), "(A;;FA;;;"+me+")"); err != nil {
		t.Fatal(err)
	}
	changed, err := apply(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("apply reset the private file")
	}
	checkPrivate(t, config, me, "secret")

	cases := []struct {
		name string
		sddl string
	}{
		{"Users modify", "D:P(A;;0x1301bf;;;BU)(A;;FA;;;" + me + ")"},
		{"owner rights widened", "D:P(A;;FA;;;SY)(A;;FA;;;OW)(A;;FA;;;" + me + ")"},
		{"deny SYSTEM", "D:P(D;;FA;;;SY)(A;;FA;;;" + me + ")"},
	}
	for _, c := range cases {
		setDACL(t, config, c.sddl)
		changed, err := apply(root, extra)
		if err != nil || !changed {
			t.Errorf("%s: apply changed=%v err=%v, want a reset", c.name, changed, err)
		}
		if _, protected := readACEs(t, config); protected {
			t.Errorf("%s: file still has a DACL of its own", c.name)
		}
	}
}
