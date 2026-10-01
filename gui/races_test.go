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

// sendAwake reads the duration on the UI goroutine and only the request
// runs in the background (refactor-plan 1-5); the row is busy meanwhile
// and usable again once the answer is applied.
func TestSendAwakePostsTheSelectedMinutes(t *testing.T) {
	posted := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/awake" {
			var body struct {
				Minutes int `json:"minutes"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			posted <- body.Minutes
			w.Write([]byte(`{"on":true}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	u := newTestUI(t, LangEn, &Client{base: srv.URL, http: srv.Client()})
	u.awake.sel.SetSelectedIndex(awakePresetIndex(120))
	u.sendAwake(true)
	if got := <-posted; got != 120 {
		t.Errorf("posted minutes = %d, want 120", got)
	}
	if u.awake.toggle.Disabled() || u.awake.sel.Disabled() {
		t.Error("the row stayed busy after the answer")
	}
	if !u.awake.toggle.Checked {
		t.Error("the answer (on) was not applied to the toggle")
	}
}
