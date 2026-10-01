package secureacl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// mySID is the test account's SID; the tests grant it full control on top
// of the install DACL so t.TempDir can still be removed without elevation.
func mySID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func readACEs(t *testing.T, path string) (aces []ace, protected bool) {
	t.Helper()
	acl, protected, err := readDACL(path)
	if err != nil {
		t.Fatal(err)
	}
	aces, err = aceList(acl)
	if err != nil {
		t.Fatal(err)
	}
	return aces, protected
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writable(t *testing.T, path string) bool {
	t.Helper()
	w, err := NonAdminWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// installTree lays out what a real install folder holds, with the two ways
// a child can escape the folder's own DACL: an explicit ACE, and a
// protected DACL of its own.
func installTree(t *testing.T, me string) (root string, files []string) {
	root = t.TempDir()
	files = []string{
		filepath.Join(root, "smartthings-pc-control.exe"),
		filepath.Join(root, "config.json"),
		filepath.Join(root, "service.log"),
		filepath.Join(root, "presets", "show-desktop.ps1"),
	}
	for _, f := range files {
		mustWrite(t, f)
	}
	// Everyone may overwrite the exe; presets\ is a protected folder that
	// lets Users modify everything in it.
	setDACL(t, files[0], "D:P(A;;FA;;;WD)(A;;FA;;;"+me+")")
	setDACL(t, filepath.Join(root, "presets"), "D:P(A;OICI;0x1301bf;;;BU)(A;OICI;FA;;;"+me+")")
	return root, files
}

func checkLocked(t *testing.T, root string, files []string, extra []ace) {
	t.Helper()
	aces, protected := readACEs(t, root)
	if !protected {
		t.Error("root DACL is not protected")
	}
	want := []ace{
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0x3, 0x1f01ff, "S-1-5-18"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0x3, 0x1f01ff, "S-1-5-32-544"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0x3, 0x1200a9, "S-1-5-32-545"},
		{windows.ACCESS_ALLOWED_ACE_TYPE, 0x3, 0x1200a9, "S-1-3-4"},
	}
	want = append(want, extra...)
	if len(aces) != len(want) {
		t.Errorf("root has %d ACEs, want %d: %+v", len(aces), len(want), aces)
	}
	for _, w := range want {
		found := false
		for _, a := range aces {
			found = found || a == w
		}
		if !found {
			t.Errorf("root is missing ACE %+v (have %+v)", w, aces)
		}
	}
	paths := append([]string{root, filepath.Join(root, "presets")}, files...)
	for _, p := range paths {
		if writable(t, p) {
			t.Errorf("%s is still writable by non-admins", p)
		}
	}
	for _, p := range paths[1:] {
		aces, protected := readACEs(t, p)
		if protected {
			t.Errorf("%s is still protected", p)
		}
		owner := false
		for _, a := range aces {
			if a.flags&windows.INHERITED_ACE == 0 {
				t.Errorf("%s keeps an explicit ACE %+v", p, a)
			}
			owner = owner || (a.sid == "S-1-3-4" && a.mask == 0x1200a9)
		}
		// OWNER RIGHTS must reach every child as is, or a file's owner
		// keeps its implicit WRITE_DAC.
		if !owner {
			t.Errorf("%s did not inherit the OWNER RIGHTS ACE: %+v", p, aces)
		}
	}
}

func TestApplyLocksTreeAndIsIdempotent(t *testing.T) {
	me := mySID(t)
	root, files := installTree(t, me)
	if !writable(t, files[0]) || !writable(t, filepath.Join(root, "presets")) {
		t.Fatal("setup: expected the planted ACLs to be non-admin writable")
	}

	extra := "(A;OICI;FA;;;" + me + ")"
	changed, err := apply(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first apply reported no change")
	}
	checkLocked(t, root, files, []ace{{windows.ACCESS_ALLOWED_ACE_TYPE, 0x3, 0x1f01ff, me}})

	changed, err = apply(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second apply changed something")
	}

	// A file created afterwards (the service writing state.json) inherits
	// the folder's rights and needs no further work.
	mustWrite(t, filepath.Join(root, "state.json"))
	if writable(t, filepath.Join(root, "state.json")) {
		t.Error("new file is writable by non-admins")
	}
	if changed, err = apply(root, extra); err != nil || changed {
		t.Errorf("apply after a new file: changed=%v err=%v", changed, err)
	}

	// An explicit ACE added later is found and removed again.
	setDACL(t, files[1], "D:P(A;;FA;;;AU)(A;;FA;;;"+me+")")
	if changed, err = apply(root, extra); err != nil || !changed {
		t.Errorf("apply after tampering: changed=%v err=%v", changed, err)
	}
	if writable(t, files[1]) {
		t.Error("tampered file is still writable by non-admins")
	}
}

// TestApplyProductionDACL applies the exact install DACL. Only an elevated
// test run can delete the folder afterwards, so it is skipped otherwise
// (CI runners are elevated).
func TestApplyProductionDACL(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated test run")
	}
	root, files := installTree(t, mySID(t))
	changed, err := Apply(root)
	if err != nil || !changed {
		t.Fatalf("Apply: changed=%v err=%v", changed, err)
	}
	checkLocked(t, root, files, nil)
	if changed, err := Apply(root); err != nil || changed {
		t.Errorf("second Apply: changed=%v err=%v", changed, err)
	}
}

func TestApplyLeavesJunctionTargetAlone(t *testing.T) {
	me := mySID(t)
	root := t.TempDir()
	target := t.TempDir()
	mustWrite(t, filepath.Join(target, "victim.txt"))
	setDACL(t, target, "D:P(A;OICI;0x1301bf;;;BU)(A;OICI;FA;;;"+me+")")
	link := filepath.Join(root, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
	t.Cleanup(func() { os.Remove(link) })

	before, _ := readACEs(t, target)
	beforeFile, _ := readACEs(t, filepath.Join(target, "victim.txt"))
	if _, err := apply(root, "(A;OICI;FA;;;"+me+")"); err != nil {
		t.Fatal(err)
	}
	after, _ := readACEs(t, target)
	afterFile, _ := readACEs(t, filepath.Join(target, "victim.txt"))
	if !equalACEs(before, after) || !equalACEs(beforeFile, afterFile) {
		t.Errorf("junction target changed:\n before %+v / %+v\n after  %+v / %+v", before, beforeFile, after, afterFile)
	}

	err := lockable(root, folders{})
	if !errors.Is(err, ErrNotDedicated) || !strings.Contains(err.Error(), "junction") {
		t.Errorf("lockable with a junction = %v, want ErrNotDedicated (junction)", err)
	}
}

func equalACEs(a, b []ace) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLockableEntryCap(t *testing.T) {
	root := t.TempDir()
	if err := lockable(root, folders{}); err != nil {
		t.Errorf("empty folder: %v", err)
	}
	// MaxEntries files plus their folder is one entry too many.
	for i := 0; i < MaxEntries; i++ {
		mustWrite(t, filepath.Join(root, "f", fmt.Sprintf("%03d.txt", i)))
	}
	if err := lockable(root, folders{}); !errors.Is(err, ErrNotDedicated) {
		t.Errorf("large tree: got %v, want ErrNotDedicated", err)
	}
	// The real well-known folders refuse a temp dir, which lives in a
	// user profile (or under Windows for SYSTEM).
	if err := Lockable(t.TempDir()); !errors.Is(err, ErrNotDedicated) {
		t.Errorf("Lockable(temp dir) = %v, want ErrNotDedicated", err)
	}
}

func TestPathReason(t *testing.T) {
	f := folders{
		exact: []string{`C:\Program Files`, `C:\Program Files (x86)`, `C:\ProgramData`, ""},
		trees: []string{`C:\Windows`, `C:\Users`, ""},
	}
	cases := []struct {
		dir  string
		want string // "" = may be locked
	}{
		{`C:\PC Control`, ""},
		{`C:\Program Files\SmartThings PC Control`, ""},
		{`D:\Apps\PC Control`, ""},
		{`C:\`, "drive or share root"},
		{`D:\`, "drive or share root"},
		{`\\server\share`, "drive or share root"},
		{`relative\dir`, "not an absolute path"},
		{`C:\Program Files`, "shared system folder"},
		{`c:\program files (X86)\`, "shared system folder"},
		{`C:\ProgramData`, "shared system folder"},
		{`C:\Windows`, "system or user-profile folder"},
		{`C:\Windows\System32`, "system or user-profile folder"},
		{`C:\Users\alice\Downloads\pc`, "system or user-profile folder"},
		{`C:\Users`, "system or user-profile folder"},
		{`C:\UsersData\pc`, ""},
		{`C:\Users\..\PC Control`, ""},
	}
	for _, c := range cases {
		if got := pathReason(c.dir, f); got != c.want {
			t.Errorf("pathReason(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

func TestGrantsNonAdminWrite(t *testing.T) {
	const (
		allow = windows.ACCESS_ALLOWED_ACE_TYPE
		deny  = windows.ACCESS_DENIED_ACE_TYPE
		oici  = 0x3
		io    = windows.INHERIT_ONLY_ACE
		inh   = windows.INHERITED_ACE
	)
	base := []ace{
		{allow, oici, 0x1f01ff, "S-1-5-18"},
		{allow, oici, 0x1f01ff, "S-1-5-32-544"},
		{allow, oici, 0x1200a9, "S-1-5-32-545"},
	}
	cases := []struct {
		name string
		add  []ace
		want bool
	}{
		{"install DACL", nil, false},
		{"program files (CREATOR OWNER inherit-only)", []ace{{allow, oici | io, windows.GENERIC_ALL, "S-1-3-0"}}, false},
		{"C:\\ child: Authenticated Users modify", []ace{{allow, inh, 0x1301bf, "S-1-5-11"}}, true},
		{"C:\\ child: Users append (CI)(AD)", []ace{{allow, 0x2 | inh, 0x4, "S-1-5-32-545"}}, true},
		{"Everyone full", []ace{{allow, 0, 0x1f01ff, "S-1-1-0"}}, true},
		{"INTERACTIVE generic write", []ace{{allow, 0, windows.GENERIC_WRITE, "S-1-5-4"}}, true},
		{"Users WRITE_DAC only", []ace{{allow, 0, windows.WRITE_DAC, "S-1-5-32-545"}}, true},
		{"inherit-only write for Users", []ace{{allow, oici | io, 0x1301bf, "S-1-5-32-545"}}, false},
		{"deny write for Everyone", []ace{{deny, 0, 0x1301bf, "S-1-1-0"}}, false},
		{"one user account may write", []ace{{allow, oici, 0x1f01ff, "S-1-5-21-1-2-3-1001"}}, false},
		{"Users write attributes only", []ace{{allow, 0, 0x100, "S-1-5-32-545"}}, false},
	}
	for _, c := range cases {
		if got := grantsNonAdminWrite(append(append([]ace{}, base...), c.add...)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNonAdminWritableOnDisk(t *testing.T) {
	me := mySID(t)
	dir := t.TempDir()
	// A fresh temp dir grants SYSTEM, Administrators and its owner only.
	if writable(t, dir) {
		t.Errorf("temp dir reported writable by non-admins: %+v", func() []ace { a, _ := readACEs(t, dir); return a }())
	}
	setDACL(t, dir, "D:P(A;OICI;0x1301bf;;;AU)(A;OICI;FA;;;"+me+")")
	if !writable(t, dir) {
		t.Error("Authenticated Users: Modify not detected")
	}
	setDACL(t, dir, "D:P(A;OICI;0x1200a9;;;BU)(A;OICI;FA;;;"+me+")")
	if writable(t, dir) {
		t.Error("Users: read & execute reported writable")
	}
	if _, err := NonAdminWritable(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing path: expected an error")
	}
}
