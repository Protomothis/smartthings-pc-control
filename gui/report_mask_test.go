package gui

import (
	"strings"
	"testing"
)

func testMasker() *Masker {
	return NewMasker(MaskInput{
		Secrets: []string{"hunter2pass", "", "abc"}, // "abc" is too short to mask
		ChatIDs: []string{"123456789", "-1001234567890", ""},
		Names:   []string{"DESKTOP-K1M", "kim", "x", "거실PC"},
	})
}

func TestMaskLine(t *testing.T) {
	m := testMasker()
	for _, c := range []struct{ in, want string }{
		// secrets, in any case; a too-short one stays
		{"login with HUNTER2PASS failed", "login with <secret> failed"},
		{"secret=hunter2pass;abc", "secret=<secret>;abc"},
		// a bot token, known or not
		{"token 987654321:AAH1bcdefghijklmnopqrstuvwxyz012345 bad", "token <bot-token> bad"},
		// chat ids: configured, and any long number after "chat"
		{"Telegram: 명령 수신 /status (chat 123456789)", "Telegram: 명령 수신 /status (chat <chat-id>)"},
		{"send to -1001234567890 ok", "send to <chat-id> ok"},
		{"chat_id=55555555 refused", "chat_id=<chat-id> refused"},
		{`{"chat_id":"777777777"}`, `{"chat_id":"<chat-id>"}`},
		{"Telegram: ignored message from unknown chat 42424242 (someone): hello there",
			"Telegram: ignored message from unknown chat <chat-id> (<user>): <text>"},
		// e-mail
		{"mail me at Jane.Doe+x@example.co.kr please", "mail me at <email> please"},
		// names as whole words, any case; not inside other words
		{"PC desktop-k1m started by KIM", "PC <name> started by <name>"},
		{"skimming kimchi kim.", "skimming kimchi <name>."},
		{"거실PC 에서 실행", "<name> 에서 실행"},
		// user folders, either slash, doubled backslashes
		{`open C:\Users\Jane Doe\Documents\a.ps1`, `open C:\Users\<user>\Documents\a.ps1`},
		{`"C:\\Users\\jdoe\\x"`, `"C:\\Users\\<user>\\x"`},
		{"c:/users/jdoe/AppData", "c:/users/<user>/AppData"},
		// MAC addresses keep the vendor
		{"WoL: adapter AA:BB:CC:DD:EE:FF selected", "WoL: adapter AA:BB:CC:XX:XX:XX selected"},
		{"mac 00-1a-2b-3c-4d-5e", "mac 00-1a-2b-XX-XX-XX"},
		// IPv4: public gone, private loses its last number, loopback stays
		{"reply to 8.8.8.8 and 203.0.113.9", "reply to <public-ip> and <public-ip>"},
		{"hub 192.168.1.20, 10.0.0.2, 172.16.5.4, 100.101.102.103", "hub 192.168.1.x, 10.0.0.x, 172.16.5.x, 100.101.102.x"},
		{"listen 127.0.0.1:5001 and 0.0.0.0", "listen 127.0.0.1:5001 and 0.0.0.0"},
		{"version 1.2.3.400 stays", "version 1.2.3.400 stays"},
		// IPv6, but not the loopback nor a clock time
		{"from 2001:db8::1 and fe80::1%12", "from <ipv6> and <ipv6>"},
		{"at 15:04:05 on ::1", "at 15:04:05 on ::1"},
		// now playing: the rest of the line
		{"2026/11/15 09:12:05 Media: now playing NewJeans - Hype Boy (Spotify), volume 42%",
			"2026/11/15 09:12:05 Media: now playing <media>"},
		{`media {"title":"Hype Boy","artist":"NewJeans","app":"Spotify"}`,
			`media {"title":"<media>","artist":"<media>","app":"Spotify"}`},
		{"title=Ditto, artist=NewJeans, status=playing", "title=<media>, artist=<media>, status=playing"},
		// nothing personal: unchanged
		{"2026/11/15 09:12:05 Listening on port 5001 (WebUI 5002)", "2026/11/15 09:12:05 Listening on port 5001 (WebUI 5002)"},
	} {
		if got := m.Line(c.in); got != c.want {
			t.Errorf("Line(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Masking twice changes nothing: no placeholder matches a rule again.
func TestMaskIdempotent(t *testing.T) {
	m := NewMasker(MaskInput{Names: []string{"user", "name", "media"}, ChatIDs: []string{"chat"}})
	in := `C:\Users\user\x 8.8.8.8 a@b.cc now playing x title=y 192.168.0.5 AA:BB:CC:DD:EE:FF chat 1234567`
	once := m.Line(in)
	if twice := m.Line(once); twice != once {
		t.Fatalf("second pass changed it:\n%q\n%q", once, twice)
	}
}

func TestMaskText(t *testing.T) {
	m := testMasker()
	got := m.Text("a kim\r\nb 8.8.8.8\n")
	if got != "a <name>\r\nb <public-ip>\n" {
		t.Fatalf("got %q", got)
	}
	if m.Text("") != "" {
		t.Fatal("empty text changed")
	}
	if l := m.Lines([]string{"kim", "x"}); l[0] != "<name>" || l[1] != "x" {
		t.Fatalf("Lines %q", l)
	}
}

func TestReportMaskInput(t *testing.T) {
	cfg := Config{Secret: "s3cr3t!", Telegram: TelegramConfig{BotToken: "****6789", ChatID: "111111", AllowedChatIDs: []string{"222222"}, PCName: "Office"}}
	in := ReportMaskInput(cfg, SystemInfo{PCName: "DESK", UserName: "jdoe"})
	if strings.Join(in.Secrets, ",") != "s3cr3t!" {
		t.Errorf("a masked token is no secret to hide: %q", in.Secrets)
	}
	if strings.Join(in.ChatIDs, ",") != "111111,222222" || strings.Join(in.Names, ",") != "DESK,jdoe,Office" {
		t.Errorf("chats %q names %q", in.ChatIDs, in.Names)
	}
	cfg.Telegram.BotToken = "12345678:plain"
	if in := ReportMaskInput(cfg, SystemInfo{}); len(in.Secrets) != 2 || in.Secrets[1] != "12345678:plain" {
		t.Errorf("a plaintext token is hidden too: %q", in.Secrets)
	}
}
