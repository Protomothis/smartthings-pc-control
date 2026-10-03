package secureacl

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOthersWithWrite(t *testing.T) {
	const me = "S-1-5-21-1-2-3-1001"
	ok := map[string]bool{sidSystem: true, sidAdministrators: true, me: true}
	allow := func(sid string, mask uint32) ace {
		return ace{typ: windows.ACCESS_ALLOWED_ACE_TYPE, mask: mask, sid: sid}
	}
	for _, tc := range []struct {
		name string
		aces []ace
		want string
	}{
		{"owner, SYSTEM, Administrators", []ace{allow(sidSystem, 0x1f01ff), allow(sidAdministrators, 0x1f01ff), allow(me, 0x1f01ff)}, ""},
		{"Users read & execute", []ace{allow(sidSystem, 0x1f01ff), allow("S-1-5-32-545", 0x1200a9)}, ""},
		{"Authenticated Users modify", []ace{allow(sidSystem, 0x1f01ff), allow("S-1-5-11", 0x1301bf)}, "S-1-5-11"},
		{"Everyone write data", []ace{allow("S-1-1-0", 0x2)}, "S-1-1-0"},
		{"another user full control", []ace{allow("S-1-5-21-1-2-3-1002", 0x1f01ff)}, "S-1-5-21-1-2-3-1002"},
		{"another user WRITE_DAC only", []ace{allow("S-1-5-21-1-2-3-1002", windows.WRITE_DAC)}, "S-1-5-21-1-2-3-1002"},
		{"inherit-only is about children", []ace{{typ: windows.ACCESS_ALLOWED_ACE_TYPE, flags: windows.INHERIT_ONLY_ACE, mask: 0x1f01ff, sid: "S-1-5-11"}}, ""},
		{"deny is not weighed", []ace{{typ: windows.ACCESS_DENIED_ACE_TYPE, mask: 0x1f01ff, sid: "S-1-1-0"}}, ""},
	} {
		if got := othersWithWrite(tc.aces, ok); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWritableByOthers(t *testing.T) {
	me := mySID(t)
	// The temp folder's own DACL is whatever this machine gives it (it may
	// well name a second account), so the folder gets the shape of a
	// user's own folder explicitly: SYSTEM, Administrators and this
	// account (the owner, or the target user), inherited by the files.
	dir := filepath.Join(t.TempDir(), "mine")
	plain := filepath.Join(dir, "run.ps1")
	mustWrite(t, plain)
	setDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+me+")")
	setDACL(t, plain, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")")
	if who, err := WritableByOthers(plain, me); err != nil || who != "" {
		t.Errorf("default temp file: %q, %v; want nobody", who, err)
	}
	if who, err := WritableByOthers(dir, me); err != nil || who != "" {
		t.Errorf("default temp folder: %q, %v; want nobody", who, err)
	}

	// The same file with an extra Everyone write ACE.
	open := filepath.Join(dir, "open.ps1")
	mustWrite(t, open)
	setDACL(t, open, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+me+")(A;;0x120116;;;WD)")
	if who, err := WritableByOthers(open, me); err != nil || who != "S-1-1-0" {
		t.Errorf("file with Everyone write: %q, %v; want S-1-1-0", who, err)
	}

	// A folder that lets Authenticated Users add files.
	sub := filepath.Join(dir, "shared")
	inner := filepath.Join(sub, "tool.exe")
	mustWrite(t, inner)
	setDACL(t, sub, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+me+")(A;;0x1301bf;;;AU)")
	if who, err := WritableByOthers(sub, me); err != nil || who != "S-1-5-11" {
		t.Errorf("folder with Authenticated Users modify: %q, %v; want S-1-5-11", who, err)
	}

	if _, err := WritableByOthers(filepath.Join(dir, "missing.exe"), me); err == nil {
		t.Error("a missing file gave no error")
	}
}
