package ui

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// TestShotsSmartThings draws the SmartThings page (#165 2b): configured,
// with nothing set up, with errors, and edited with the adapter list open
// — ko light and dark at 480 and 1200dp, en at 480.
//
//	STPC_SHOTS_DIR=C:\tmp\shots go test -run TestShotsSmartThings ./ui/
func TestShotsSmartThings(t *testing.T) {
	dir := os.Getenv("STPC_SHOTS_DIR")
	if dir == "" {
		t.Skip("set STPC_SHOTS_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []gui.Lang{gui.LangKo, gui.LangEn} {
		for _, dark := range []bool{false, true} {
			sfx := string(lang)
			if dark {
				sfx += "-dark"
			}
			for _, w := range []int{480, 1200} {
				if lang == gui.LangEn && w == 1200 {
					continue
				}
				name := func(what string) string { return fmt.Sprintf("st-%s-%d-%s", what, w, sfx) }
				const tall = 2000

				u := stSampleUI(lang, dark)
				p := u.pages[pageSmartThings].(*stPage)
				u.open(pageSmartThings)
				u.runTasks()
				p.net = stSampleNet()
				shotW(t, dir, name("configured"), w, tall, u.layout)
				shotW(t, dir, name("window"), w, winH, u.layout)

				// edited: a hub removed, another adapter picked, the
				// adapter list open (the save bar shows)
				p.form.Hubs = nil
				p.form.WoLMAC = "11-22-33-44-55-66"
				p.adapter.open = true
				shotW(t, dir, name("edited"), w, tall, u.layout)
				u.nav.stack = nil

				// nothing set up: no hub, no secret, no list, loading
				u = stSampleUI(lang, dark)
				u.update(func(s *snapshot) {
					s.hub = gui.STHub{MachineID: s.hub.MachineID, SSDP: gui.STSSDPState{Running: true, FirewallRule: true}}
					s.cfg.Secret = ""
					s.cfg.SmartThings.AllowedHubs = nil
					s.cfgGen++
				})
				u.syncForms(u.current())
				u.open(pageSmartThings)
				u.runTasks()
				u.pages[pageSmartThings].(*stPage).net = stNet{}
				shotW(t, dir, name("empty"), w, tall, u.layout)
				u.nav.stack = nil

				// errors: no responder socket, no command port rule with a
				// port waiting for a restart, the adapter list failed, a
				// saved adapter that is gone
				u = stSampleUI(lang, dark)
				u.update(func(s *snapshot) {
					s.hub.SSDP = gui.STSSDPState{}
					s.hub.CommandPort = &gui.STCommandPort{Port: 5001, Checked: true}
					s.cfg.Port = 5011
					s.cfg.SmartThings.WoLMAC = "DE-AD-BE-EF-00-01"
					s.cfgGen++
				})
				u.syncForms(u.current())
				u.open(pageSmartThings)
				u.runTasks()
				u.pages[pageSmartThings].(*stPage).net = stNet{set: true, err: errors.New("HTTP 500")}
				shotW(t, dir, name("error"), w, tall, u.layout)
				u.nav.stack = nil
			}
		}
	}
	usePalette(false)
}

// stSampleUI is sampleUI with a full SmartThings picture.
func stSampleUI(lang gui.Lang, dark bool) *UI {
	u := sampleUI(lang, dark)
	u.update(func(s *snapshot) {
		s.cfg.Secret = "s3cret"
		s.cfg.SmartThings.AllowedHubs = []string{"192.168.1.20", "192.168.1.21"}
		eth := &gui.STWoLSelected{Name: "이더넷", MAC: "B4-2E-99-45-B4-F5", IP: "192.168.1.10", WoLEnabled: true, WoLCapable: true, Source: "auto"}
		s.hub = gui.STHub{
			Connected: true, IP: "192.168.1.20", DriverVersion: "1.4.2",
			LastSeen:  time.Now().Add(-8 * time.Second).Format(time.RFC3339),
			MachineID: "58bff996-6d9a-4a2f-8e1c-0123456789ab",
			SSDP: gui.STSSDPState{Running: true, FirewallRule: true,
				LastSearch: &gui.STLastSearch{IP: "192.168.1.20", At: time.Now().Add(-3 * time.Minute).Format(time.RFC3339)}},
			WoL: gui.STWoLInfo{Selected: eth, Auto: eth, Adapters: []gui.STWoLAdapter{
				{Name: "이더넷", MAC: "B4-2E-99-45-B4-F5", IP: "192.168.1.10", WoLEnabled: true, WoLCapable: true, Selected: true},
				{Name: "Wi-Fi", MAC: "11-22-33-44-55-66", IP: "192.168.1.11", WoLCapable: true},
				{Name: "vEthernet (WSL)", MAC: "00-15-5D-01-02-03"},
			}},
			CommandPort: &gui.STCommandPort{Port: 5001, FirewallRule: true, Checked: true},
		}
		s.cfgGen++
	})
	u.syncForms(u.current())
	return u
}

func stSampleNet() stNet {
	return stNet{set: true, status: gui.WoLStatus{
		ExternalIP: "203.0.113.7",
		Ready:      true,
		Adapters: []gui.WoLAdapter{
			{Name: "이더넷", MacAddress: "B4-2E-99-45-B4-F5", IPs: []string{"192.168.1.10", "fe80::1c2d:3e4f:5a6b:7c8d"}, Status: "Up", WoLEnabled: true, WoLCapable: true},
			{Name: "Wi-Fi", MacAddress: "11-22-33-44-55-66", IPs: []string{"192.168.1.11"}, Status: "Up", WoLCapable: true},
			{Name: "vEthernet (WSL)", MacAddress: "00-15-5D-01-02-03", Status: "Disconnected"},
		},
	}}
}
