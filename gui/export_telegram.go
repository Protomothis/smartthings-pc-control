package gui

// The Telegram form's model, used by the Telegram page
// (ui/pages_telegram.go, #165): the event catalogue, the
// config round trip, the dirty check and the token/chat helpers. No
// toolkit here.

import (
	"fmt"
	"slices"
	"strings"
)

// botFatherURL is the hint hyperlink of the connection section.
const botFatherURL = "https://core.telegram.org/bots#how-do-i-create-a-bot"

// notifyKind is one toggleable Category.Kind with its catalogue default —
// the fallback when a (pre-catalogue) service omits the key.
type notifyKind struct {
	kind string
	on   bool
}

// notifyCatalogue lists the categories in display order with every
// user-toggleable kind (design doc §3). system.test and system.digest are
// deliberately absent: they are not user-toggleable.
var notifyCatalogue = []struct {
	cat   string
	kinds []notifyKind
}{
	{"remote", []notifyKind{{"received", true}, {"grace_scheduled", true}, {"grace_cancelled", true}, {"executed", true}, {"force", true}}},
	{"schedule", []notifyKind{{"created", false}, {"cancelled", false}, {"executed", true}, {"replaced", true}}},
	// #87: power.stopping is on by default now that its message names the
	// reason. This row is only the fallback for a key config.json does not
	// carry; it must stay in step with the service catalogue
	// (service/notify/config.go), or the checkbox would open on a value the
	// service does not use.
	{"power", []notifyKind{{"started", true}, {"resumed", true}, {"stopping", true}}},
	{"security", []notifyKind{{"unauthorized", true}, {"login_limited", true}, {"unknown_command", true}, {"config_changed", true}, {"unknown_chat", true}}},
	{"system", []notifyKind{{"update_available", true}, {"updated", true}, {"exec_failed", true}, {"tray_wake_failed", true}}},
}

// detailOptions maps the detail select's index to the wire value.
var detailOptions = []string{"simple", "full"}

const (
	defaultDetail     = "full"
	defaultQuietStart = "22:00"
	defaultQuietEnd   = "07:00"
)

// --- Pure form model (unit-tested) -----------------------------------------

// notifyFormState is the tab's contents as plain values, independent of
// widgets, so the config round-trip and the dirty check are testable.
type notifyFormState struct {
	// Token is the entry text: "" or the masked value mean "keep the
	// stored token", "-" clears it, anything else is a new plaintext token.
	Token          string
	ChatID         string
	Enabled        bool
	ControlEnabled bool
	AllowedChatIDs string // comma-separated entry text
	// Masters is the per-category master check; Kinds the child checks as
	// displayed. A kind is effectively on only when its master is on too
	// (children keep their values while the master is off, see
	// effectiveNotify).
	Masters map[string]bool
	Kinds   map[string]map[string]bool
	Quiet   QuietHours
	Detail  string // "simple" | "full"
	PCName  string
}

// normalizeTelegram fills the string defaults a client or an older
// service may leave empty, so the form and the dirty check see one shape.
func normalizeTelegram(tg TelegramConfig) TelegramConfig {
	if tg.Detail != "simple" && tg.Detail != "full" {
		tg.Detail = defaultDetail
	}
	if tg.QuietHours.Start == "" {
		tg.QuietHours.Start = defaultQuietStart
	}
	if tg.QuietHours.End == "" {
		tg.QuietHours.End = defaultQuietEnd
	}
	return tg
}

// notifyValue reports whether cat.kind is on in cfg, falling back to the
// catalogue default when the key is missing.
func notifyValue(cfg Config, cat, kind string) bool {
	if kinds, ok := cfg.Notify[cat]; ok {
		if v, ok := kinds[kind]; ok {
			return v
		}
	}
	for _, c := range notifyCatalogue {
		if c.cat != cat {
			continue
		}
		for _, k := range c.kinds {
			if k.kind == kind {
				return k.on
			}
		}
	}
	return false
}

// anyOn reports whether at least one value is true.
func anyOn(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

// notifyStateFromConfig is what the tab shows for cfg: the master of a
// category is on when any of its kinds is.
func notifyStateFromConfig(cfg Config) notifyFormState {
	tg := normalizeTelegram(cfg.Telegram)
	s := notifyFormState{
		Token:          tg.BotToken,
		ChatID:         tg.ChatID,
		Enabled:        tg.Enabled,
		ControlEnabled: tg.ControlEnabled,
		AllowedChatIDs: strings.Join(tg.AllowedChatIDs, ", "),
		Masters:        map[string]bool{},
		Kinds:          map[string]map[string]bool{},
		Quiet:          tg.QuietHours,
		Detail:         tg.Detail,
		PCName:         tg.PCName,
	}
	for _, c := range notifyCatalogue {
		kinds := make(map[string]bool, len(c.kinds))
		for _, k := range c.kinds {
			kinds[k.kind] = notifyValue(cfg, c.cat, k.kind)
		}
		s.Kinds[c.cat] = kinds
		s.Masters[c.cat] = anyOn(kinds)
	}
	return s
}

// effective reports the value that will be saved for cat.kind: the child
// value gated by its master.
func (s notifyFormState) effective(cat, kind string) bool {
	return s.Masters[cat] && s.Kinds[cat][kind]
}

// effectiveNotify returns a fresh map: base's entries (so keys the GUI does
// not know survive) with every catalogue kind overwritten by the form.
func (s notifyFormState) effectiveNotify(base map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(notifyCatalogue))
	for cat, kinds := range base {
		out[cat] = make(map[string]bool, len(kinds))
		for kind, on := range kinds {
			out[cat][kind] = on
		}
	}
	for _, c := range notifyCatalogue {
		if out[c.cat] == nil {
			out[c.cat] = make(map[string]bool, len(c.kinds))
		}
		for _, k := range c.kinds {
			out[c.cat][k.kind] = s.effective(c.cat, k.kind)
		}
	}
	return out
}

// tokenChanged reports whether the token entry holds a new token: anything
// non-empty that is neither the masked value nor another "****" form (the
// service keeps the stored token for those, so they are not a change).
func tokenChanged(typed, masked string) bool {
	return typed != "" && typed != masked && !strings.HasPrefix(typed, maskedTokenPrefix)
}

// tokenToSend is the bot_token for POST /api/config: an emptied entry
// sends the masked value back (keep), otherwise the text as typed — the
// service applies the keep/clear/replace rules itself.
func tokenToSend(typed, masked string) string {
	if typed == "" {
		return masked
	}
	return typed
}

// parseChatIDs splits the comma-separated entry, trimming blanks and
// dropping empties. Never nil, so the JSON is [] (nil would mean "keep
// current" to the service).
func parseChatIDs(text string) []string {
	out := []string{}
	for _, part := range strings.Split(text, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyTo returns base with the form's fields written over telegram/notify.
// Everything else (port, secret, grace…) is untouched so a save from this
// tab never clobbers the settings tab; the message language follows the
// app language (design doc §10). base's maps and slices are not aliased.
func (s notifyFormState) applyTo(base Config, appLang Lang) Config {
	cfg := base
	tg := normalizeTelegram(base.Telegram)
	tg.BotToken = tokenToSend(s.Token, base.Telegram.BotToken)
	tg.Enabled = s.Enabled
	tg.ChatID = strings.TrimSpace(s.ChatID)
	tg.ControlEnabled = s.ControlEnabled
	tg.AllowedChatIDs = parseChatIDs(s.AllowedChatIDs)
	tg.Detail = s.Detail
	tg.PCName = strings.TrimSpace(s.PCName)
	tg.QuietHours = s.Quiet
	tg.Lang = string(appLang)
	cfg.Telegram = tg
	cfg.Notify = s.effectiveNotify(base.Notify)
	return cfg
}

// dirty reports whether saving would change base. The language is not
// compared (it is written silently on save) and a masked/empty token is
// "unchanged".
func (s notifyFormState) dirty(base Config) bool {
	if tokenChanged(s.Token, base.Telegram.BotToken) {
		return true
	}
	tg := normalizeTelegram(base.Telegram)
	if s.Enabled != tg.Enabled ||
		strings.TrimSpace(s.ChatID) != tg.ChatID ||
		s.ControlEnabled != tg.ControlEnabled ||
		!slices.Equal(parseChatIDs(s.AllowedChatIDs), tg.AllowedChatIDs) ||
		s.Quiet != tg.QuietHours ||
		s.Detail != tg.Detail ||
		strings.TrimSpace(s.PCName) != tg.PCName {
		return true
	}
	for _, c := range notifyCatalogue {
		for _, k := range c.kinds {
			if s.effective(c.cat, k.kind) != notifyValue(base, c.cat, k.kind) {
				return true
			}
		}
	}
	return false
}

// quietHourOptions is "00:00" … "23:30" in 30-minute steps.
func quietHourOptions() []string {
	out := make([]string, 0, 48)
	for h := 0; h < 24; h++ {
		out = append(out, fmt.Sprintf("%02d:00", h), fmt.Sprintf("%02d:30", h))
	}
	return out
}

// pcNamePlaceholder is what the PC-name entry shows while it is empty: the
// name the service would fall back to (#75). An unknown hostname leaves the
// placeholder to the caller's translated text.
func pcNamePlaceholder(host, fallback string) string {
	if h := strings.TrimSpace(host); h != "" {
		return h
	}
	return fallback
}

// chatLabel renders one /api/telegram/chats entry for the picker:
// "title (@username) · chat_id".
func chatLabel(c TelegramChat) string { return chatTitle(c) + " · " + c.ChatID }

// chatTitle is a found chat's name: "title (@username)", or its type when
// it has no title.
func chatTitle(c TelegramChat) string {
	title := c.Title
	if title == "" {
		title = c.Type
	}
	if c.Username != "" {
		title += " (@" + c.Username + ")"
	}
	return title
}

// --- Exported for the Gio page (package ui) --------------------------------

// BotFatherURL is the "how to make a bot" link.
const BotFatherURL = botFatherURL

// TelegramForm is the Telegram page as plain values: the token text, the
// chat IDs, the switches, the event catalogue, quiet hours and display.
type TelegramForm = notifyFormState

// NewTelegramForm is what the page shows for cfg. Token is the masked
// value the service handed out ("" when none is stored).
func NewTelegramForm(cfg Config) TelegramForm { return notifyStateFromConfig(cfg) }

// ApplyTo writes the form's telegram/notify fields over cfg; the message
// language follows l. Fresh maps and slices, so cfg's are not aliased.
func (s notifyFormState) ApplyTo(cfg *Config, l Lang) { *cfg = s.applyTo(*cfg, l) }

// Dirty reports whether saving would change base.
func (s notifyFormState) Dirty(base Config) bool { return s.dirty(base) }

// TelegramCategory is one category of the event catalogue with its
// user-toggleable kinds, in display order. Texts: "notify.cat.<Cat>" and
// "notify.kind.<Cat>.<kind>".
type TelegramCategory struct {
	Cat   string
	Kinds []string
}

// TelegramCatalogue is the event catalogue in display order.
func TelegramCatalogue() []TelegramCategory {
	out := make([]TelegramCategory, len(notifyCatalogue))
	for i, c := range notifyCatalogue {
		kinds := make([]string, len(c.kinds))
		for j, k := range c.kinds {
			kinds[j] = k.kind
		}
		out[i] = TelegramCategory{Cat: c.cat, Kinds: kinds}
	}
	return out
}

// TelegramDetailOptions are the detail levels, as the wire names them;
// their texts are "notify.detail.<value>".
func TelegramDetailOptions() []string { return slices.Clone(detailOptions) }

// TelegramTokenChanged reports whether typed is a new token rather than
// "keep the stored one" (empty, or a masked value).
func TelegramTokenChanged(typed, masked string) bool { return tokenChanged(typed, masked) }

// TelegramTokenToSend is the bot_token for a save, the test and the chat
// lookup: an empty field means the stored token.
func TelegramTokenToSend(typed, masked string) string { return tokenToSend(typed, masked) }

// ParseChatIDs splits a comma-separated list of chat IDs; never nil.
func ParseChatIDs(text string) []string { return parseChatIDs(text) }

// QuietHourOptions is "00:00" … "23:30" in 30-minute steps.
func QuietHourOptions() []string { return quietHourOptions() }

// PCNamePlaceholder is the PC-name field's hint: host, or fallback.
func PCNamePlaceholder(host, fallback string) string { return pcNamePlaceholder(host, fallback) }

// TelegramChatTitle is a found chat's name: "title (@username)"; the
// chat ID goes under it.
func TelegramChatTitle(c TelegramChat) string { return chatTitle(c) }
