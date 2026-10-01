package config

import "slices"

// ChangedKeys lists the security-relevant settings that differ between
// old and new, for the security.config_changed event. Values are never
// included — only key names.
func ChangedKeys(old, new Config) []string {
	var keys []string
	add := func(key string, changed bool) {
		if changed {
			keys = append(keys, key)
		}
	}
	add("secret", old.Secret != new.Secret)
	add("port", old.Port != new.Port)
	add("webui_remote", old.WebUIRemote != new.WebUIRemote)
	add("telegram.enabled", old.Telegram.Enabled != new.Telegram.Enabled)
	add("telegram.bot_token", old.Telegram.BotToken != new.Telegram.BotToken)
	add("telegram.chat_id", old.Telegram.ChatID != new.Telegram.ChatID)
	add("telegram.control_enabled", old.Telegram.ControlEnabled != new.Telegram.ControlEnabled)
	add("telegram.allowed_chat_ids", !slices.Equal(old.Telegram.AllowedChatIDs, new.Telegram.AllowedChatIDs))
	add("telegram.detail", old.Telegram.Detail != new.Telegram.Detail)
	add("telegram.lang", old.Telegram.Lang != new.Telegram.Lang)
	add("telegram.pc_name", old.Telegram.PCName != new.Telegram.PCName)
	add("telegram.quiet_hours", old.Telegram.QuietHours != new.Telegram.QuietHours)
	add("smartthings.allowed_hubs", !slices.Equal(old.SmartThings.AllowedHubs, new.SmartThings.AllowedHubs))
	add("smartthings.expose_session", old.SmartThings.ExposeSession != new.SmartThings.ExposeSession)
	add("smartthings.expose_session_user", old.SmartThings.ExposeSessionUser != new.SmartThings.ExposeSessionUser)
	add("smartthings.wol_mac", old.SmartThings.WoLMAC != new.SmartThings.WoLMAC)
	// What the hub learns about running programs is privacy-relevant too.
	add("activity.enabled", old.Activity.Enabled != new.Activity.Enabled)
	add("activity.watch", !slices.Equal(old.Activity.Watch, new.Activity.Watch))
	add("media.enabled", old.Media.Enabled != new.Media.Enabled)
	add("media.now_playing", old.Media.NowPlaying != new.Media.NowPlaying)
	// PC notifications and presets let the network act in the user's
	// session (#106, #109); a preset change names its slots, never what
	// they run.
	add("notify_pc.enabled", old.NotifyPC.Enabled != new.NotifyPC.Enabled)
	if slots := ChangedPresetSlots(old.Presets, new.Presets); len(slots) > 0 {
		keys = append(keys, PresetChangeKey(slots))
	}
	return keys
}
