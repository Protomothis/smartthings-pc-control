package gui

// Checking for and applying app updates (update.go, selfupdate.go hold
// the download and verification).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// checkForUpdates queries GitHub Releases; on a newer version it notifies
// the user (dialog on startup, tray notification on periodic checks).
func (u *ui) checkForUpdates(startup bool) {
	rel, err := checkLatestRelease()
	if err != nil || !release.IsNewer(u.version, rel.TagName) {
		return
	}
	// Notify once per discovered version on periodic checks.
	if !startup && u.app.Preferences().String("update_notified") == rel.TagName {
		return
	}
	u.app.Preferences().SetString("update_notified", rel.TagName)

	fyne.Do(func() {
		body := fmt.Sprintf(u.t("update.body"), rel.TagName, u.version)
		u.app.SendNotification(fyne.NewNotification(u.t("update.title"), body))
		if startup {
			u.showUpdateDialog(rel)
		}
	})
}

// checkForUpdatesManual is the "Check for updates" button: always reports
// a result — newer release (update dialog), up to date, or the error.
// btn is the button that asked; it is busy while GitHub answers. UI
// goroutine only.
func (u *ui) checkForUpdatesManual(btn *widget.Button) {
	runAsync(busyControls(btn), checkLatestRelease, func(rel *release.Info, err error) {
		switch {
		case err != nil:
			dialog.ShowError(errors.New(u.t("update.checkfailed")+err.Error()), u.win)
		case release.IsNewer(u.version, rel.TagName):
			u.showUpdateDialog(rel)
		case !u.canSelfUpdate():
			// dev build: version comparison is meaningless, but the
			// user asked — offer the release page.
			u.showUpdateDialog(rel)
		default:
			dialog.ShowInformation(u.t("update.title"), fmt.Sprintf(u.t("update.uptodate"), u.version), u.win)
		}
	})
}

// canSelfUpdate is false for "dev"/empty builds — those may check for
// releases but must never overwrite themselves.
func (u *ui) canSelfUpdate() bool {
	_, ok := release.ParseVersion(u.version)
	return ok
}

// showUpdateDialog fetches and verifies the release's signed update
// manifest (#66) off the UI goroutine, then offers "Update now" when this
// is a release build and the manifest names an asset for this arch that the
// installed version may update to. Anything else — dev build, unsigned
// release, bad signature, min_version gate — falls back to opening the
// release page. Must be called on the UI goroutine.
func (u *ui) showUpdateDialog(rel *release.Info) {
	if !u.canSelfUpdate() {
		u.showUpdateChoice(rel, nil, nil)
		return
	}
	runAsync(nil, func() (*release.Manifest, error) { return fetchManifest(rel) },
		func(m *release.Manifest, err error) { u.showUpdateChoice(rel, m, err) })
}

// showUpdateChoice renders the update dialog for rel given the manifest
// fetch result (m/err both nil for dev builds, which only get the release
// page). Must run on the UI goroutine.
func (u *ui) showUpdateChoice(rel *release.Info, m *release.Manifest, err error) {
	page := rel.HTMLURL
	if page == "" {
		page = release.Page
	}
	openPage := func() { _ = exec.Command("cmd", "/c", "start", page).Start() }

	body := container.NewVBox(widget.NewLabel(fmt.Sprintf(u.t("update.body"), rel.TagName, u.version)))
	if pageURL, err := url.Parse(page); err == nil {
		body.Add(widget.NewHyperlink(u.t("update.releasepage"), pageURL))
	}
	note := func(text string) {
		l := widget.NewLabel(text)
		l.Wrapping = fyne.TextWrapWord
		body.Add(l)
	}
	pageOnly := func() {
		dialog.ShowCustomConfirm(u.t("update.title"), u.t("update.open"), u.t("update.later"), body,
			func(ok bool) {
				if ok {
					openPage()
				}
			}, u.win)
	}

	var asset *release.ManifestAsset
	var assetURL string
	switch {
	case !u.canSelfUpdate():
		// dev build: version comparison is meaningless, never overwrite.
	case errors.Is(err, release.ErrNoManifest):
		note(u.t("update.unsigned"))
	case errors.Is(err, release.ErrBadSignature):
		note(u.t("update.badsig"))
	case err != nil:
		note(u.t("update.checkfailed") + err.Error())
	case !m.Allows(u.version):
		note(fmt.Sprintf(u.t("update.minversion"), m.MinVersion))
	default:
		var ok bool
		asset, ok = m.AssetFor(runtime.GOARCH)
		if ok {
			assetURL = release.AssetURL(rel, asset.Name)
		}
		if !ok || assetURL == "" {
			asset = nil
			note(u.t("update.noasset"))
		}
	}
	if asset == nil {
		pageOnly()
		return
	}
	dialog.ShowCustomConfirm(u.t("update.title"), u.t("update.now"), u.t("update.later"), body,
		func(ok bool) {
			if ok {
				u.startSelfUpdate(rel, m, asset, assetURL)
			}
		}, u.win)
}

// startSelfUpdate downloads the manifest's asset behind a progress dialog,
// checks its SHA-256/size against the signed manifest, runs the exe's own
// version check, then hands over to the elevated updater (see
// selfupdate.go) and quits — the updater waits for this process to exit
// before swapping the binary. Download errors, a hash mismatch and a
// declined UAC prompt leave the app running.
func (u *ui) startSelfUpdate(rel *release.Info, m *release.Manifest, asset *release.ManifestAsset, assetURL string) {
	status := widget.NewLabel(u.t("update.downloading"))
	status.Wrapping = fyne.TextWrapWord // the "applying" text is a couple of sentences
	bar := widget.NewProgressBar()
	ctx, cancel := context.WithCancel(context.Background())
	d := dialog.NewCustom(u.t("update.title"), u.t("update.cancel"), container.NewVBox(status, bar), u.win)
	d.SetOnClosed(cancel)
	d.Resize(fyne.NewSize(440, 200))
	d.Show()

	go func() {
		path, err := downloadUpdate(ctx, assetURL, stagingDir(), rel.TagName, func(done, total int64) {
			fyne.Do(func() {
				if total > 0 {
					bar.SetValue(float64(done) / float64(total))
					status.SetText(fmt.Sprintf("%s  %s / %s", u.t("update.downloading"), formatBytes(done), formatBytes(total)))
				} else {
					status.SetText(fmt.Sprintf("%s  %s", u.t("update.downloading"), formatBytes(done)))
				}
			})
		})
		if err == nil {
			// First gate: the bytes must be exactly what the signed
			// manifest promised. Nothing has executed yet.
			fyne.Do(func() { status.SetText(u.t("update.checking")) })
			err = verifyDownloadedHash(path, asset)
		}
		if err == nil {
			fyne.Do(func() { status.SetText(u.t("update.verifying")) })
			err = verifyDownloadedExe(path, rel.TagName)
			if err != nil {
				os.Remove(path)
			}
		}
		if err == nil {
			// The elevated updater re-verifies everything itself from its
			// own admin-only copies (#126), so it gets the signed manifest
			// too.
			fyne.Do(func() { status.SetText(u.t("update.applying")) })
			var man, sig string
			if man, sig, err = stageManifest(filepath.Dir(path), m); err == nil {
				err = runElevatedSelf(fmt.Sprintf(`update-apply "%s" %d "%s" "%s"`, path, os.Getpid(), man, sig))
			}
		}
		fyne.Do(func() {
			d.Hide()
			if err != nil {
				if ctx.Err() == nil { // user cancel is not an error worth a dialog
					msg := u.t("update.failed") + err.Error()
					if errors.Is(err, errHashMismatch) {
						msg = u.t("update.hashmismatch")
					}
					dialog.ShowError(errors.New(msg), u.win)
				}
				return
			}
			// The elevated updater is waiting for this pid to exit.
			u.app.Quit()
		})
	}()
}
