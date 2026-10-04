package gui

// Checking for and applying app updates (update.go, selfupdate.go hold
// the download and verification).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// checkForUpdates queries GitHub Releases; on a newer version it notifies
// the user (dialog on startup, tray notification on periodic checks). A
// version the user chose to skip stays silent here; the next one prompts
// again (#135).
func (u *ui) checkForUpdates(startup bool) {
	chk, err := checkLatestRelease()
	if err != nil || !release.IsNewer(u.version, chk.rel.TagName) {
		return
	}
	rel := chk.rel
	if u.app.Preferences().String("update_skipped") == rel.TagName {
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
			u.showUpdateDialog(chk)
		}
	})
}

// checkForUpdatesManual is the "Check for updates" button: always reports
// a result — newer release (update dialog, even for a skipped version),
// up to date, or the error. btn is the button that asked; it is busy while
// GitHub answers. UI goroutine only.
func (u *ui) checkForUpdatesManual(btn *widget.Button) {
	runAsync(busyControls(btn), checkLatestRelease, func(chk updateCheck, err error) {
		switch {
		case err != nil:
			dialog.ShowError(errors.New(u.t("update.checkfailed")+err.Error()), u.win)
		case release.IsNewer(u.version, chk.rel.TagName):
			u.showUpdateDialog(chk)
		case !u.canSelfUpdate():
			// dev build: version comparison is meaningless, but the
			// user asked — offer the release page.
			u.showUpdateDialog(chk)
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
func (u *ui) showUpdateDialog(chk updateCheck) {
	if !u.canSelfUpdate() {
		u.showUpdateChoice(chk, nil, nil)
		return
	}
	runAsync(nil, func() (*release.Manifest, error) { return fetchManifest(chk.rel) },
		func(m *release.Manifest, err error) { u.showUpdateChoice(chk, m, err) })
}

// showUpdateChoice renders the update dialog for chk.rel given the
// manifest fetch result (m/err both nil for dev builds, which only get the
// release page): the release notes since this version, then "Update now"
// or "Open download page", "Later" and "Skip this version". Must run on
// the UI goroutine.
func (u *ui) showUpdateChoice(chk updateCheck, m *release.Manifest, err error) {
	rel := chk.rel
	page := rel.HTMLURL
	if page == "" {
		page = release.Page
	}
	openPage := func() { _ = systool.Command(systool.Cmd, "/c", "start", page).Start() }

	body := container.NewVBox(
		widget.NewLabel(fmt.Sprintf(u.t("update.body"), rel.TagName, u.version)),
		u.releaseNotes(updateNotes(chk, u.version)),
	)
	if pageURL, err := url.Parse(page); err == nil {
		body.Add(widget.NewHyperlink(u.t("update.releasepage"), pageURL))
	}
	note := func(text string) {
		l := widget.NewLabel(text)
		l.Wrapping = fyne.TextWrapWord
		body.Add(l)
	}
	skipped := u.app.Preferences().String("update_skipped") == rel.TagName
	if skipped {
		note(u.t("update.skipped"))
	}
	pageOnly := func() {
		u.showUpdatePrompt(rel.TagName, skipped, body, u.t("update.open"), openPage)
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
	u.showUpdatePrompt(rel.TagName, skipped, body, u.t("update.now"), func() {
		u.startSelfUpdate(rel, m, asset, assetURL)
	})
}

// showUpdatePrompt shows the update dialog with body and three buttons in
// Fyne's own dialog button row: "Skip this version", "Later" and the
// primary action. When tag is already skipped (only the manual check gets
// here then) the first button lifts the skip instead, so the automatic
// checks offer tag again. UI goroutine only.
func (u *ui) showUpdatePrompt(tag string, skipped bool, body fyne.CanvasObject, primary string, onPrimary func()) {
	d := dialog.NewCustomWithoutButtons(u.t("update.title"), body, u.win)
	skipText, skipTo := u.t("update.skip"), tag
	if skipped {
		skipText, skipTo = u.t("update.unskip"), ""
	}
	skip := widget.NewButton(skipText, func() {
		d.Hide()
		u.app.Preferences().SetString("update_skipped", skipTo)
	})
	ok := widget.NewButton(primary, func() {
		d.Hide()
		onPrimary()
	})
	ok.Importance = widget.HighImportance
	d.SetButtons([]fyne.CanvasObject{skip, widget.NewButton(u.t("update.later"), d.Hide), ok})
	d.Show()
}

// maxNotes caps the versions the update dialog shows notes for; the rest
// are a link to the releases page away.
const maxNotes = 10

// updateNotes is the releases whose notes the dialog for chk shows: every
// app release newer than current, highest first. A build that is not
// behind (dev, or the manual check of an rc) gets the offered release's
// notes alone.
func updateNotes(chk updateCheck, current string) []release.Info {
	if notes := release.NotesSince(chk.list, current); len(notes) > 0 {
		return notes
	}
	return []release.Info{*chk.rel}
}

// releaseNotes builds the dialog's scrollable notes area: per release a
// "v1.2.0 · 2026-10-03" heading and its notes as word-wrapped Markdown,
// at most maxNotes of them, then a link to the releases page.
func (u *ui) releaseNotes(notes []release.Info) fyne.CanvasObject {
	box := container.NewVBox()
	for i := range notes {
		if i == maxNotes {
			if pageURL, err := url.Parse(release.Page); err == nil {
				box.Add(widget.NewHyperlink(u.t("update.morenotes"), pageURL))
			}
			break
		}
		r := &notes[i]
		if i > 0 {
			box.Add(widget.NewSeparator())
		}
		heading := r.TagName
		if !r.PublishedAt.IsZero() {
			heading += " · " + r.PublishedAt.Local().Format("2006-01-02")
		}
		box.Add(widget.NewRichText(&widget.TextSegment{Text: heading, Style: widget.RichTextStyleSubHeading}))
		md := r.Notes()
		if md == "" {
			box.Add(widget.NewLabel(u.t("update.nonotes")))
			continue
		}
		text := widget.NewRichTextFromMarkdown(compactHeadings(md))
		text.Wrapping = fyne.TextWrapWord
		box.Add(text)
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(420, 300))
	return scroll
}

// compactHeadings turns level 1 and 2 Markdown headings into level 3.
// Fyne 2.8.1 renders # and ## larger than the version heading above them
// and ### and deeper as bold body text, which keeps the notes compact.
// Lines inside fenced code blocks are left alone.
func compactHeadings(md string) string {
	lines := strings.Split(md, "\n")
	fenced := false
	for i, line := range lines {
		t := strings.TrimLeft(line, " ")
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || len(line)-len(t) > 3 { // 4 spaces: an indented code block
			continue
		}
		level := len(t) - len(strings.TrimLeft(t, "#"))
		if rest := t[level:]; (level == 1 || level == 2) && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			lines[i] = "###" + rest
		}
	}
	return strings.Join(lines, "\n")
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
