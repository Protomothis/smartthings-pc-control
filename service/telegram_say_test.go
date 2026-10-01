package service

// Tests for /say (#106).

import (
	"context"
	"strings"
	"testing"
)

func tgSayCfg(notify NotifyPCConfig) Config {
	return Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko", PCName: "PC"}, NotifyPC: notify}
}

func TestTelegramSay(t *testing.T) {
	initLogger()
	withLiveConfig(t, tgSayCfg(NotifyPCConfig{Enabled: true}))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
	h := tgCtl

	reply, _, err := h.HandleCommand(context.Background(), "42", "say", []string{"빨래가", "끝났어요", "$(calc)"})
	if err != nil || !strings.Contains(reply, "PC에 알림을 띄웠습니다") {
		t.Fatalf("reply %q, %v", reply, err)
	}
	// The words are joined back into the sentence.
	want := []string{"notify", "--title", "SmartThings", "--text", "빨래가 끝났어요 $(calc)"}
	if len(*calls) != 1 || strings.Join((*calls)[0], "|") != strings.Join(want, "|") {
		t.Errorf("args = %q", *calls)
	}

	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", nil); !strings.Contains(reply, "사용법") {
		t.Errorf("no text: %q", reply)
	}
}

func TestTelegramSayRefusals(t *testing.T) {
	initLogger()
	withLiveConfig(t, tgSayCfg(NotifyPCConfig{Enabled: false}))
	calls := fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
	h := tgCtl

	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", []string{"hi"}); !strings.Contains(reply, "PC 알림이 꺼져") {
		t.Errorf("disabled: %q", reply)
	}
	setConfig(tgSayCfg(NotifyPCConfig{Enabled: true}))
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", []string{strings.Repeat("a", 201)}); !strings.Contains(reply, "1~200자") {
		t.Errorf("too long: %q", reply)
	}
	if len(*calls) != 0 {
		t.Errorf("refused /say ran: %q", *calls)
	}

	for i := 0; i < pcNotifyPerMinute; i++ {
		h.HandleCommand(context.Background(), "42", "say", []string{"hi"})
	}
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", []string{"hi"}); !strings.Contains(reply, "너무 잦습니다") {
		t.Errorf("rate limited: %q", reply)
	}
	// Another chat has its own allowance.
	if reply, _, _ := h.HandleCommand(context.Background(), "43", "say", []string{"hi"}); !strings.Contains(reply, "띄웠습니다") {
		t.Errorf("other chat: %q", reply)
	}

	fakeNotifyRun(t, "", errNoUserSession)
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", []string{"hi"}); !strings.Contains(reply, "로그인한 사용자가 없어") {
		t.Errorf("no session: %q", reply)
	}
	fakeNotifyRun(t, "", &userActionError{Code: "failed", Message: "toast <failed>"})
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "say", []string{"hi"}); !strings.Contains(reply, "toast &lt;failed&gt;") {
		t.Errorf("failure is escaped: %q", reply)
	}
}
