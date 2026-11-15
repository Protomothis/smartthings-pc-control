package ui

// 업데이트: the running version and the newest release. The shell checks
// GitHub at start and daily (shell.go: snapshot.updateTag/updateCheck,
// the badge on the home update row). Phase 2a shows that; the check now
// button, release notes, skip and the self-update with its progress are
// #165 phase 2b and replace this file.

type updatePage struct{}

func newUpdatePage() screen { return &updatePage{} }

func (p *updatePage) title(u *UI) string { return u.t("update.manual") }

func (p *updatePage) body(gtx C, u *UI, s snapshot) D {
	th := u.th
	newest := "—"
	if s.updateTag != "" {
		newest = s.updateTag
	}
	return sections(gtx,
		section(th, "", func(gtx C) D {
			return infoRows(gtx,
				infoRow(th, u.t("home.version"), u.version),
				infoRow(th, u.t("update.title"), newest),
				infoRow(th, u.t("update.check"), u.onOff(u.prefs.Bool(prefCheckUpdates, true))),
			)
		}),
		pendingNote(u),
	)
}
