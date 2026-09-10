package gui

import (
	"reflect"
	"slices"
	"testing"
)

// baseConfig is what GET /api/config hands the tab: masked token, the full
// catalogue with the schedule.created/cancelled and power.stopping defaults
// off, plus one key the GUI does not know (must survive a save).
func baseConfig() Config {
	notify := map[string]map[string]bool{}
	for _, c := range notifyCatalogue {
		notify[c.cat] = map[string]bool{}
		for _, k := range c.kinds {
			notify[c.cat][k.kind] = k.on
		}
	}
	notify["system"]["future_kind"] = true
	return Config{
		Port: 5001, Secret: "s3cret", ShutdownGrace: true, GraceSeconds: 300,
		Telegram: TelegramConfig{
			Enabled: true, BotToken: "****6789", BotTokenSet: true, ChatID: "42",
			AllowedChatIDs: []string{}, Detail: "full", Lang: "ko",
			QuietHours: QuietHours{Enabled: false, Start: "22:00", End: "07:00", SecurityBypass: true, Digest: true},
		},
		Notify: notify,
	}
}

func TestNotifyCatalogueCoversDesignDoc(t *testing.T) {
	var kinds []string
	for _, c := range notifyCatalogue {
		for _, k := range c.kinds {
			kinds = append(kinds, c.cat+"."+k.kind)
		}
	}
	if len(kinds) != 21 {
		t.Fatalf("catalogue has %d kinds, want 21: %v", len(kinds), kinds)
	}
	for _, hidden := range []string{"system.test", "system.digest"} {
		if slices.Contains(kinds, hidden) {
			t.Errorf("%s must not be user-toggleable", hidden)
		}
	}
	// Every kind and category has both translations.
	for _, c := range notifyCatalogue {
		for _, l := range []Lang{LangKo, LangEn} {
			if key := "notify.cat." + c.cat; T(l, key) == key {
				t.Errorf("missing %s translation for %s", l, key)
			}
			for _, k := range c.kinds {
				if key := "notify.kind." + c.cat + "." + k.kind; T(l, key) == key {
					t.Errorf("missing %s translation for %s", l, key)
				}
			}
		}
	}
}

func TestNotifyStateFromConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.Telegram.AllowedChatIDs = []string{"1", "2"}
	s := notifyStateFromConfig(cfg)
	if s.Token != "****6789" || s.ChatID != "42" || !s.Enabled || s.AllowedChatIDs != "1, 2" || s.Detail != "full" {
		t.Errorf("unexpected state: %+v", s)
	}
	if !s.Masters["remote"] || !s.Masters["schedule"] || !s.Masters["power"] {
		t.Errorf("masters should be on when any kind is on: %v", s.Masters)
	}
	if s.Kinds["schedule"]["created"] || !s.Kinds["schedule"]["executed"] {
		t.Errorf("kinds should mirror the config: %v", s.Kinds["schedule"])
	}
	// A category with everything off derives an unchecked master.
	for k := range cfg.Notify["power"] {
		cfg.Notify["power"][k] = false
	}
	if notifyStateFromConfig(cfg).Masters["power"] {
		t.Error("master should be off when every kind is off")
	}
	// Missing keys (older service) fall back to the catalogue defaults and
	// empty strings to the design-doc defaults.
	cfg.Notify = nil
	cfg.Telegram.Detail = ""
	cfg.Telegram.QuietHours.Start = ""
	s = notifyStateFromConfig(cfg)
	if s.Kinds["schedule"]["created"] || !s.Kinds["remote"]["force"] || s.Detail != "full" || s.Quiet.Start != "22:00" {
		t.Errorf("defaults not applied: %+v", s)
	}
}

func TestNotifyApplyToKeepsOtherFields(t *testing.T) {
	base := baseConfig()
	s := notifyStateFromConfig(base)
	s.Masters["remote"] = false // master off: children keep their values but save as off
	s.Kinds["security"]["unknown_chat"] = false
	s.ChatID = " 77 "
	s.AllowedChatIDs = "77, ,  -100 ,"
	s.ControlEnabled = true
	s.Quiet = QuietHours{Enabled: true, Start: "23:00", End: "06:30", SecurityBypass: false, Digest: true}
	s.Detail = "simple"
	s.PCName = "office"

	cfg := s.applyTo(base, LangEn)
	if cfg.Port != 5001 || cfg.Secret != "s3cret" || !cfg.ShutdownGrace || cfg.GraceSeconds != 300 {
		t.Errorf("settings-tab fields were changed: %+v", cfg)
	}
	tg := cfg.Telegram
	if tg.BotToken != "****6789" {
		t.Errorf("an untouched token must go back masked (keep), got %q", tg.BotToken)
	}
	if tg.ChatID != "77" || !reflect.DeepEqual(tg.AllowedChatIDs, []string{"77", "-100"}) || !tg.ControlEnabled {
		t.Errorf("chat fields: %+v", tg)
	}
	if tg.Detail != "simple" || tg.PCName != "office" || tg.Lang != "en" || tg.QuietHours != s.Quiet {
		t.Errorf("display/quiet fields: %+v", tg)
	}
	for kind := range cfg.Notify["remote"] {
		if cfg.Notify["remote"][kind] {
			t.Errorf("remote.%s should save as off under an unchecked master", kind)
		}
	}
	if cfg.Notify["security"]["unknown_chat"] || !cfg.Notify["security"]["unauthorized"] {
		t.Errorf("security kinds: %v", cfg.Notify["security"])
	}
	if !cfg.Notify["system"]["future_kind"] {
		t.Error("keys the GUI does not know must survive a save")
	}
	// base must not be aliased.
	if base.Notify["remote"]["received"] != true || len(base.Telegram.AllowedChatIDs) != 0 {
		t.Error("applyTo mutated the baseline")
	}
}

func TestNotifyDirty(t *testing.T) {
	base := baseConfig()
	fresh := func() notifyFormState { return notifyStateFromConfig(base) }
	if fresh().dirty(base) {
		t.Fatal("a freshly filled form must not be dirty")
	}
	cases := map[string]func(s *notifyFormState){
		"enabled":      func(s *notifyFormState) { s.Enabled = false },
		"chat id":      func(s *notifyFormState) { s.ChatID = "43" },
		"control":      func(s *notifyFormState) { s.ControlEnabled = true },
		"allowed":      func(s *notifyFormState) { s.AllowedChatIDs = "1" },
		"quiet":        func(s *notifyFormState) { s.Quiet.Enabled = true },
		"quiet start":  func(s *notifyFormState) { s.Quiet.Start = "21:00" },
		"detail":       func(s *notifyFormState) { s.Detail = "simple" },
		"pc name":      func(s *notifyFormState) { s.PCName = "x" },
		"kind":         func(s *notifyFormState) { s.Kinds["remote"]["force"] = false },
		"master":       func(s *notifyFormState) { s.Masters["remote"] = false },
		"new token":    func(s *notifyFormState) { s.Token = "123456789:ABCDEFGHIJKLMNOP" },
		"clear token":  func(s *notifyFormState) { s.Token = "-" },
		"kind default": func(s *notifyFormState) { s.Kinds["schedule"]["created"] = true },
	}
	for name, mutate := range cases {
		s := fresh()
		mutate(&s)
		if !s.dirty(base) {
			t.Errorf("%s: change not detected", name)
		}
	}
	notDirty := map[string]func(s *notifyFormState){
		"emptied token":      func(s *notifyFormState) { s.Token = "" },
		"masked token":       func(s *notifyFormState) { s.Token = "****6789" },
		"other masked":       func(s *notifyFormState) { s.Token = "****" },
		"whitespace chat id": func(s *notifyFormState) { s.ChatID = " 42 " },
		"allowed spacing":    func(s *notifyFormState) { s.AllowedChatIDs = " , " },
		// Turning an off kind on under an off master saves nothing new.
		"kind under off master": func(s *notifyFormState) {
			for k := range s.Kinds["power"] {
				s.Kinds["power"][k] = false
			}
			s.Masters["power"] = false
			base.Notify["power"] = map[string]bool{"started": false, "resumed": false, "stopping": false}
			s.Kinds["power"]["resumed"] = true
		},
	}
	for name, mutate := range notDirty {
		base = baseConfig()
		s := fresh()
		mutate(&s)
		if s.dirty(base) {
			t.Errorf("%s: should not be dirty", name)
		}
	}
	// The language is written silently on save and never makes the form dirty.
	base = baseConfig()
	base.Telegram.Lang = "en"
	if fresh().dirty(base) {
		t.Error("language must not count as a change")
	}
	// An older service without notify/detail/quiet keys: still not dirty.
	base = baseConfig()
	base.Notify = nil
	base.Telegram.Detail = ""
	base.Telegram.QuietHours.Start, base.Telegram.QuietHours.End = "", ""
	base.Telegram.AllowedChatIDs = nil
	if fresh().dirty(base) {
		t.Error("defaults filled by the form must not read as changes")
	}
}

func TestTokenRules(t *testing.T) {
	cases := []struct {
		typed, masked string
		changed       bool
		send          string
	}{
		{"", "****6789", false, "****6789"},
		{"", "", false, ""},
		{"****6789", "****6789", false, "****6789"},
		{"****", "****6789", false, "****"},
		{"-", "****6789", true, "-"},
		{"123456789:ABCDEF", "****6789", true, "123456789:ABCDEF"},
		{"123456789:ABCDEF", "", true, "123456789:ABCDEF"},
	}
	for _, c := range cases {
		if got := tokenChanged(c.typed, c.masked); got != c.changed {
			t.Errorf("tokenChanged(%q, %q) = %v, want %v", c.typed, c.masked, got, c.changed)
		}
		if got := tokenToSend(c.typed, c.masked); got != c.send {
			t.Errorf("tokenToSend(%q, %q) = %q, want %q", c.typed, c.masked, got, c.send)
		}
	}
}

func TestMaskedAfterSave(t *testing.T) {
	cases := []struct{ typed, old, want string }{
		{"", "****6789", "****6789"},
		{"****6789", "****6789", "****6789"},
		{"-", "****6789", ""},
		{"123456789:ABCDEFGHIJ", "", "****GHIJ"},
		{"short", "****6789", "****"},
	}
	for _, c := range cases {
		if got := maskedAfterSave(c.typed, c.old); got != c.want {
			t.Errorf("maskedAfterSave(%q, %q) = %q, want %q", c.typed, c.old, got, c.want)
		}
	}
}

func TestParseChatIDs(t *testing.T) {
	if got := parseChatIDs(""); got == nil || len(got) != 0 {
		t.Errorf("empty text must give a non-nil empty slice, got %#v", got)
	}
	if got := parseChatIDs(" 1,, 2 ,-100,"); !reflect.DeepEqual(got, []string{"1", "2", "-100"}) {
		t.Errorf("got %v", got)
	}
}

func TestQuietHourOptions(t *testing.T) {
	opts := quietHourOptions()
	if len(opts) != 48 || opts[0] != "00:00" || opts[1] != "00:30" || opts[47] != "23:30" {
		t.Errorf("unexpected options: %v", opts)
	}
	if !slices.Contains(opts, defaultQuietStart) || !slices.Contains(opts, defaultQuietEnd) {
		t.Error("defaults must be selectable")
	}
}

func TestChatLabel(t *testing.T) {
	cases := []struct {
		in   TelegramChat
		want string
	}{
		{TelegramChat{ChatID: "42", Title: "Kim Lump", Username: "lump", Type: "private"}, "Kim Lump (@lump) · 42"},
		{TelegramChat{ChatID: "-100", Title: "Home", Type: "group"}, "Home · -100"},
		{TelegramChat{ChatID: "7", Type: "private"}, "private · 7"},
	}
	for _, c := range cases {
		if got := chatLabel(c.in); got != c.want {
			t.Errorf("chatLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
