package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Every tab title stays on the tab bar, in both languages, at the window's
// opening width: none folds into AppTabs' overflow menu. Measured with the
// app's own theme (Malgun Gothic), so the test needs Windows' fonts.
func TestTabBarFitsTheWindow(t *testing.T) {
	if _, err := os.Stat(filepath.Join(os.Getenv("WINDIR"), "Fonts", "malgunbd.ttf")); err != nil {
		t.Skip("Malgun Gothic not installed:", err)
	}
	a := test.NewTempApp(t)
	th := newKoreanTheme()
	a.Settings().SetTheme(th)
	pad := th.Size(theme.SizeNamePadding)
	// The window pads its content by one theme padding on each side.
	avail := windowWidth - 2*pad
	for _, lang := range languages {
		t.Run(string(lang), func(t *testing.T) {
			// What a title-only tab button takes (Fyne's tabButtonRenderer:
			// bold text plus inner padding each side), with HBox padding
			// between the buttons.
			var want float32
			items := make([]*container.TabItem, len(tabKeys))
			for i, key := range tabKeys {
				title := T(lang, key)
				want += fyne.MeasureText(title, th.Size(theme.SizeNameText), fyne.TextStyle{Bold: true}).Width +
					2*th.Size(theme.SizeNameInnerPadding)
				items[i] = container.NewTabItem(title, widget.NewLabel(""))
			}
			want += float32(len(tabKeys)-1) * pad
			t.Logf("%d tabs: %.1f px of %.0f", len(tabKeys), want, avail)
			if want > avail {
				t.Errorf("the tab titles need %.1f px, the window has %.0f", want, avail)
			}

			// And Fyne agrees: laid out at that width, the overflow button
			// stays hidden.
			tabs := container.NewAppTabs(items...)
			r := test.TempWidgetRenderer(t, tabs)
			tabs.Resize(fyne.NewSize(avail, 400))
			r.Layout(tabs.Size())
			bar, ok := r.Objects()[0].(*fyne.Container)
			if !ok || len(bar.Objects) != 2 {
				t.Fatalf("unexpected AppTabs renderer: %T", r.Objects()[0])
			}
			if buttons := bar.Objects[0].(*fyne.Container).Objects; len(buttons) != len(tabKeys) || bar.Objects[1].Visible() {
				t.Errorf("%d of %d tabs on the bar, overflow shown %v", len(buttons), len(tabKeys), bar.Objects[1].Visible())
			}
		})
	}
}

// The About tab needs no service: it stays enabled while the service is
// away, and whoever is reading it is not sent to the settings tab.
func TestAboutTabStaysOpenWithoutTheService(t *testing.T) {
	u := newTestUI(t, LangKo, nil) // never connected
	if u.tabs.Items[tabAbout].Disabled() {
		t.Fatal("the About tab is disabled without the service")
	}
	if u.tabs.Items[tabAbout].Text != "정보" {
		t.Errorf("About tab title %q", u.tabs.Items[tabAbout].Text)
	}
	u.tabs.SelectIndex(tabAbout)
	if u.curTab != tabAbout {
		t.Fatalf("on tab %d, want About", u.curTab)
	}
	u.applyConnected(false)
	if u.curTab != tabAbout || u.tabs.SelectedIndex() != tabAbout {
		t.Errorf("service lost: moved to tab %d", u.tabs.SelectedIndex())
	}
	u.connected.Store(true)
	u.applyConnected(true)
	if u.tabs.SelectedIndex() != tabAbout {
		t.Errorf("service back: moved to tab %d", u.tabs.SelectedIndex())
	}
}

// Every module go.mod requires directly is named, with its licence, on
// the About tab.
func TestOSSNoticesCoverGoMod(t *testing.T) {
	data, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, n := range ossNotices {
		named[n.module] = true
	}
	inBlock, direct := false, 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
			continue
		case line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !inBlock:
			continue
		}
		if line == "" || strings.Contains(line, "// indirect") {
			continue
		}
		module := strings.Fields(line)[0]
		direct++
		if !named[module] {
			t.Errorf("%s: required by go.mod, missing from ossNotices", module)
		}
	}
	if direct == 0 {
		t.Fatal("no direct requirements found in go.mod")
	}
}

func TestVersionKindKey(t *testing.T) {
	for v, want := range map[string]string{
		"v1.3.0":       "about.build.release",
		"v1.3.0-rc1":   "about.build.pre",
		"dev":          "about.build.dev",
		"ci-0123abcd":  "about.build.dev",
		"":             "about.build.dev",
		" v1.2.2 ":     "about.build.release",
		"v1.3.0-beta2": "about.build.pre",
	} {
		if got := versionKindKey(v); got != want {
			t.Errorf("versionKindKey(%q) = %s, want %s", v, got, want)
		}
	}
}
