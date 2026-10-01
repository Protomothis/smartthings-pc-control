package gui

// The SmartThings tab (the network tab before #128): how the Edge driver
// reaches this PC — the SmartThings section (st_section.go: hub, this PC's
// id, discovery, WoL adapter, allowed hubs) and the Wake-on-LAN adapter
// list. What the PC shares with SmartThings is the sharing tab's.

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (u *ui) buildSmartThingsTab() fyne.CanvasObject {
	u.networkBox = container.NewVBox(widget.NewLabel(u.t("network.loading")))
	// One button for the whole tab: the SmartThings hub state and the WoL
	// adapter list are both re-read (the tab has no polling loop of its own,
	// so it also refreshes whenever it is shown — see setCurTab).
	refreshBtn := widget.NewButtonWithIcon(u.t("network.refresh"), theme.ViewRefreshIcon(), u.refreshNetwork)
	stBody := u.buildSTSection()

	// Save lives in the fixed footer (savebar.go), enabled only while the
	// SmartThings section differs from the baseline.
	ft := u.forms.register(u.stForm(tabSmartThings))
	ft.bar = newSaveBar(u, func() { u.saveTab(ft) })

	u.stRoot = container.NewVBox(
		container.NewHBox(layout.NewSpacer(), refreshBtn),
		section(u.t("st.section"), stBody),
		widget.NewSeparator(),
		section(u.t("network.wol.section"), u.networkBox),
		// Trailing padding so the last row never sits flush against the
		// footer (same as the other form tabs).
		widget.NewLabel(""),
	)
	u.refreshNetwork()
	return withSaveBar(u.stRoot, ft.bar)
}

// refreshNetwork re-reads both halves of the SmartThings tab off the UI
// thread.
func (u *ui) refreshNetwork() {
	background(u.loadNetwork)
	background(u.loadSTHub)
}

func (u *ui) loadNetwork() {
	s, err := u.client.GetWoLStatus()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() {
		if u.networkBox == nil {
			return // not built yet (a minimized start)
		}
		// Every free-text line wraps: adapter names, warnings and especially
		// IPv6 address lists would otherwise set the window's minimum width
		// once they load (a few seconds after start) and make it jump.
		wrapped := func(text string) *widget.Label {
			l := widget.NewLabel(text)
			l.Wrapping = fyne.TextWrapWord
			return l
		}
		// The adapter list is the last section of the tab root, so it has to
		// re-lay out the parent once it grows (like fillSvcBox, #53).
		defer func() {
			if u.stRoot != nil {
				u.stRoot.Refresh()
			}
		}()
		u.networkBox.RemoveAll()
		if s.Error != "" {
			u.networkBox.Add(wrapped(s.Error))
			return
		}
		// The summary is about the adapter WoL will actually use (#96), and
		// names it, rather than saying "some adapter has WoL on" — which is
		// no help on a PC with an Ethernet port, Wi-Fi and three pseudo
		// adapters. The SmartThings section below polls that choice; until
		// it has, fall back to the old wording.
		ready, summary := s.Ready, "network.wolready"
		if !ready {
			summary = "network.wolnotready"
		}
		line := u.t(summary)
		if u.st != nil && u.st.wolLoaded {
			sel := stWoLPick(u.st.hub.WoL, u.st.wolMAC)
			ready = sel != nil && sel.WoLEnabled
			line = stWoLLine(u.lang, sel)
		}
		mark := "✗ "
		if ready {
			mark = "✓ "
		}
		u.networkBox.Add(widget.NewLabelWithStyle(mark+line, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		if s.Warning != "" {
			u.networkBox.Add(wrapped(s.Warning))
		}
		if s.ExternalIP != "" {
			u.networkBox.Add(wrapped(u.t("network.externalip") + ": " + s.ExternalIP))
		}
		u.networkBox.Add(widget.NewSeparator())
		for _, ad := range s.Adapters {
			state := u.t("network.adapter.down")
			if ad.Status == "Up" {
				state = u.t("network.adapter.up")
			}
			wol := u.t("network.wol.off")
			if ad.WoLEnabled {
				wol = u.t("network.wol.on")
			}
			title := fmt.Sprintf("%s — %s / %s", ad.Name, state, wol)
			detail := "MAC: " + ad.MacAddress
			if len(ad.IPs) > 0 {
				detail += "\nIP: " + strings.Join(ad.IPs, ", ")
			}
			head := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			head.Wrapping = fyne.TextWrapWord
			u.networkBox.Add(container.NewVBox(head, wrapped(detail)))
			u.networkBox.Add(widget.NewSeparator())
		}
	})
}
