package gui

import "testing"

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
