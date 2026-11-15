package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

func TestOwnedByInstaller(t *testing.T) {
	cases := []struct {
		exeDir, location string
		want             bool
	}{
		{`C:\Program Files\SmartThings PC Control`, `C:\Program Files\SmartThings PC Control\`, true}, // Inno's trailing backslash
		{`C:\Program Files\SmartThings PC Control`, `c:\program files\smartthings pc control`, true},
		{`C:\Program Files\SmartThings PC Control`, `C:\Program Files\SmartThings PC Control\.`, true},
		{`C:\PC Control`, `C:\PC Control\`, true},                                    // a [설치] copy the setup upgraded in place
		{`C:\Users\me\Downloads`, `C:\Program Files\SmartThings PC Control\`, false}, // a portable copy beside an installed one
		{`C:\Program Files\SmartThings PC Control\sub`, `C:\Program Files\SmartThings PC Control\`, false},
		{`C:\Program Files\SmartThings PC Control`, ``, false},
		{``, `C:\Program Files\SmartThings PC Control\`, false},
	}
	for _, c := range cases {
		if got := ownedByInstaller(c.exeDir, c.location); got != c.want {
			t.Errorf("ownedByInstaller(%q, %q) = %v, want %v", c.exeDir, c.location, got, c.want)
		}
	}
}

// The setup script and the app must agree on the AppId (how the app finds
// the setup's uninstall key) and on the Start menu shortcut (the app keeps
// no per-user twin of an identical one, internal/appid).
func TestInstallerScriptAgrees(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "installer", "smartthings-pc-control.iss"))
	if err != nil {
		t.Fatal(err)
	}
	iss := string(src)
	// AppId={{GUID} in the script: "{{" is an escaped brace.
	m := regexp.MustCompile(`(?m)^AppId=\{(\{[0-9A-F-]+\})\s*$`).FindStringSubmatch(iss)
	if m == nil || m[1] != installerAppID {
		t.Errorf("AppId in the .iss = %v, want %s", m, installerAppID)
	}
	for _, want := range []string{
		`#define AppUserModelID "` + appid.AUMID + `"`,
		`#define AppName "` + appid.DisplayName + `"`,
		// A [설치] copy is upgraded where its service runs from, else the
		// recommended folder.
		`DefaultDirName={code:DefaultInstallDir}`,
		`ExpandConstant('{autopf}\{#AppName}')`,
		`#define ServiceName "` + serviceName + `"`,
		`#define AppExe "smartthings-pc-control.exe"`,
	} {
		if !strings.Contains(iss, want) {
			t.Errorf("the .iss lacks %s", want)
		}
	}
	icons := regexp.MustCompile(`(?m)^Name: "\{autoprograms\}\\\{#AppName\}".*$`).FindString(iss)
	for _, want := range []string{`Parameters: "gui"`, `WorkingDir: "{app}"`, `AppUserModelID: "{#AppUserModelID}"`} {
		if !strings.Contains(icons, want) {
			t.Errorf("Start menu shortcut %q lacks %s", icons, want)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(recommendedInstallDir, `\`), `\`+appid.DisplayName) {
		t.Errorf("recommendedInstallDir %q is not the setup's folder", recommendedInstallDir)
	}
}

// stagedInstallerUpdate is newStagedUpdate with the release's setup staged
// instead of the exe and listed under the manifest's installer key.
func stagedInstallerUpdate(t *testing.T, version string) *stagedUpdate {
	t.Helper()
	s := newStagedUpdate(t, version)
	setup := []byte("MZ setup " + version)
	sum := sha256.Sum256(setup)
	m := *s.m
	m.Installer = &release.ManifestAsset{Name: "SmartThingsPCControl-Setup-" + version + ".exe", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(setup))}
	data, err := release.EncodeManifest(&m)
	if err != nil {
		t.Fatal(err)
	}
	sig := release.EncodeSignature(release.Sign(data, s.priv))
	if s.m, err = release.VerifyManifest(data, sig, s.pub); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Dir(s.args.NewExe)
	os.Remove(s.args.NewExe)
	s.args.NewExe = installerStagingPath(staging, version)
	s.payload = setup
	if err := os.WriteFile(s.args.NewExe, setup, 0o644); err != nil {
		t.Fatal(err)
	}
	if s.args.Manifest, s.args.Signature, err = stageManifest(staging, s.m); err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeStartSetup records the setup update-install would start.
func fakeStartSetup(t *testing.T, fail error) *[]string {
	t.Helper()
	var got []string
	orig := startSetup
	startSetup = func(setup string, args []string) error {
		got = append([]string{setup}, args...)
		return fail
	}
	t.Cleanup(func() { startSetup = orig })
	return &got
}

func TestRunVerifiedSetup(t *testing.T) {
	origKey := updatePublicKey
	t.Cleanup(func() { updatePublicKey = origKey })

	t.Run("runs the verified copy silently", func(t *testing.T) {
		s := stagedInstallerUpdate(t, "v1.3.0")
		updatePublicKey = s.pub
		started := fakeStartSetup(t, nil)
		if err := runVerifiedSetup(s.args, s.cur, "v1.2.4"); err != nil {
			t.Fatal(err)
		}
		got := *started
		if len(got) == 0 || got[0] != filepath.Join(s.updateDir(), verifiedSetupName) {
			t.Fatalf("started %q, want the verified copy in %s", got, s.updateDir())
		}
		if b, _ := os.ReadFile(got[0]); string(b) != string(s.payload) {
			t.Errorf("verified copy = %q", b)
		}
		joined := strings.Join(got[1:], " ")
		for _, flag := range []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/RELAUNCH=1", "/LOG=" + filepath.Join(s.updateDir(), setupLogName)} {
			if !strings.Contains(joined, flag) {
				t.Errorf("setup args %q lack %s", joined, flag)
			}
		}
		if _, err := os.Stat(s.args.NewExe); !os.IsNotExist(err) {
			t.Error("the user-writable staged setup was left behind")
		}
		if b, _ := os.ReadFile(s.cur); string(b) != "MZ installed" {
			t.Error("update-install touched the installed exe itself")
		}
	})

	t.Run("tampered setup never starts", func(t *testing.T) {
		s := stagedInstallerUpdate(t, "v1.3.0")
		updatePublicKey = s.pub
		started := fakeStartSetup(t, nil)
		os.WriteFile(s.args.NewExe, []byte("MZ evil"), 0o644)
		err := runVerifiedSetup(s.args, s.cur, "v1.2.4")
		if !errors.Is(err, errHashMismatch) {
			t.Errorf("err = %v, want errHashMismatch", err)
		}
		if len(*started) != 0 {
			t.Errorf("a tampered setup was started: %q", *started)
		}
		s.assertInstalledUntouched(t)
	})

	t.Run("a manifest without an installer is refused", func(t *testing.T) {
		s := newStagedUpdate(t, "v1.3.0") // exe-only manifest
		updatePublicKey = s.pub
		started := fakeStartSetup(t, nil)
		if err := runVerifiedSetup(s.args, s.cur, "v1.2.4"); err == nil || !strings.Contains(err.Error(), "installer") {
			t.Errorf("err = %v, want no installer", err)
		}
		if len(*started) != 0 {
			t.Error("something was started")
		}
	})

	t.Run("not newer", func(t *testing.T) {
		s := stagedInstallerUpdate(t, "v1.3.0")
		updatePublicKey = s.pub
		started := fakeStartSetup(t, nil)
		if err := runVerifiedSetup(s.args, s.cur, "v1.3.0"); err == nil {
			t.Error("the same version was installed again")
		}
		if len(*started) != 0 {
			t.Error("something was started")
		}
	})

	t.Run("start failure clears the update folder", func(t *testing.T) {
		s := stagedInstallerUpdate(t, "v1.3.0")
		updatePublicKey = s.pub
		fakeStartSetup(t, errors.New("boom"))
		if err := runVerifiedSetup(s.args, s.cur, "v1.2.4"); err == nil {
			t.Fatal("start failure not reported")
		}
		s.assertInstalledUntouched(t)
	})
}

func TestPlanInstallerUpdate(t *testing.T) {
	exe := release.ManifestAsset{Name: "smartthings-pc-control.exe", SHA256: strings.Repeat("ab", 32)}
	setup := &release.ManifestAsset{Name: "SmartThingsPCControl-Setup-1.3.0.exe", SHA256: strings.Repeat("cd", 32)}
	rel := &release.Info{TagName: "v1.3.0", Assets: []release.Asset{
		{Name: exe.Name, DownloadURL: "https://x/exe"},
		{Name: setup.Name, DownloadURL: "https://x/setup"},
	}}
	chk := NewUpdateCheck(rel, nil)
	withInstaller := &release.Manifest{Version: "v1.3.0", Assets: []release.ManifestAsset{exe}, Installer: setup}
	exeOnly := &release.Manifest{Version: "v1.3.0", Assets: []release.ManifestAsset{exe}}
	setupOnly := &release.Manifest{Version: "v1.3.0", Assets: []release.ManifestAsset{{Name: "other.zip", SHA256: "x", Arch: "arm64"}}, Installer: setup}

	cases := []struct {
		name      string
		m         *release.Manifest
		err       error
		version   string
		apply     bool
		installer bool
		url       string
		block     UpdateBlock
	}{
		{"setup listed", withInstaller, nil, "v1.2.4", true, true, "https://x/setup", UpdateCanApply},
		{"release before #166 swaps the exe", exeOnly, nil, "v1.2.4", true, false, "https://x/exe", UpdateCanApply},
		{"setup without an exe for this arch", setupOnly, nil, "v1.2.4", true, true, "https://x/setup", UpdateCanApply},
		{"unsigned stays blocked", nil, release.ErrNoManifest, "v1.2.4", false, false, "", UpdateUnsigned},
		{"bad signature stays blocked", nil, release.ErrBadSignature, "v1.2.4", false, false, "", UpdateBadSig},
		{"dev build", withInstaller, nil, "dev", false, false, "", UpdateDevBuild},
	}
	for _, c := range cases {
		p := PlanInstallerUpdate(chk, c.version, c.m, c.err)
		if p.CanApply() != c.apply || p.ViaInstaller() != c.installer || p.assetURL != c.url || p.Block != c.block {
			t.Errorf("%s: apply=%v installer=%v url=%q block=%v; want %v %v %q %v",
				c.name, p.CanApply(), p.ViaInstaller(), p.assetURL, p.Block, c.apply, c.installer, c.url, c.block)
		}
	}
	// The self-update plan never picks the setup, whatever the manifest.
	if p := PlanUpdate(chk, "v1.2.4", withInstaller, nil); p.ViaInstaller() || p.assetURL != "https://x/exe" {
		t.Errorf("PlanUpdate chose %q (installer %v)", p.assetURL, p.ViaInstaller())
	}
	// A setup the release does not carry as an asset: back to the exe.
	noAsset := NewUpdateCheck(&release.Info{TagName: "v1.3.0", Assets: rel.Assets[:1]}, nil)
	if p := PlanInstallerUpdate(noAsset, "v1.2.4", withInstaller, nil); p.ViaInstaller() || !p.CanApply() {
		t.Errorf("missing setup asset: installer %v, apply %v", p.ViaInstaller(), p.CanApply())
	}
}

func TestInstallerTexts(t *testing.T) {
	via := UpdatePlan{Block: UpdateCanApply, asset: &release.ManifestAsset{}, installer: true}
	swap := UpdatePlan{Block: UpdateCanApply, asset: &release.ManifestAsset{}}
	if via.ApplyingText(LangKo) != T(LangKo, "update.applying.installer") || swap.ApplyingText(LangKo) != T(LangKo, "update.applying") {
		t.Error("ApplyingText does not follow the plan")
	}
	if got := (UpdateProgress{Stage: StageApplying, Installer: true}).Text(LangEn); got != T(LangEn, "update.applying.installer") {
		t.Errorf("installer progress text = %q", got)
	}
	if got := installerStagingPath(`C:\s`, `v1.3.0/../x`); got != `C:\s\SmartThingsPCControl-Setup.v1.3.0_.._x.exe` {
		t.Errorf("installerStagingPath = %q", got)
	}
}
