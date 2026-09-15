package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

func testEvent() notify.Event {
	return notify.Event{
		Category: "remote",
		Kind:     "grace_scheduled",
		At:       time.Date(2026, 9, 10, 14, 35, 0, 0, time.Local),
		Fields: map[string]string{
			"command":    "shutdown",
			"from":       "192.168.1.20",
			"delay":      "5분",
			"execute_at": "14:40:00",
		},
		Actions: []notify.Action{
			{Label: "바로 실행", Data: "runnow:"},
			{Label: "취소", Data: "cancel:"},
		},
	}
}

func TestSinkSendRendersAndAttachesKeyboard(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":321}}`)
	r, err := NewRenderer("ko", "full")
	if err != nil {
		t.Fatal(err)
	}
	s := NewSink(cli, "42", r, "DESKTOP-TEST")
	var gotEv notify.Event
	gotID, gotHTML := 0, ""
	s.OnSent = func(ev notify.Event, msgID int, html string) { gotEv, gotID, gotHTML = ev, msgID, html }

	if err := s.Send(context.Background(), testEvent()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s.ChatID() != "42" || cap.body["chat_id"] != "42" {
		t.Errorf("chat_id = %v", cap.body["chat_id"])
	}
	// Layout only; template wording is covered by the render golden tests.
	text, _ := cap.body["text"].(string)
	if !strings.HasPrefix(text, "🔌 <b>") || !strings.Contains(text, "</b>\n<blockquote>") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "<code>shutdown</code>") || !strings.HasSuffix(text, "<i>DESKTOP-TEST · 14:35:00</i>") {
		t.Errorf("text = %q", text)
	}
	if cap.body["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v", cap.body["parse_mode"])
	}
	rm, _ := cap.body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("want one keyboard row, got %v", rm)
	}
	row, _ := rows[0].([]any)
	if len(row) != 2 {
		t.Fatalf("want two buttons, got %v", row)
	}
	b1, _ := row[1].(map[string]any)
	if b1["text"] != "취소" || b1["callback_data"] != "cancel:" {
		t.Errorf("button[1] = %v", b1)
	}
	if gotID != 321 || gotEv.Key() != "remote.grace_scheduled" {
		t.Errorf("OnSent got (%s, %d)", gotEv.Key(), gotID)
	}
	if gotHTML != text {
		t.Errorf("OnSent html = %q, want the sent text %q", gotHTML, text)
	}
}

func TestSinkSendWithoutActionsOmitsKeyboard(t *testing.T) {
	cli, cap := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	r, _ := NewRenderer("en", "simple")
	s := NewSink(cli, "42", r, "PC")
	ev := testEvent()
	ev.Actions = nil
	called := false
	s.OnSent = func(notify.Event, int, string) { called = true }
	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, has := cap.body["reply_markup"]; has {
		t.Errorf("reply_markup should be omitted: %v", cap.body)
	}
	if !called {
		t.Errorf("OnSent not called")
	}
}

func TestSinkPassesRateLimitThrough(t *testing.T) {
	cli, _ := newFakeAPI(t, 429, `{"ok":false,"error_code":429,"description":"slow down","parameters":{"retry_after":3}}`)
	r, _ := NewRenderer("ko", "simple")
	s := NewSink(cli, "42", r, "PC")
	s.OnSent = func(notify.Event, int, string) { t.Errorf("OnSent must not fire on failure") }
	err := s.Send(context.Background(), testEvent())
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second {
		t.Fatalf("want *RateLimitError(3s), got %v", err)
	}
}

func TestSinkRejectsEmptyChatID(t *testing.T) {
	cli, _ := newFakeAPI(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	r, _ := NewRenderer("ko", "simple")
	s := NewSink(cli, "", r, "PC")
	if err := s.Send(context.Background(), testEvent()); err == nil {
		t.Fatal("expected error for empty chat id")
	}
}

func TestKeyboardSkipsUnlabelledActions(t *testing.T) {
	if Keyboard(nil) != nil {
		t.Error("nil actions should give nil keyboard")
	}
	if Keyboard([]notify.Action{{Label: "", Data: "x:"}}) != nil {
		t.Error("unlabelled-only actions should give nil keyboard")
	}
	kb := Keyboard([]notify.Action{{Label: "A", Data: "a:"}, {Label: "", Data: "b:"}, {Label: "C", Data: "c:"}})
	if kb == nil || len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 2 {
		t.Fatalf("keyboard = %+v", kb)
	}
	if kb.InlineKeyboard[0][1].CallbackData != "c:" {
		t.Errorf("keyboard = %+v", kb)
	}
}
