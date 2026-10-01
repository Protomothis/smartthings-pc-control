package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsRiskyInstallDir(t *testing.T) {
	const (
		home = `C:\Users\alice`
		temp = `C:\Users\alice\AppData\Local\Temp`
	)
	cases := []struct {
		name   string
		exeDir string
		want   bool
	}{
		{"program files", `C:\Program Files\SmartThings PC Control`, false},
		// Path only: who may write there is nonAdminWritable's job (#126).
		{"custom root folder", `C:\PC Control`, false},
		{"home subfolder that is not a risky one", `C:\Users\alice\Apps\PCControl`, false},
		{"appdata (outside temp)", `C:\Users\alice\AppData\Local\PCControl`, false},
		{"downloads", `C:\Users\alice\Downloads`, true},
		{"downloads, case-insensitive", `c:\users\ALICE\downloads`, true},
		{"downloads subfolder", `C:\Users\alice\Downloads\smartthings-pc-control-v0.3.3`, true},
		{"desktop", `C:\Users\alice\Desktop`, true},
		{"documents subfolder", `C:\Users\alice\Documents\tools`, true},
		{"home root itself", `C:\Users\alice`, true},
		{"home root with trailing separator", `C:\Users\alice\`, true},
		{"temp", temp, true},
		{"temp subfolder (7-zip style extraction)", temp + `\7zO1234`, true},
		{"prefix that only looks like downloads", `C:\Users\alice\DownloadsArchive`, false},
		{"different user's downloads", `C:\Users\bob\Downloads`, false},
		{"unclean path resolving into downloads", `C:\Users\alice\Apps\..\Downloads\x`, true},
	}
	for _, c := range cases {
		if got := isRiskyInstallDir(c.exeDir, home, temp); got != c.want {
			t.Errorf("%s: isRiskyInstallDir(%q) = %v, want %v", c.name, c.exeDir, got, c.want)
		}
	}
}

func TestIsRiskyInstallDirUnknownEnv(t *testing.T) {
	// With no home/temp known nothing can be judged risky — the warning
	// must never block a legitimate install.
	if isRiskyInstallDir(`C:\Users\alice\Downloads`, "", "") {
		t.Error("expected false when home and temp are unknown")
	}
	if !isRiskyInstallDir(`D:\tmp\x`, "", `D:\tmp`) {
		t.Error("temp rule should still apply without a home dir")
	}
}

func TestClassifyWritable(t *testing.T) {
	if got := classifyWritable(true); got != installDirWritable {
		t.Errorf("lockable: got %v, want installDirWritable", got)
	}
	if got := classifyWritable(false); got != installDirUnsafe {
		t.Errorf("not lockable: got %v, want installDirUnsafe", got)
	}
}

// Every warning has its dialog text, in both languages, with the two %s
// (exe folder, recommended folder) the install button fills in.
func TestInstallDirRiskBodies(t *testing.T) {
	for _, r := range []installDirRisk{installDirVolatile, installDirWritable, installDirUnsafe} {
		key, ok := installDirRiskBodyKey[r]
		if !ok {
			t.Errorf("risk %d has no dialog text", r)
			continue
		}
		for _, l := range []Lang{LangKo, LangEn} {
			if s := messages[key][l]; strings.Count(s, "%s") != 2 {
				t.Errorf("%s/%s: want two %%s, got %q", key, l, s)
			}
		}
	}
}

func setTestDACL(t *testing.T, path, sddl string) {
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

// The effective check behind the path heuristic (#126): a folder made
// directly under C:\ inherits "Authenticated Users: Modify" and must be
// flagged even though its path looks fine; once locked down it must not.
func TestNonAdminWritableInstallDir(t *testing.T) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := "(A;OICI;FA;;;" + u.User.Sid.String() + ")" // keeps t.TempDir removable
	dir := t.TempDir()
	exe := filepath.Join(dir, "smartthings-pc-control.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	if nonAdminWritable(dir, exe) {
		t.Error("fresh temp dir (owner, SYSTEM, Administrators only) flagged")
	}
	// What C:\PC Control inherits from the drive root.
	setTestDACL(t, dir, "D:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)(A;;0x1301bf;;;AU)(A;OICIIO;0x1301bf;;;AU)"+me)
	if !nonAdminWritable(dir, exe) {
		t.Error("default C:\\ child ACL not flagged")
	}
	// The locked-down install DACL.
	setTestDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;0x1200a9;;;OW)"+me)
	setTestDACL(t, exe, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;BU)"+me)
	if nonAdminWritable(dir, exe) {
		t.Error("locked-down folder flagged")
	}
	// The exe alone being writable is enough.
	setTestDACL(t, exe, "D:P(A;;FA;;;WD)"+me)
	if !nonAdminWritable(dir, exe) {
		t.Error("Everyone-writable exe not flagged")
	}
	// Missing paths never block an install.
	if nonAdminWritable(filepath.Join(dir, "missing")) {
		t.Error("missing path flagged")
	}
}
