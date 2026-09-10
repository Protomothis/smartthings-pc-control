package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedReply is one getUpdates response the fake Bot API hands out.
type scriptedReply struct {
	status int
	body   string
}

func okUpdates(updates string) scriptedReply {
	return scriptedReply{200, `{"ok":true,"result":[` + updates + `]}`}
}

// apiCall is a captured non-getUpdates request.
type apiCall struct {
	method string
	body   map[string]any
}

// fakeBot serves getUpdates from a script (blocking once the script is
// exhausted, like a real long poll) and records every other call.
type fakeBot struct {
	mu     sync.Mutex
	script []scriptedReply
	polls  chan map[string]any // every getUpdates request body
	calls  chan apiCall        // every other request
}

func newFakeBot(t *testing.T, script ...scriptedReply) (*Client, *fakeBot) {
	t.Helper()
	fb := &fakeBot{script: script, polls: make(chan map[string]any, 64), calls: make(chan apiCall, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not JSON: %v (%s)", err, raw)
			}
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		if method != "getUpdates" {
			fb.calls <- apiCall{method, body}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
			return
		}
		fb.polls <- body
		fb.mu.Lock()
		var reply *scriptedReply
		if len(fb.script) > 0 {
			reply = &fb.script[0]
			fb.script = fb.script[1:]
		}
		fb.mu.Unlock()
		if reply == nil {
			// Script exhausted: behave like an idle long poll.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(reply.status)
		fmt.Fprint(w, reply.body)
	}))
	t.Cleanup(srv.Close)
	return NewClient(testToken, WithBaseURL(srv.URL), WithHTTPClient(srv.Client())), fb
}

func (fb *fakeBot) nextPoll(t *testing.T) map[string]any {
	t.Helper()
	select {
	case p := <-fb.polls:
		return p
	case <-time.After(3 * time.Second):
		t.Fatal("no getUpdates request arrived")
		return nil
	}
}

func (fb *fakeBot) nextCall(t *testing.T, method string) apiCall {
	t.Helper()
	select {
	case c := <-fb.calls:
		if c.method != method {
			t.Fatalf("got %s call, want %s (%v)", c.method, method, c.body)
		}
		return c
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s call arrived", method)
		return apiCall{}
	}
}

func (fb *fakeBot) expectNoCall(t *testing.T) {
	t.Helper()
	select {
	case c := <-fb.calls:
		t.Fatalf("unexpected %s call: %v", c.method, c.body)
	case <-time.After(100 * time.Millisecond):
	}
}

// fakeHandler records what the poller routed to it.
type fakeHandler struct {
	mu        sync.Mutex
	commands  []string // "chat cmd args..."
	callbacks []string // "chat msgID data|msgText"
	unauth    []string // "chat user text"
	withKB    bool     // implement EditKeyboarder behaviour for confirm:
}

func (h *fakeHandler) HandleCommand(_ context.Context, chatID, cmd string, args []string) (string, *InlineKeyboard, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, strings.TrimSpace(chatID+" "+cmd+" "+strings.Join(args, " ")))
	var kb *InlineKeyboard
	if cmd == "shutdown" {
		kb = &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "확인", CallbackData: "exec:shutdown"}}}}
	}
	return "reply:" + cmd, kb, nil
}

func (h *fakeHandler) HandleCallback(_ context.Context, chatID string, msgID int, msgText string, data string) (string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, fmt.Sprintf("%s %d %s|%s", chatID, msgID, data, msgText))
	if strings.HasPrefix(data, "toastonly:") {
		return "", "toast:" + data, nil
	}
	return "edited:" + data, "toast:" + data, nil
}

func (h *fakeHandler) Unauthorized(chatID, username, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unauth = append(h.unauth, chatID+" "+username+" "+text)
}

// kbHandler adds EditKeyboarder on top of fakeHandler.
type kbHandler struct{ *fakeHandler }

func (h kbHandler) EditKeyboard(data string) *InlineKeyboard {
	if strings.HasPrefix(data, "confirm:") {
		return &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "ok", CallbackData: "exec:" + strings.TrimPrefix(data, "confirm:")}}}}
	}
	return nil
}

func (h *fakeHandler) snapshot() (commands, callbacks, unauth []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...), append([]string(nil), h.callbacks...), append([]string(nil), h.unauth...)
}

// runPoller starts p.Run and returns a stop func that cancels and waits.
func runPoller(t *testing.T, p *Poller) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}
	t.Cleanup(func() { stop() })
	return stop
}

func allow(ids ...string) func() []string {
	return func() []string { return ids }
}

func msgUpdate(id int, chat int64, user, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":7,"username":%q},"chat":{"id":%d,"type":"private"},"text":%q}}`, id, id, user, chat, text)
}

func cbUpdate(id int, cbID string, chat int64, msgID int, data string) string {
	return fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":%q,"from":{"id":7,"username":"me"},"message":{"message_id":%d,"chat":{"id":%d,"type":"private"},"text":"msg %d"},"data":%q}}`, id, cbID, msgID, chat, msgID, data)
}

func TestPollerDrainsThenRoutesCommands(t *testing.T) {
	cli, fb := newFakeBot(t,
		// Queued before start: must be discarded, never handled.
		okUpdates(msgUpdate(10, 42, "me", "/shutdown")),
		okUpdates(strings.Join([]string{
			msgUpdate(11, 42, "me", "/lock@stpc_bot now"),
			msgUpdate(12, 99, "mallory", "/shutdown"),
			msgUpdate(13, 42, "me", "hello there"),
			msgUpdate(14, 42, "me", "/Shutdown"),
		}, ",")),
	)
	h := &fakeHandler{}
	p := NewPoller(cli, PollerOptions{AllowedChatIDs: allow("42"), Handler: h, Log: t.Logf})
	stop := runPoller(t, p)

	drain := fb.nextPoll(t)
	if drain["offset"] != float64(-1) || drain["timeout"] != float64(0) {
		t.Errorf("drain request = %v, want offset -1 timeout 0", drain)
	}
	second := fb.nextPoll(t)
	if second["offset"] != float64(11) || second["timeout"] != float64(30) {
		t.Errorf("second request = %v, want offset 11 timeout 30", second)
	}

	// /lock reply, /help reply for plain text, /shutdown reply with keyboard.
	lock := fb.nextCall(t, "sendMessage")
	if lock.body["chat_id"] != "42" || lock.body["text"] != "reply:lock" {
		t.Errorf("lock reply = %v", lock.body)
	}
	if _, has := lock.body["reply_markup"]; has {
		t.Errorf("lock reply should have no keyboard: %v", lock.body)
	}
	help := fb.nextCall(t, "sendMessage")
	if help.body["text"] != "reply:help" {
		t.Errorf("plain text should be answered with /help, got %v", help.body)
	}
	sd := fb.nextCall(t, "sendMessage")
	if sd.body["text"] != "reply:shutdown" || sd.body["reply_markup"] == nil {
		t.Errorf("shutdown reply = %v, want keyboard", sd.body)
	}

	third := fb.nextPoll(t)
	if third["offset"] != float64(15) {
		t.Errorf("offset after batch = %v, want 15", third["offset"])
	}
	fb.expectNoCall(t)

	commands, callbacks, unauth := h.snapshot()
	wantCommands := []string{"42 lock now", "42 help", "42 shutdown"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Errorf("commands = %q, want %q", commands, wantCommands)
	}
	if len(callbacks) != 0 {
		t.Errorf("unexpected callbacks %q", callbacks)
	}
	if want := []string{"99 @mallory /shutdown"}; !reflect.DeepEqual(unauth, want) {
		t.Errorf("unauthorized = %q, want %q", unauth, want)
	}

	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

func TestPollerCallbackAnswersAndEdits(t *testing.T) {
	cli, fb := newFakeBot(t,
		okUpdates(""),
		okUpdates(strings.Join([]string{
			cbUpdate(1, "cb1", 42, 55, "cancel:"),
			cbUpdate(2, "cb2", 42, 56, "confirm:shutdown"),
			cbUpdate(3, "cb3", 42, 57, "toastonly:"),
			cbUpdate(4, "cb4", 99, 58, "exec:shutdown"),
		}, ",")),
	)
	h := &fakeHandler{}
	p := NewPoller(cli, PollerOptions{AllowedChatIDs: allow("42"), Handler: kbHandler{h}, Log: t.Logf})
	runPoller(t, p)

	// cancel: → answer + edit without keyboard
	ans := fb.nextCall(t, "answerCallbackQuery")
	if ans.body["callback_query_id"] != "cb1" || ans.body["text"] != "toast:cancel:" {
		t.Errorf("answer = %v", ans.body)
	}
	edit := fb.nextCall(t, "editMessageText")
	if edit.body["chat_id"] != "42" || edit.body["message_id"] != float64(55) || edit.body["text"] != "edited:cancel:" {
		t.Errorf("edit = %v", edit.body)
	}
	if _, has := edit.body["reply_markup"]; has {
		t.Errorf("edit after cancel: must remove the keyboard, got %v", edit.body["reply_markup"])
	}

	// confirm:shutdown → edit carries the handler's EditKeyboard.
	fb.nextCall(t, "answerCallbackQuery")
	edit = fb.nextCall(t, "editMessageText")
	rm, _ := edit.body["reply_markup"].(map[string]any)
	if edit.body["message_id"] != float64(56) || rm == nil || !strings.Contains(fmt.Sprint(rm), "exec:shutdown") {
		t.Errorf("confirm edit = %v", edit.body)
	}

	// toastonly: → answered, nothing edited; the unauthorized chat's press
	// (cb4) produces no API call at all.
	ans = fb.nextCall(t, "answerCallbackQuery")
	if ans.body["callback_query_id"] != "cb3" {
		t.Errorf("answer = %v", ans.body)
	}
	fb.nextPoll(t) // drain
	fb.nextPoll(t) // batch
	next := fb.nextPoll(t)
	if next["offset"] != float64(5) {
		t.Errorf("offset = %v, want 5", next["offset"])
	}
	fb.expectNoCall(t)

	_, callbacks, unauth := h.snapshot()
	// The message text rides along so handlers can keep it when editing (#62).
	wantCB := []string{"42 55 cancel:|msg 55", "42 56 confirm:shutdown|msg 56", "42 57 toastonly:|msg 57"}
	if !reflect.DeepEqual(callbacks, wantCB) {
		t.Errorf("callbacks = %q, want %q", callbacks, wantCB)
	}
	if want := []string{"99 @me exec:shutdown"}; !reflect.DeepEqual(unauth, want) {
		t.Errorf("unauthorized = %q, want %q", unauth, want)
	}
}

func TestPollerBacksOffOnErrors(t *testing.T) {
	serverErr := scriptedReply{500, `{"ok":false,"error_code":500,"description":"boom"}`}
	rateLimited := scriptedReply{429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`}
	cli, fb := newFakeBot(t,
		okUpdates(""), // drain
		serverErr,     // → 1s
		rateLimited,   // → max(2s, 7s) = 7s
		serverErr,     // → 4s
		okUpdates(""), // resets
		serverErr,     // → 1s again
	)
	p := NewPoller(cli, PollerOptions{AllowedChatIDs: allow("42"), Handler: &fakeHandler{}, Log: t.Logf})
	sleeps := make(chan time.Duration, 16)
	p.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps <- d
		return ctx.Err()
	}
	runPoller(t, p)

	want := []time.Duration{time.Second, 7 * time.Second, 4 * time.Second, time.Second}
	for i, w := range want {
		select {
		case got := <-sleeps:
			if got != w {
				t.Errorf("sleep[%d] = %s, want %s", i, got, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("sleep[%d] never happened", i)
		}
	}
	// 7 polls happened: drain + 5 scripted + the blocking one.
	for i := 0; i < 7; i++ {
		fb.nextPoll(t)
	}
	select {
	case d := <-sleeps:
		t.Errorf("unexpected extra sleep %s", d)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPollerStopsOnCancel(t *testing.T) {
	cli, fb := newFakeBot(t, okUpdates(""))
	p := NewPoller(cli, PollerOptions{AllowedChatIDs: allow("42"), Handler: &fakeHandler{}})
	stop := runPoller(t, p)
	fb.nextPoll(t) // drain
	fb.nextPoll(t) // now blocked in the long poll
	start := time.Now()
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
}

func TestPollerRequiresClientAndHandler(t *testing.T) {
	if err := NewPoller(nil, PollerOptions{}).Run(context.Background()); err == nil {
		t.Error("Run without client/handler should fail")
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		cmd  string
		args []string
	}{
		{"/status", "status", []string{}},
		{"/Shutdown 30", "shutdown", []string{"30"}},
		{"/shutdown@stpc_bot 30", "shutdown", []string{"30"}},
		{"  /mute   2h  ", "mute", []string{"2h"}},
		{"hello", "", nil},
		{"/", "", nil},
		{"", "", nil},
	}
	for _, c := range cases {
		cmd, args := ParseCommand(c.in)
		if cmd != c.cmd || (len(args) != len(c.args)) || (len(args) > 0 && !reflect.DeepEqual(args, c.args)) {
			t.Errorf("ParseCommand(%q) = (%q, %q), want (%q, %q)", c.in, cmd, args, c.cmd, c.args)
		}
	}
}

func TestUserName(t *testing.T) {
	if got := userName(nil); got != "" {
		t.Errorf("nil user = %q", got)
	}
	if got := userName(&User{Username: "bob", FirstName: "Bob"}); got != "@bob" {
		t.Errorf("username = %q", got)
	}
	if got := userName(&User{FirstName: "Bob", LastName: "K"}); got != "Bob K" {
		t.Errorf("name = %q", got)
	}
}
