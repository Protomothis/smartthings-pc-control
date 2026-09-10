package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:ABC-DEF_secret-token"

// capture records the last request the fake Bot API saw.
type capture struct {
	method string
	path   string
	ctype  string
	body   map[string]any
}

// newFakeAPI starts an httptest.Server that answers every call with reply
// (status + JSON) and returns a Client pointed at it plus the capture.
func newFakeAPI(t *testing.T, status int, reply string) (*Client, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.ctype = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		cap.body = map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &cap.body); err != nil {
				t.Errorf("request body is not JSON: %v (%s)", err, raw)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return NewClient(testToken, WithBaseURL(srv.URL), WithHTTPClient(srv.Client())), cap
}

func TestSendMessagePayload(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":77,"chat":{"id":42,"type":"private"}}}`)
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		{Text: "바로 실행", CallbackData: "runnow:"},
		{Text: "취소", CallbackData: "cancel:"},
	}}}
	id, err := cli.SendMessage(context.Background(), "42", "<b>hi</b> &amp; bye", kb)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id != 77 {
		t.Errorf("message id = %d, want 77", id)
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %s, want POST", cap.method)
	}
	if want := "/bot" + testToken + "/sendMessage"; cap.path != want {
		t.Errorf("path = %s, want %s", cap.path, want)
	}
	if !strings.HasPrefix(cap.ctype, "application/json") {
		t.Errorf("content-type = %q", cap.ctype)
	}
	if cap.body["chat_id"] != "42" {
		t.Errorf("chat_id = %v", cap.body["chat_id"])
	}
	if cap.body["text"] != "<b>hi</b> &amp; bye" {
		t.Errorf("text = %v", cap.body["text"])
	}
	if cap.body["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", cap.body["parse_mode"])
	}
	if cap.body["disable_web_page_preview"] != true {
		t.Errorf("disable_web_page_preview = %v, want true", cap.body["disable_web_page_preview"])
	}
	rm, ok := cap.body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong type: %v", cap.body["reply_markup"])
	}
	rows, _ := rm["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("inline_keyboard rows = %d, want 1", len(rows))
	}
	row, _ := rows[0].([]any)
	if len(row) != 2 {
		t.Fatalf("buttons in row = %d, want 2", len(row))
	}
	b0, _ := row[0].(map[string]any)
	if b0["text"] != "바로 실행" || b0["callback_data"] != "runnow:" {
		t.Errorf("button[0] = %v", b0)
	}
	if _, has := b0["url"]; has {
		t.Errorf("button[0] should omit empty url: %v", b0)
	}
}

func TestSendMessageWithoutKeyboardOmitsReplyMarkup(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	if _, err := cli.SendMessage(context.Background(), "42", "x", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, has := cap.body["reply_markup"]; has {
		t.Errorf("reply_markup should be omitted when kb is nil: %v", cap.body)
	}
}

func TestRateLimitMapping(t *testing.T) {
	cli, _ := newFakeAPI(t, 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`)
	_, err := cli.SendMessage(context.Background(), "42", "x", nil)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want *RateLimitError, got %T: %v", err, err)
	}
	if rl.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %s, want 7s", rl.RetryAfter)
	}
	if d, ok := RetryAfterOf(err); !ok || d != 7*time.Second {
		t.Errorf("RetryAfterOf = %s,%v", d, ok)
	}
	if !strings.Contains(rl.Description, "Too Many Requests") {
		t.Errorf("description = %q", rl.Description)
	}
}

func TestRateLimitWithoutParametersDefaultsToOneSecond(t *testing.T) {
	cli, _ := newFakeAPI(t, 429, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`)
	_, err := cli.GetMe(context.Background())
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want *RateLimitError, got %T: %v", err, err)
	}
	if rl.RetryAfter != time.Second {
		t.Errorf("RetryAfter = %s, want 1s", rl.RetryAfter)
	}
}

func TestAPIErrorMapping(t *testing.T) {
	cli, _ := newFakeAPI(t, 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	_, err := cli.SendMessage(context.Background(), "42", "x", nil)
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if ae.Code != 400 || ae.Description != "Bad Request: chat not found" {
		t.Errorf("APIError = %+v", *ae)
	}
	if _, ok := RetryAfterOf(err); ok {
		t.Errorf("RetryAfterOf should be false for APIError")
	}
}

func TestNonJSONResponseBecomesAPIError(t *testing.T) {
	cli, _ := newFakeAPI(t, 502, `<html>bad gateway</html>`)
	_, err := cli.GetMe(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if ae.Code != 502 {
		t.Errorf("code = %d", ae.Code)
	}
}

func TestTokenNeverAppearsInErrorsOrLogs(t *testing.T) {
	// A server that is already closed produces a transport error whose
	// *url.Error would normally embed the full URL, token included.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	var logs []string
	cli := NewClient(testToken, WithBaseURL(url))
	cli.Log = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	_, err := cli.SendMessage(context.Background(), "42", "x", nil)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "sendMessage") {
		t.Errorf("error should name the method: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l, testToken) {
			t.Errorf("token leaked into log: %s", l)
		}
	}
	if len(logs) == 0 {
		t.Errorf("expected a debug log line")
	}

	// Context cancellation must still be detectable through the masked chain.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli2, _ := newFakeAPI(t, 200, `{"ok":true,"result":{}}`)
	_, err = cli2.GetMe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("token leaked into error: %v", err)
	}
}

func TestGetUpdatesParams(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":[
		{"update_id":100,"message":{"message_id":5,"from":{"id":9,"is_bot":false,"first_name":"K","username":"kim"},"chat":{"id":42,"type":"private","username":"kim"},"date":1,"text":"/status"}},
		{"update_id":101,"callback_query":{"id":"cbq1","from":{"id":9,"first_name":"K"},"message":{"message_id":6,"chat":{"id":42,"type":"private"}},"data":"cancel:"}}
	]}`)
	ups, err := cli.GetUpdates(context.Background(), 15, 25)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if want := "/bot" + testToken + "/getUpdates"; cap.path != want {
		t.Errorf("path = %s, want %s", cap.path, want)
	}
	if cap.body["offset"] != float64(15) {
		t.Errorf("offset = %v, want 15", cap.body["offset"])
	}
	if cap.body["timeout"] != float64(25) {
		t.Errorf("timeout = %v, want 25", cap.body["timeout"])
	}
	au, _ := cap.body["allowed_updates"].([]any)
	if len(au) != 2 || au[0] != "message" || au[1] != "callback_query" {
		t.Errorf("allowed_updates = %v", cap.body["allowed_updates"])
	}
	if len(ups) != 2 {
		t.Fatalf("updates = %d, want 2", len(ups))
	}
	if ups[0].UpdateID != 100 || ups[0].Message == nil || ups[0].Message.Text != "/status" || ups[0].Message.Chat.IDString() != "42" {
		t.Errorf("update[0] = %+v", ups[0])
	}
	if ups[1].CallbackQuery == nil || ups[1].CallbackQuery.Data != "cancel:" || ups[1].CallbackQuery.Message.MessageID != 6 {
		t.Errorf("update[1] = %+v", ups[1])
	}
}

func TestGetMe(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"id":7,"is_bot":true,"first_name":"PC Bot","username":"pc_control_bot"}}`)
	u, err := cli.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if !strings.HasSuffix(cap.path, "/getMe") {
		t.Errorf("path = %s", cap.path)
	}
	if u.Username != "pc_control_bot" || !u.IsBot || u.ID != 7 {
		t.Errorf("user = %+v", u)
	}
}

func TestEditMessageTextPayload(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":77}}`)
	if err := cli.EditMessageText(context.Background(), "42", 77, "<b>done</b>", nil); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	if !strings.HasSuffix(cap.path, "/editMessageText") {
		t.Errorf("path = %s", cap.path)
	}
	if cap.body["chat_id"] != "42" || cap.body["message_id"] != float64(77) || cap.body["text"] != "<b>done</b>" {
		t.Errorf("body = %v", cap.body)
	}
	if cap.body["parse_mode"] != "HTML" || cap.body["disable_web_page_preview"] != true {
		t.Errorf("body = %v", cap.body)
	}
	if _, has := cap.body["reply_markup"]; has {
		t.Errorf("nil kb must omit reply_markup so Telegram drops the keyboard: %v", cap.body)
	}
}

func TestAnswerCallbackQueryPayload(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":true}`)
	if err := cli.AnswerCallbackQuery(context.Background(), "cbq1", "취소됨"); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if !strings.HasSuffix(cap.path, "/answerCallbackQuery") {
		t.Errorf("path = %s", cap.path)
	}
	if cap.body["callback_query_id"] != "cbq1" || cap.body["text"] != "취소됨" {
		t.Errorf("body = %v", cap.body)
	}
}

func TestSetMyCommandsPayload(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":true}`)
	cmds := []BotCommand{{Command: "status", Description: "상태"}, {Command: "menu", Description: "메뉴"}}
	if err := cli.SetMyCommands(context.Background(), cmds); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	if !strings.HasSuffix(cap.path, "/setMyCommands") {
		t.Errorf("path = %s", cap.path)
	}
	list, _ := cap.body["commands"].([]any)
	if len(list) != 2 {
		t.Fatalf("commands = %v", cap.body["commands"])
	}
	c0, _ := list[0].(map[string]any)
	if c0["command"] != "status" || c0["description"] != "상태" {
		t.Errorf("commands[0] = %v", c0)
	}
}

func TestWithBaseURLTrimsSlash(t *testing.T) {
	c := NewClient("t", WithBaseURL("http://example.test/"))
	if got := c.methodURL("getMe"); got != "http://example.test/bott/getMe" {
		t.Errorf("methodURL = %s", got)
	}
	d := NewClient("t")
	if got := d.methodURL("getMe"); got != DefaultBaseURL+"/bott/getMe" {
		t.Errorf("default methodURL = %s", got)
	}
}
