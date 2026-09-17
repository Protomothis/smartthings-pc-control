-- ko/en strings for the human-readable *attribute values* the app shows
-- (`pcStatus.message`, `pcCommand.lastCommand`, `pcSchedule.origin`).
--
-- Design doc §6.5: profile/presentation labels stay English; only these string
-- attributes follow the `language` preference. `auto` resolves to `en` because
-- a driver cannot read the hub locale.
--
-- Pure Lua: no st.* / cosock requires, so tests load it directly.

local i18n = {}

i18n.DEFAULT_LANG = "en"

-- key -> { ko = ..., en = ... }. Values may contain string.format verbs.
local STRINGS = {
  -- WoL / wake
  wake_failed = {
    ko = "깨우기 실패: WoL 응답 없음",
    en = "Wake failed: no WoL response",
  },
  wol_not_ready = {
    ko = "PC의 어댑터에 WoL이 꺼져 있습니다 · 네트워크 탭 확인",
    en = "Wake-on-LAN is off on the PC's adapter · check the Network tab",
  },
  wol_no_mac = {
    ko = "MAC 주소를 설정하세요",
    en = "Set the MAC address in settings",
  },
  wol_bad_mac = {
    ko = "MAC 주소 형식이 올바르지 않습니다: %s",
    en = "MAC address is not valid: %s",
  },

  -- connection / classification (§6.1)
  unauthorized = {
    ko = "시크릿이 일치하지 않습니다 · 설정에서 확인하세요",
    en = "Secret does not match · check the device settings",
  },
  -- 403: the secret was fine, the hub is simply not in
  -- `smartthings.allowed_hubs` (§4.1). Same `connection` value as 401, but the
  -- fix is a different one, so it gets its own sentence.
  forbidden = {
    ko = "허브가 허용 목록에 없습니다 · PC의 SmartThings 설정에서 추가하세요",
    en = "Hub not in allow-list · add it in the PC's SmartThings settings",
  },
  unreachable = {
    ko = "PC에 연결할 수 없습니다",
    en = "Cannot reach the PC",
  },
  incompatible_service = {
    ko = "서비스 v%s 이상 필요",
    en = "Requires service v%s or newer",
  },
  incompatible_driver = {
    ko = "드라이버 업데이트 필요",
    en = "Driver update required",
  },
  badrequest = {
    ko = "서비스가 명령을 거부했습니다",
    en = "The service rejected the command",
  },
  no_secret = {
    ko = "시크릿이 설정되지 않았습니다 · 설정을 권장합니다",
    en = "No secret is set · setting one is recommended",
  },
  no_ip = {
    ko = "PC의 IP 주소를 설정하세요",
    en = "Set the PC IP address in settings",
  },
  -- 429 (§8). Never shown as a `message`: a rate-limited poll keeps the last
  -- state, so this only reaches the driver log.
  ratelimited = {
    ko = "요청이 너무 잦습니다 · 폴링 주기를 늘리세요",
    en = "Too many requests · increase the poll interval",
  },

  -- service state notices (§4.2)
  update_available = {
    ko = "서비스 업데이트 %s 사용 가능",
    en = "Service update %s available",
  },
  -- `update.available` without a usable `update.latest`.
  update_available_plain = {
    ko = "서비스 업데이트 사용 가능",
    en = "A service update is available",
  },

  -- command outcomes (§4.3/§4.4)
  schedule_replaced = {
    ko = "기존 예약을 새 예약으로 대체했습니다",
    en = "Replaced the existing schedule",
  },
  schedule_cancelled = {
    ko = "예약을 취소했습니다",
    en = "Schedule cancelled",
  },
  schedule_none = {
    ko = "취소할 예약이 없습니다",
    en = "There was no schedule to cancel",
  },

  -- schedule origins (§4.2). `remote` is the legacy PCControl HTTP path, which
  -- for this driver always means a SmartThings command.
  origin_ui = { ko = "앱", en = "App" },
  origin_remote = { ko = "SmartThings 명령", en = "SmartThings command" },
  origin_telegram = { ko = "텔레그램", en = "Telegram" },
  origin_smartthings = { ko = "SmartThings", en = "SmartThings" },

  -- command names (§4.3). Wording follows the Go side (service/telegram_control.go).
  cmd_shutdown = { ko = "종료", en = "Shut down" },
  cmd_forceshutdown = { ko = "강제 종료", en = "Force shut down" },
  cmd_restart = { ko = "재시작", en = "Restart" },
  cmd_suspend = { ko = "절전", en = "Sleep" },
  cmd_hibernate = { ko = "최대 절전", en = "Hibernate" },
  cmd_lock = { ko = "잠금", en = "Lock" },
  cmd_turnscreenoff = { ko = "화면 끄기", en = "Screen off" },
  cmd_turnscreenon = { ko = "화면 켜기", en = "Screen on" },
  cmd_ping = { ko = "핑", en = "Ping" },
}

--- Normalise a `language` preference value to a supported language code.
-- `auto`, nil and anything unknown resolve to `en` (§6.5).
function i18n.resolve(lang)
  if lang == "ko" or lang == "en" then
    return lang
  end
  return i18n.DEFAULT_LANG
end

--- Look up `key` in `lang`, formatted with the remaining arguments.
-- An unknown key returns the key itself so a missing string is visible in the
-- app rather than crashing the driver.
function i18n.t(lang, key, ...)
  local entry = STRINGS[key]
  if not entry then
    return tostring(key)
  end
  local text = entry[i18n.resolve(lang)] or entry[i18n.DEFAULT_LANG] or tostring(key)
  if select("#", ...) > 0 then
    local ok, formatted = pcall(string.format, text, ...)
    if ok then
      return formatted
    end
  end
  return text
end

--- Localised label for a schedule/command origin (`ui`, `remote`, `telegram`,
-- `smartthings`). Unknown origins are returned unchanged.
function i18n.origin(lang, origin)
  if origin == nil or origin == "" then
    return ""
  end
  local key = "origin_" .. tostring(origin)
  if STRINGS[key] then
    return i18n.t(lang, key)
  end
  return tostring(origin)
end

--- Localised label for a service command name. Unknown commands are returned
-- unchanged so a newer service's command still shows something useful.
function i18n.command(lang, command)
  if command == nil or command == "" then
    return ""
  end
  local key = "cmd_" .. tostring(command)
  if STRINGS[key] then
    return i18n.t(lang, key)
  end
  return tostring(command)
end

--- True when `key` exists; used by tests to catch typos in the table.
function i18n.has(key)
  return STRINGS[key] ~= nil
end

return i18n
