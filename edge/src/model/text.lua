-- The sentences of the status rows (design doc §4): summaries, versions, the
-- `pcInfo.message` ladder. Pure; the words themselves are in i18n.lua.
--
-- Two platform facts shape every function here (platform notes "상세
-- 화면(detailView) 위젯", "화면 배치"): an empty string reads as "-" like a row
-- that was never sent, so every row always has a sentence; and a row is narrow
-- and cut with "…" without saying so, so an optional part is only added while
-- the line still fits.

local features = require "features"
local i18n = require "i18n"
local wolinfo = require "model.wol"

local text = {}

--- "2026-09-17T23:05:00+09:00" -> "23:05"; anything else comes back unchanged.
function text.hhmm(iso)
  if type(iso) ~= "string" then
    return ""
  end
  return iso:match("T(%d%d:%d%d)") or iso
end

--- `pcRemote.lastCommand`: "Shut down · SmartThings · 23:05", or a sentence
--- for a PC that has run nothing yet.
function text.format_last_command(last, lang)
  if type(last) ~= "table" or not last.command then
    return i18n.t(lang, "last_command_none")
  end
  local parts = { i18n.command(lang, last.command) }
  local origin = i18n.origin(lang, last.origin)
  if origin ~= "" then
    parts[#parts + 1] = origin
  end
  local at = text.hhmm(last.at)
  if at ~= "" then
    parts[#parts + 1] = at
  end
  return table.concat(parts, " · ")
end

-- UTF-8 code points, not bytes (a Korean syllable is three): continuation
-- bytes 10xxxxxx are dropped.
local function char_len(s)
  return #(tostring(s):gsub("[\128-\191]", ""))
end

--- How long `pcInfo.summary` may get before the row truncates it.
text.SUMMARY_MAX_CHARS = 24

--- How long the PC has been up: "5분", "2시간 5분", "3일 2시간". Nil under a
--- minute ("0분" reads like a fault).
function text.uptime_text(seconds, lang)
  seconds = tonumber(seconds)
  if not seconds or seconds < 60 then
    return nil
  end
  local minutes = math.floor(seconds / 60)
  if minutes < 60 then
    return i18n.t(lang, "uptime_m", minutes)
  end
  if minutes < 1440 then
    local hours, rest = math.floor(minutes / 60), minutes % 60
    if rest == 0 then
      return i18n.t(lang, "uptime_h", hours)
    end
    return i18n.t(lang, "uptime_hm", hours, rest)
  end
  local days = math.floor(minutes / 1440)
  local hours = math.floor((minutes % 1440) / 60)
  if hours == 0 then
    return i18n.t(lang, "uptime_d", days)
  end
  return i18n.t(lang, "uptime_dh", days, hours)
end

--- How long ago the PC last answered, in the largest unit: "12분 전", "3시간
--- 전", "2일 전"; at least "1분 전". Nil for a missing or negative age (a hub
--- clock that jumped back has no honest answer).
function text.ago_text(seconds, lang)
  seconds = tonumber(seconds)
  if not seconds or seconds < 0 then
    return nil
  end
  local minutes = math.max(1, math.floor(seconds / 60))
  if minutes < 60 then
    return i18n.t(lang, "ago_m", minutes)
  end
  if minutes < 1440 then
    return i18n.t(lang, "ago_h", math.floor(minutes / 60))
  end
  return i18n.t(lang, "ago_d", math.floor(minutes / 1440))
end

-- The first candidate that fits in SUMMARY_MAX_CHARS, else the last one -
-- every caller ends its list with the short form, which always fits.
local function first_fitting(candidates)
  for _, line in ipairs(candidates) do
    if char_len(line) <= text.SUMMARY_MAX_CHARS then
      return line
    end
  end
  return candidates[#candidates]
end

--- `pcInfo.summary`, the status line: "연결됨 · 3일 2시간", "연결됨 · WoL 꺼짐",
--- "응답 없음 · 마지막 확인 12분 전", "연결 안 됨 · 시크릿 불일치". No power word
--- (the pcPower row says it) and no advice (that is `pcInfo.message`'s).
-- Inside SUMMARY_MAX_CHARS the WoL warning comes first (it changes what the
-- switch does), then "앱 업데이트 필요" (the PC app is older than the driver
-- wants), then the adapter's name, then the uptime.
-- @param connection a `pcInfo.connection` value; nil counts as `ok`
-- @param wol_off true when the PC answers but its adapter has WoL disabled
-- @param adapter the chosen adapter's name, optional
-- @param extra optional: `uptime_seconds` for the connected line, `seen_ago`
--   (seconds since the last successful poll, nil when there never was one)
--   for the unreachable one, `app_update` (features.needs_app_update) for
--   the " · 앱 업데이트 필요" ending of the connected one, `app_down` (the
--   connection was refused, client.transport_kind) for "PC 앱 응답 없음" -
--   the PC is on, so "when was it last seen" is not the point, and the line
--   stays one fixed short phrase
function text.status_summary(connection, lang, wol_off, adapter, extra)
  extra = extra or {}
  if connection ~= nil and connection ~= "ok" then
    if extra.app_down == true then
      return i18n.t(lang, "offline_app_down")
    end
    local parts = { i18n.t(lang, "conn_down") }
    local reason = i18n.connection(lang, connection)
    if reason ~= "" then
      parts[#parts + 1] = reason
    end
    local plain = table.concat(parts, " · ")
    if connection == "unreachable" then
      local ago = text.ago_text(extra.seen_ago, lang)
      if ago then
        return first_fitting({ i18n.t(lang, "conn_seen", ago), plain })
      end
    end
    return plain
  end

  local ok = i18n.t(lang, "conn_ok")
  local uptime = text.uptime_text(extra.uptime_seconds, lang)
  local candidates = {}
  -- Every line without the update ending, longest first. With it, the same
  -- lines ending in it come first, so the uptime and then the adapter's name
  -- are dropped before the ending is; the plain ones are the fallback.
  local lines = {}
  local function add(...)
    lines[#lines + 1] = { ... }
  end
  if wol_off == true then
    local warnings = {}
    if type(adapter) == "string" and adapter ~= "" then
      warnings[#warnings + 1] = i18n.t(lang, "wol_off_short_on", adapter)
    end
    warnings[#warnings + 1] = i18n.t(lang, "wol_off_short")
    if uptime then
      add(warnings[1], uptime)
    end
    for _, warning in ipairs(warnings) do
      add(warning)
    end
  else
    if uptime then
      add(uptime)
    end
    add()
  end
  local function join(parts, ending)
    local all = { ok }
    for _, part in ipairs(parts) do
      all[#all + 1] = part
    end
    all[#all + 1] = ending
    return table.concat(all, " · ")
  end
  if extra.app_update == true then
    local ending = i18n.t(lang, "app_update_short")
    for _, parts in ipairs(lines) do
      candidates[#candidates + 1] = join(parts, ending)
    end
  end
  for _, parts in ipairs(lines) do
    candidates[#candidates + 1] = join(parts)
  end
  return first_fitting(candidates)
end

-- The "v" a release tag carries ("v1.1.0"); the row writes its own.
local function bare_version(v)
  return (tostring(v):gsub("^[vV]", ""))
end

-- "1.0.0" -> "1.0": what a user compares against the channel. Anything that
-- is not two dotted numbers is left alone.
local function major_minor(v)
  local major, minor = tostring(v):match("^(%d+)%.(%d+)")
  if major then
    return major .. "." .. minor
  end
  return tostring(v)
end

--- `pcVersion.versions`: "v1.1.0 · 드라이버 1.0", plus " · 업데이트 v1.2.0"
--- while one is out. `service_version` is the status body's, or the last one
--- a successful poll saw; "v?" for a PC never reached.
-- @param update `status.update`; appended only when `available` is set
function text.versions(service_version, lang, update)
  local service
  if type(service_version) == "string" and service_version ~= "" then
    service = bare_version(service_version)
  else
    service = i18n.t(lang, "version_unknown")
  end
  local driver = "?"
  local ok, value = pcall(require, "driver_version")
  if ok and type(value) == "string" and value ~= "" then
    driver = major_minor(value)
  end
  local line = i18n.t(lang, "versions", service, driver)
  update = update or {}
  if update.available == true then
    local latest = update.latest
    if type(latest) == "string" and latest ~= "" then
      return line .. " · " .. i18n.t(lang, "versions_update", bare_version(latest))
    end
    return line .. " · " .. i18n.t(lang, "versions_update_plain")
  end
  return line
end

--- How far away a schedule is, in the largest unit that fits: "곧", "4분 후",
--- "2시간 5분 후", "3일 2시간 후" - the list reaches three days, and "4320분
--- 후" is a number nobody reads as that.
function text.remaining_text(minutes, lang)
  minutes = math.floor(tonumber(minutes) or 0)
  if minutes < 1 then
    return i18n.t(lang, "schedule_soon")
  end
  if minutes < 60 then
    return i18n.t(lang, "schedule_remaining", minutes)
  end
  if minutes < 1440 then
    local hours, rest = math.floor(minutes / 60), minutes % 60
    if rest == 0 then
      return i18n.t(lang, "schedule_remaining_h", hours)
    end
    return i18n.t(lang, "schedule_remaining_hm", hours, rest)
  end
  local days = math.floor(minutes / 1440)
  local hours = math.floor((minutes % 1440) / 60)
  if hours == 0 then
    return i18n.t(lang, "schedule_remaining_d", days)
  end
  return i18n.t(lang, "schedule_remaining_dh", days, hours)
end

--- `pcDefer.summary`: "종료 · 4분 후", or "없음" when nothing is scheduled.
--- Who asked is `pcDefer.origin`'s and `lastCommand`'s.
function text.schedule_summary(schedule, lang)
  schedule = schedule or {}
  if schedule.active ~= true then
    return i18n.t(lang, "schedule_idle")
  end
  local parts = {}
  local command = i18n.command(lang, schedule.command)
  if command ~= "" then
    parts[#parts + 1] = command
  end
  local seconds = math.floor(tonumber(schedule.remaining_seconds) or 0)
  if seconds >= 60 then
    -- Rounded up: "2분 후" at 100 seconds.
    parts[#parts + 1] = text.remaining_text(math.ceil(seconds / 60), lang)
  else
    parts[#parts + 1] = i18n.t(lang, "schedule_soon")
  end
  return table.concat(parts, " · ")
end

--- `pcUser.summary`: "사용 중", "잠김 · 20분", or "꺼짐" while the session block
--- is not exposed. The idle minutes only on "잠김" and from a full minute on;
--- the user name only when the service sent one (a separate opt-in, §3.2).
function text.session_summary(session, lang)
  session = session or {}
  if session.exposed ~= true then
    return i18n.t(lang, "session_off")
  end
  local parts = {}
  if session.locked == true then
    local locked = i18n.t(lang, "session_locked")
    local minutes = math.floor((tonumber(session.idle_seconds) or 0) / 60)
    if minutes >= 1 then
      locked = locked .. " · " .. i18n.t(lang, "session_idle", minutes)
    end
    parts[#parts + 1] = locked
  else
    parts[#parts + 1] = i18n.t(lang, "session_unlocked")
  end
  if type(session.user) == "string" and session.user ~= "" then
    parts[#parts + 1] = session.user
  end
  return table.concat(parts, " · ")
end

-- `pcInfo.message` shows one sentence; highest priority first:
--
--   error            a failed request (from poll.lua's err_kind, `opts.error`)
--   incompatible     protocol mismatch, service or driver too old
--   app_update       the PC app is older than features.RECOMMENDED_SERVICE_VERSION
--   wol_not_ready    WoL is off on the PC's adapter, so `switch on` may not land
--   update_available a newer PC app release is out (the installed one is new
--                    enough)
--   no_secret        the service accepts unauthenticated calls (§3.1)
--   note             a one-off confirmation from a command handler
text.MESSAGE_ORDER = {
  "error", "incompatible", "app_update", "wol_not_ready", "update_available", "no_secret", "note",
}

--- "PC 앱을 v1.2.0 이상으로 업데이트하세요", plus " (최신 v1.2.1)" when the
--- service names a release newer than the one asked for.
function text.app_update_message(status, lang)
  local wanted = features.RECOMMENDED_SERVICE_VERSION
  local line = i18n.t(lang, "app_update", wanted)
  local latest = ((status or {}).update or {}).latest
  if features.version_below(wanted, latest) == true then
    local tag = "v" .. bare_version(latest)
    line = line .. i18n.t(lang, "app_update_latest", tag)
  end
  return line
end

--- The single `pcInfo.message` for a status body, by MESSAGE_ORDER.
-- @param opts `lang`, `error` (a ready-made message that outranks the body),
--   `note` (a confirmation shown only when nothing is wrong)
function text.status_message(status, opts)
  opts = opts or {}
  if type(opts.error) == "string" and opts.error ~= "" then
    return opts.error
  end

  status = status or {}
  local lang = opts.lang

  if features.needs_app_update(status) then
    -- Before WoL: a PC app this old is why commands are refused, and the
    -- summary row can only hint at it.
    return text.app_update_message(status, lang)
  end
  if wolinfo.wol_off(status) then
    -- §6.4: the packet still goes; this says why it may not work, naming the
    -- adapter to go and open when the service gave one.
    local adapter = wolinfo.wol_adapter(status)
    if adapter then
      return i18n.t(lang, "wol_not_ready_on", adapter)
    end
    return i18n.t(lang, "wol_not_ready")
  end
  if (status.update or {}).available == true then
    local latest = status.update.latest
    if type(latest) == "string" and latest ~= "" then
      return i18n.t(lang, "update_available", latest)
    end
    return i18n.t(lang, "update_available_plain")
  end
  if status.secret_set == false then
    -- §3.1: still a healthy connection, so a warning here, not in `connection`.
    return i18n.t(lang, "no_secret")
  end

  if type(opts.note) == "string" and opts.note ~= "" then
    return opts.note
  end
  return ""
end

return text
