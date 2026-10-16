package gui

// The About tab (#156): which program and build this is, where to read
// more or report a bug, the licences, and — small, at the very bottom — how
// to support the project. Support is only ever asked for here: no pop-up,
// notification or toast. Nothing on the tab needs the service, so it stays
// usable while the service is unreachable (applyConnected).

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// ossNotice names an open-source module built into the exe and its
// licence. Every direct requirement in go.mod has one (about_tab_test.go);
// the indirect ones are behind the go.mod link.
type ossNotice struct {
	name, module, license string
}

var ossNotices = []ossNotice{
	{"Fyne", "fyne.io/fyne/v2", "BSD-3-Clause"},
	{"Fyne systray", "fyne.io/systray", "Apache-2.0"},
	{"Go x/sys", "golang.org/x/sys", "BSD-3-Clause"},
	{"rsc.io/qr", "rsc.io/qr", "BSD-3-Clause"},
}

// versionKindKey says what sort of build version is: a release tag, a
// pre-release (v1.3.0-rc1) or anything else ("dev", a CI build).
func versionKindKey(version string) string {
	v, ok := release.ParseVersion(version)
	switch {
	case !ok:
		return "about.build.dev"
	case v.Pre != "":
		return "about.build.pre"
	}
	return "about.build.release"
}

func (u *ui) buildAboutTab() fyne.CanvasObject {
	name := widget.NewLabelWithStyle(windowTitle, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	version := widget.NewLabel(fmt.Sprintf(u.t("about.version"), u.version, u.t(versionKindKey(u.version))))
	// The same check as the settings tab's button (update_ui.go).
	var updateBtn *widget.Button
	updateBtn = widget.NewButtonWithIcon(u.t("update.manual"), theme.DownloadIcon(), func() {
		u.checkForUpdatesManual(updateBtn)
	})
	program := container.NewVBox(
		name,
		hint(u.t("about.tagline")),
		version,
		container.NewHBox(updateBtn, layout.NewSpacer()),
	)

	link := func(key, raw string) fyne.CanvasObject {
		return widget.NewHyperlink(u.t(key), mustURL(raw))
	}
	links := container.NewVBox(
		link("about.link.repo", repoURL),
		link("about.link.wiki", wikiURL),
		link("about.link.changelog", changelogURL),
		link("about.link.issues", issuesURL),
		link("about.link.invite", driverInviteURL),
	)

	oss := ""
	for i, n := range ossNotices {
		if i > 0 {
			oss += ", "
		}
		oss += n.name + " (" + n.license + ")"
	}
	licenses := container.NewVBox(
		container.NewHBox(widget.NewLabel(u.t("about.license")), link("about.license.link", licenseURL), layout.NewSpacer()),
		hint(u.t("about.oss")+": "+oss),
		hint(u.t("about.oss.font")),
		link("about.oss.all", goModURL),
	)

	// Low importance throughout: a quiet line and two flat buttons.
	open := func(raw string) func() {
		return func() {
			if err := openLink(raw); err != nil {
				dialog.ShowError(err, u.win)
			}
		}
	}
	star := widget.NewButton(u.t("about.star"), open(repoURL))
	star.Importance = widget.LowImportance
	sponsor := widget.NewButton(u.t("about.sponsor"), open(sponsorURL))
	sponsor.Importance = widget.LowImportance
	support := container.NewVBox(
		hint(u.t("about.support")),
		container.NewHBox(star, sponsor, layout.NewSpacer()),
	)

	return container.NewVScroll(container.NewPadded(container.NewVBox(
		section(u.t("about.program"), program),
		widget.NewSeparator(),
		section(u.t("about.links"), links),
		widget.NewSeparator(),
		section(u.t("about.licenses"), licenses),
		widget.NewSeparator(),
		support,
	)))
}
