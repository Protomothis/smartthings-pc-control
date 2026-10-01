package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fyne.io/fyne/v2/test"
)

// newTestUI builds the whole window once with Fyne's in-memory test app and
// background work made synchronous. client may be nil (never contacted).
func newTestUI(t *testing.T, lang Lang, client *Client) *ui {
	t.Helper()
	syncBackground = true
	t.Cleanup(func() { syncBackground = false })
	a := test.NewTempApp(t)
	if client == nil {
		client = NewClient(1)
	}
	u := &ui{app: a, client: client, version: "test", quit: make(chan struct{}), lang: lang}
	u.win = a.NewWindow(windowTitle)
	t.Cleanup(u.win.Close)
	u.rebuild()
	return u
}

// pollLoop decides on the logs refresh off the UI goroutine, so it reads a
// mirror of the auto-refresh check rather than the widget (refactor-plan
// 1-5).
func TestLogsAutoRefreshFlagFollowsTheCheck(t *testing.T) {
	u := newTestUI(t, LangEn, nil)
	if !u.logsAutoOn.Load() {
		t.Fatal("auto refresh is on by default")
	}
	u.logsAuto.SetChecked(false)
	if u.logsAutoOn.Load() {
		t.Error("unchecking auto refresh left the poll flag on")
	}
	u.logsAuto.SetChecked(true)
	if !u.logsAutoOn.Load() {
		t.Error("checking auto refresh left the poll flag off")
	}
}

// sendAwake runs off the UI goroutine, so the duration comes in as an
// argument instead of being read from the select there (refactor-plan 1-5).
func TestSendAwakePostsTheMinutesPassedIn(t *testing.T) {
	var got struct {
		Minutes int `json:"minutes"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"on":true}`))
	}))
	defer srv.Close()
	test.NewTempApp(t)
	u := &ui{client: &Client{base: srv.URL, http: srv.Client()}}
	u.sendAwake(true, 120)
	if got.Minutes != 120 {
		t.Errorf("posted minutes = %d, want 120", got.Minutes)
	}
}
