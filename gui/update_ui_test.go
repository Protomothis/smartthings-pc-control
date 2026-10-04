package gui

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// fakeReleases is a GitHub release list as release.FetchWithList decodes
// it: newest first, with an Edge driver release and an rc mixed in.
func fakeReleases(newest string) []release.Info {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local) }
	return []release.Info{
		{TagName: newest, HTMLURL: "https://x/" + newest, PublishedAt: day(4),
			Body: "## [" + newest + "] - 2026-10-04\r\n\r\n### 추가\r\n\r\n- **새 기능** `config.json`\r\n"},
		{TagName: "edge-v1.1.0", Body: "driver notes"},
		{TagName: "v1.2.0-rc1", Prerelease: true, Body: "rc notes"},
		{TagName: "v1.1.0", PublishedAt: day(2)}, // no notes
		{TagName: "v1.0.0", Body: "## [v1.0.0]\n\nfirst release"},
	}
}

// stubUpdates makes checkLatestRelease answer with list and fetchManifest
// with m/merr, so no test touches the network.
func stubUpdates(t *testing.T, list []release.Info, m *release.Manifest, merr error) {
	t.Helper()
	savedCheck, savedManifest := checkLatestRelease, fetchManifest
	t.Cleanup(func() { checkLatestRelease, fetchManifest = savedCheck, savedManifest })
	checkLatestRelease = func() (updateCheck, error) {
		return updateCheck{rel: release.Newest(list), list: list}, nil
	}
	fetchManifest = func(*release.Info) (*release.Manifest, error) { return m, merr }
}

// newUpdateUI is newTestUI for an installed release build.
func newUpdateUI(t *testing.T, version string) *ui {
	u := newTestUI(t, LangEn, nil)
	u.version = version
	return u
}

// shownDialog is the update dialog on top of the window, nil when none.
func shownDialog(u *ui) fyne.CanvasObject {
	return u.win.Canvas().Overlays().Top()
}

// dialogText is every label and rich text in o, in layout order.
func dialogText(o fyne.CanvasObject) []string {
	var out []string
	for _, obj := range test.LaidOutObjects(o) {
		switch w := obj.(type) {
		case *widget.Label:
			out = append(out, w.Text)
		case *widget.RichText:
			out = append(out, segmentText(w.Segments))
		case *widget.Hyperlink:
			out = append(out, w.Text)
		}
	}
	return out
}

// segmentText is the text of segs including list items, which
// RichText.String leaves out.
func segmentText(segs []widget.RichTextSegment) string {
	var b strings.Builder
	for _, s := range segs {
		switch s := s.(type) {
		case *widget.ListSegment:
			b.WriteString(segmentText(s.Items))
		case *widget.ParagraphSegment:
			b.WriteString(segmentText(s.Texts))
		default:
			b.WriteString(s.Textual())
		}
	}
	return b.String()
}

// dialogButtons is the buttons in o by label, in layout order.
func dialogButtons(o fyne.CanvasObject) (labels []string, byLabel map[string]*widget.Button) {
	byLabel = map[string]*widget.Button{}
	for _, obj := range test.LaidOutObjects(o) {
		if b, ok := obj.(*widget.Button); ok {
			labels = append(labels, b.Text)
			byLabel[b.Text] = b
		}
	}
	return labels, byLabel
}

// indexOf is the first entry of texts that contains s, or -1.
func indexOf(texts []string, s string) int {
	return slices.IndexFunc(texts, func(x string) bool { return strings.Contains(x, s) })
}

// #135: the startup dialog lists the notes of every version since the
// installed one, newest first, without the CHANGELOG's own version
// heading; a release without notes says so.
func TestUpdateDialogShowsReleaseNotes(t *testing.T) {
	stubUpdates(t, fakeReleases("v1.2.0"), nil, release.ErrNoManifest)
	u := newUpdateUI(t, "v1.0.0")
	u.checkForUpdates(true)
	d := shownDialog(u)
	if d == nil {
		t.Fatal("no update dialog at startup")
	}
	texts := dialogText(d)
	newest, older := indexOf(texts, "v1.2.0 · 2026-10-04"), indexOf(texts, "v1.1.0 · 2026-10-02")
	if newest < 0 || older < 0 || newest > older {
		t.Fatalf("version headings missing or out of order (%d, %d): %q", newest, older, texts)
	}
	body := indexOf(texts, "새 기능")
	if body != newest+1 || !strings.Contains(texts[body], "추가") || !strings.Contains(texts[body], "config.json") {
		t.Errorf("v1.2.0 notes not rendered under its heading: %q", texts)
	}
	if indexOf(texts, "[v1.2.0]") >= 0 {
		t.Errorf("the CHANGELOG version heading was kept: %q", texts)
	}
	if i := indexOf(texts, "No release notes."); i != older+1 {
		t.Errorf("v1.1.0 without notes: %q", texts)
	}
	for _, gone := range []string{"first release", "driver notes", "rc notes"} {
		if indexOf(texts, gone) >= 0 {
			t.Errorf("%q shown: %q", gone, texts)
		}
	}
	// Unsigned release: the page-only variant, with the skip button.
	if labels, _ := dialogButtons(d); !slices.Equal(labels, []string{"Skip this version", "Later", "Open download page"}) {
		t.Errorf("buttons = %q", labels)
	}
	if indexOf(texts, "no signed update manifest") < 0 {
		t.Errorf("unsigned note missing: %q", texts)
	}
}

// The self-update variant has the same notes and "Update now" as the
// primary button.
func TestUpdateDialogSelfUpdateButtons(t *testing.T) {
	list := fakeReleases("v1.2.0")
	list[0].Assets = []release.Asset{{Name: release.AssetName, DownloadURL: "https://x/a.exe"}}
	m := &release.Manifest{Version: "v1.2.0", Assets: []release.ManifestAsset{{Name: release.AssetName, Arch: runtime.GOARCH}}}
	stubUpdates(t, list, m, nil)
	u := newUpdateUI(t, "v1.1.0")
	u.checkForUpdates(true)
	d := shownDialog(u)
	if d == nil {
		t.Fatal("no update dialog at startup")
	}
	labels, buttons := dialogButtons(d)
	if !slices.Equal(labels, []string{"Skip this version", "Later", "Update now"}) {
		t.Fatalf("buttons = %q", labels)
	}
	if buttons["Update now"].Importance != widget.HighImportance {
		t.Error("Update now is not the primary button")
	}
	texts := dialogText(d)
	if indexOf(texts, "v1.2.0 · ") < 0 || indexOf(texts, "v1.1.0 · ") >= 0 {
		t.Errorf("notes since v1.1.0: %q", texts)
	}
	test.Tap(buttons["Later"])
	if shownDialog(u) != nil {
		t.Error("Later left the dialog open")
	}
	if got := u.app.Preferences().String("update_skipped"); got != "" {
		t.Errorf("Later skipped %q", got)
	}
}

// Skipping a version silences the automatic checks for that tag only; a
// newer release prompts again, and the manual check still shows the
// skipped one, offering to stop skipping it.
func TestSkipUpdateVersion(t *testing.T) {
	stubUpdates(t, fakeReleases("v1.2.0"), nil, release.ErrNoManifest)
	u := newUpdateUI(t, "v1.0.0")
	u.checkForUpdates(true)
	_, buttons := dialogButtons(shownDialog(u))
	test.Tap(buttons["Skip this version"])
	if shownDialog(u) != nil {
		t.Fatal("Skip left the dialog open")
	}
	if got := u.app.Preferences().String("update_skipped"); got != "v1.2.0" {
		t.Fatalf("update_skipped = %q, want v1.2.0", got)
	}

	// Startup and periodic checks: no dialog, no notification.
	u.app.Preferences().SetString("update_notified", "")
	for _, startup := range []bool{true, false} {
		test.AssertNotificationSent(t, nil, func() { u.checkForUpdates(startup) })
		if shownDialog(u) != nil {
			t.Fatalf("startup=%v: dialog for the skipped version", startup)
		}
	}
	if got := u.app.Preferences().String("update_notified"); got != "" {
		t.Errorf("skipped version marked notified: %q", got)
	}

	// The manual check shows it anyway, with the skip turned around.
	u.checkForUpdatesManual(widget.NewButton("check", nil))
	d := shownDialog(u)
	if d == nil {
		t.Fatal("manual check hid the skipped version")
	}
	labels, buttons := dialogButtons(d)
	if !slices.Equal(labels, []string{"Stop skipping", "Later", "Open download page"}) {
		t.Errorf("manual check buttons = %q", labels)
	}
	if indexOf(dialogText(d), "skip this version") < 0 {
		t.Errorf("no skipped note: %q", dialogText(d))
	}
	test.Tap(buttons["Later"])
	if got := u.app.Preferences().String("update_skipped"); got != "v1.2.0" {
		t.Errorf("Later in the manual check changed the skip to %q", got)
	}

	// A newer release prompts again, with a plain skip button.
	stubUpdates(t, fakeReleases("v1.3.0"), nil, release.ErrNoManifest)
	want := &fyne.Notification{Title: "Update Available", Content: fmt.Sprintf("Version %s is available (current: %s).", "v1.3.0", "v1.0.0")}
	test.AssertNotificationSent(t, want, func() { u.checkForUpdates(true) })
	d = shownDialog(u)
	if d == nil {
		t.Fatal("no dialog for a release newer than the skipped one")
	}
	labels, buttons = dialogButtons(d)
	if labels[0] != "Skip this version" {
		t.Errorf("buttons = %q", labels)
	}
	test.Tap(buttons["Later"])

	// Stop skipping: the automatic checks offer v1.2.0 again.
	stubUpdates(t, fakeReleases("v1.2.0"), nil, release.ErrNoManifest)
	u.checkForUpdatesManual(widget.NewButton("check", nil))
	_, buttons = dialogButtons(shownDialog(u))
	test.Tap(buttons["Stop skipping"])
	if got := u.app.Preferences().String("update_skipped"); got != "" {
		t.Errorf("Stop skipping left update_skipped = %q", got)
	}
	u.checkForUpdates(true)
	if shownDialog(u) == nil {
		t.Error("no dialog after the skip was lifted")
	}
}

// More than maxNotes versions behind: the newest maxNotes, then a link to
// the releases page.
func TestUpdateNotesCapped(t *testing.T) {
	var list []release.Info
	for i := 20; i >= 1; i-- {
		list = append(list, release.Info{TagName: fmt.Sprintf("v1.%d.0", i), Body: fmt.Sprintf("notes %d", i)})
	}
	stubUpdates(t, list, nil, release.ErrNoManifest)
	u := newUpdateUI(t, "v1.0.0")
	u.checkForUpdates(true)
	texts := dialogText(shownDialog(u))
	if indexOf(texts, "notes 11") < 0 || indexOf(texts, "notes 10") >= 0 {
		t.Errorf("not capped at the newest %d: %q", maxNotes, texts)
	}
	if i := indexOf(texts, "More: release page"); i < indexOf(texts, "notes 11") {
		t.Errorf("no more-link after the notes: %q", texts)
	}
}

// A dev build is offered nothing newer, but the manual check shows the
// newest release's notes with the page-only buttons.
func TestUpdateDialogDevBuildShowsNewestNotes(t *testing.T) {
	stubUpdates(t, fakeReleases("v1.2.0"), nil, nil)
	u := newUpdateUI(t, "dev")
	u.checkForUpdates(true)
	if shownDialog(u) != nil {
		t.Fatal("dev build got a startup dialog")
	}
	u.checkForUpdatesManual(widget.NewButton("check", nil))
	d := shownDialog(u)
	if d == nil {
		t.Fatal("manual check showed nothing")
	}
	texts := dialogText(d)
	if indexOf(texts, "v1.2.0 · ") < 0 || indexOf(texts, "새 기능") < 0 || indexOf(texts, "v1.1.0 · ") >= 0 {
		t.Errorf("dev build notes: %q", texts)
	}
	if labels, _ := dialogButtons(d); !slices.Equal(labels, []string{"Skip this version", "Later", "Open download page"}) {
		t.Errorf("buttons = %q", labels)
	}
}

// Fyne 2.8.1 draws # and ## larger than the version heading; the notes
// keep every heading at ### (bold body text), code blocks untouched.
func TestCompactHeadings(t *testing.T) {
	in := "# 큰 제목\n## 중간\n### 추가\n#### 작은\n#태그 아님\n   ## 3칸 들여쓰기\n    # indented code\n```\n# shell comment\n```\n##"
	want := "### 큰 제목\n### 중간\n### 추가\n#### 작은\n#태그 아님\n### 3칸 들여쓰기\n    # indented code\n```\n# shell comment\n```\n###"
	if got := compactHeadings(in); got != want {
		t.Errorf("compactHeadings =\n%s\nwant\n%s", got, want)
	}
	// Rendered: the demoted heading is bold text at body size.
	rt := widget.NewRichTextFromMarkdown(compactHeadings("## 변경\n\n- x"))
	seg, ok := rt.Segments[0].(*widget.TextSegment)
	if !ok || seg.Text != "변경" || !seg.Style.TextStyle.Bold || seg.Style.SizeName != widget.RichTextStyleStrong.SizeName {
		t.Errorf("first segment = %#v", rt.Segments[0])
	}
}
