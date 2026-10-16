package tgcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/privacy"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// runCommands records what the bot ran.
type runCommands struct {
	fakeCommands
	ran []string
}

func (r *runCommands) Known(name string) bool { return name == "shutdown" || name == "lock" }
func (r *runCommands) RunNow(name string) bool {
	r.ran = append(r.ran, name)
	return true
}

func TestStatusPrivacyLine(t *testing.T) {
	c := newTestControl(t, config.Config{})
	if strings.Contains(c.statusText(), "카메라") {
		t.Fatal("a privacy line without the Privacy source")
	}
	var block *status.Privacy
	c.d.Privacy = func(config.Config) *status.Privacy { return block }
	if strings.Contains(c.statusText(), "카메라") {
		t.Fatal("a privacy line while the option is off")
	}
	block = &status.Privacy{Camera: "in_use", Microphone: "unknown"}
	if got := c.statusText(); !strings.Contains(got, "카메라·마이크: 카메라 사용 중 · 마이크 알 수 없음") {
		t.Fatalf("status:\n%s", got)
	}
}

func TestExecPrivacyGuard(t *testing.T) {
	c := newTestControl(t, config.Config{})
	cmds := &runCommands{}
	c.d.Commands = cmds
	verdict := privacy.Verdict{InUse: []string{"camera", "microphone"}, Block: true}
	c.d.Guard = func(name string) privacy.Verdict {
		if name != "shutdown" {
			return privacy.Verdict{}
		}
		return verdict
	}

	text, toast, err := c.handleCallback(context.Background(), "1", 10, "", "exec:shutdown")
	if err != nil || len(cmds.ran) != 0 {
		t.Fatalf("blocked: ran %v, err %v", cmds.ran, err)
	}
	if !strings.Contains(text, "카메라·마이크 사용 중이라") || toast != c.text("privacy_blocked_toast") {
		t.Fatalf("blocked reply %q / %q", text, toast)
	}

	verdict.Block = false
	text, _, _ = c.handleCallback(context.Background(), "1", 10, "", "exec:shutdown")
	if len(cmds.ran) != 1 || !strings.Contains(text, "⚠️ 카메라·마이크 사용 중") {
		t.Fatalf("warn: ran %v, reply %q", cmds.ran, text)
	}
}
