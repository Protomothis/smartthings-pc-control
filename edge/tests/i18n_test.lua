-- The driver's strings (src/i18n.lua). Most sentences are not asserted word
-- for word: the table must be complete (every key the driver asks for, both
-- languages, the same format verbs), the lookups must degrade gracefully, and
-- the few rules a wording has to keep are checked as rules. A handful of
-- goldens pin what the formatting produces.

local h = require "helpers"
local i18n = require "i18n"

local T = {}

local LANGS = { "ko", "en" }

-- The string table is local to i18n.lua; i18n.t closes over it.
local function strings_table()
  for i = 1, 16 do
    local name, value = debug.getupvalue(i18n.t, i)
    if name == nil then
      break
    end
    if name == "STRINGS" then
      return value
    end
  end
  error("i18n.t has no STRINGS upvalue")
end

local function verbs(text)
  local out = {}
  for verb in text:gmatch("%%[%-%d%.]*[sdq%%]") do
    out[#out + 1] = verb
  end
  return out
end

local function src_files()
  local dir = h.SRC_DIR
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(dir)) do
      names[#names + 1] = name
    end
    for _, sub in ipairs({ "device", "handlers", "model" }) do
      local ok, listed = pcall(host.listdir, dir .. "/" .. sub)
      for _, name in ipairs(ok and listed or {}) do
        names[#names + 1] = sub .. "/" .. name
      end
    end
  else
    local pipe = io.popen('cd "' .. dir .. '" && find . -name "*.lua"')
    for name in pipe:lines() do
      names[#names + 1] = name:gsub("^%./", "")
    end
    pipe:close()
  end
  local out = {}
  for _, name in ipairs(names) do
    if name:match("%.lua$") then
      local path = dir .. "/" .. name
      local text
      if host and host.readfile then
        text = host.readfile(path)
      else
        local f = io.open(path, "r")
        text = f:read("a")
        f:close()
      end
      out[name] = text
    end
  end
  return out
end

function T.test_resolve_defaults_to_korean()
  h.assert_equal(i18n.resolve("ko"), "ko")
  h.assert_equal(i18n.resolve("en"), "en")
  -- §6.8: a driver cannot read the hub locale; the project is Korean-first.
  for _, other in ipairs({ "auto", "fr" }) do
    h.assert_equal(i18n.resolve(other), "ko")
    h.assert_equal(i18n.t(other, "no_secret"), i18n.t("ko", "no_secret"))
  end
  h.assert_equal(i18n.resolve(nil), "ko")
end

function T.test_every_string_has_both_languages_with_the_same_verbs()
  local count = 0
  for key, entry in pairs(strings_table()) do
    count = count + 1
    for _, lang in ipairs(LANGS) do
      h.assert_true(type(entry[lang]) == "string" and entry[lang]:match("%S") ~= nil, key .. " has no " .. lang)
    end
    h.assert_deep_equal(verbs(entry.en), verbs(entry.ko), key .. ": the format verbs differ")
  end
  h.assert_true(count > 100, "found only " .. count .. " strings; is the upvalue the table?")
end

-- Every key the driver looks up exists: the literal ones in src/ (any
-- i18n.t(...) call's quoted keys), and the ones a refusal, an error note or a
-- poll failure hands to i18n.t as a variable.
function T.test_every_key_the_driver_uses_exists()
  local seen = 0
  for name, text in pairs(src_files()) do
    if name ~= "i18n.lua" then
      for call in text:gmatch("i18n%.t(%b())") do
        for key in call:gmatch('"([%w_]+)"') do
          if key ~= "ko" and key ~= "en" then
            seen = seen + 1
            h.assert_true(i18n.has(key), name .. " asks for " .. key)
          end
        end
      end
    end
  end
  h.assert_true(seen > 50, "found only " .. seen .. " literal lookups; is the pattern stale?")
  for _, key in ipairs({
    -- features.refusal / features.error_note / handlers/preset.lua
    "unreachable", "needs_service", "media_disabled", "feature_missing", "no_user",
    "notify_disabled", "action_failed", "preset_empty",
    -- poll.lua: the failure kinds it words
    "unauthorized", "forbidden", "ratelimited", "badrequest",
  }) do
    h.assert_true(i18n.has(key), "missing string " .. key)
  end
end

-- Each enum value the driver puts into a sentence has a label in both
-- languages, and anything else degrades to itself (a newer service's value
-- still shows something) or to "" for nothing at all.
function T.test_enum_labels_and_fallbacks()
  local state = require "state"
  local families = {
    command = { i18n.command, { "shutdown", "forceshutdown", "restart", "hibernate", "suspend", "lock",
      "turnscreenoff", "turnscreenon", "ping" } },
    power = { i18n.power, { "on", "sleeping", "hibernated", "off", "waking", "shuttingDown", "unknown" } },
    origin = { i18n.origin, { "ui", "remote", "telegram", "smartthings" } },
    connection = { i18n.connection, { "ok", "unauthorized", "unreachable", "incompatible" } },
  }
  for family, f in pairs(families) do
    local label, values = f[1], f[2]
    for _, value in ipairs(values) do
      for _, lang in ipairs(LANGS) do
        local text = label(lang, value)
        h.assert_true(text ~= value and text ~= "", family .. " " .. value .. " has no " .. lang .. " label")
      end
    end
    h.assert_equal(label("en", "somethingnew"), "somethingnew", family .. " passes an unknown value through")
    h.assert_equal(label("ko", nil), "", family .. " of nil")
  end
  h.assert_equal(i18n.origin("ko", ""), "")

  -- #93: the note a command held back by a power transition leaves on
  -- `pcInfo.message`; every busy `lastAction` value needs one.
  for _, action in ipairs(state.BUSY_ACTIONS) do
    for _, lang in ipairs(LANGS) do
      h.assert_contains(i18n.busy(lang, action), "·", action .. " (" .. lang .. ")")
    end
  end
  for _, bogus in ipairs({ "none", "shutdown", "", "busy" }) do
    h.assert_equal(i18n.busy("ko", bogus), "")
  end
  h.assert_equal(i18n.busy("ko", nil), "")

  h.assert_equal(i18n.t("ko", "no_such_key"), "no_such_key")
  h.assert_false(i18n.has("no_such_key"))
end

-- Every string with a %s or %d shows what it is given.
function T.test_formatted_strings_carry_their_arguments()
  for key, entry in pairs(strings_table()) do
    local args = {}
    for i, verb in ipairs(verbs(entry.ko)) do
      if verb ~= "%%" then
        args[#args + 1] = verb:sub(-1) == "d" and (40 + i) or ("ARG" .. i)
      end
    end
    if #args > 0 then
      for _, lang in ipairs(LANGS) do
        local text = i18n.t(lang, key, table.unpack(args))
        for _, arg in ipairs(args) do
          h.assert_contains(text, tostring(arg), key .. " (" .. lang .. ")")
        end
      end
    end
  end
end

-- The wording rules the strings have to keep.
function T.test_the_wording_rules()
  -- Both end up as `connection = unauthorized` (§3.1), so the message is the
  -- only thing telling the user which of the two to fix.
  h.assert_contains(i18n.t("en", "forbidden"), "allow-list")
  h.assert_contains(i18n.t("en", "unauthorized"), "Secret")
  for _, lang in ipairs(LANGS) do
    h.assert_true(i18n.t(lang, "forbidden") ~= i18n.t(lang, "unauthorized"), lang)
    -- §6.5: the multi-PC warning names what to fix.
    h.assert_contains(i18n.t(lang, "hostname_mismatch", "LAPTOP-XYZ"), "MachineGuid")
    -- #87: the summary row's WoL notice is a clipped form of the message.
    h.assert_true(#i18n.t(lang, "wol_off_short") < #i18n.t(lang, "wol_not_ready"), lang)
    -- The short connection label is not the long sentence `message` carries.
    h.assert_true(i18n.connection(lang, "unauthorized") ~= i18n.t(lang, "unauthorized"), lang)
  end
  -- #87: a summary sits next to a label the app already draws ("예약 요약",
  -- "세션") and the phone truncates the value, so it never says it again.
  local labels = { schedule = { ko = "예약", en = "chedule" }, session = { ko = "세션", en = "ession" } }
  for key in pairs(strings_table()) do
    local row = key:match("^(schedule)_") or key:match("^(session)_")
    if row and (key:match("_idle$") or key:match("_remaining") or key:match("_soon$") or key:match("_off$")) then
      for _, lang in ipairs(LANGS) do
        local text = i18n.t(lang, key, 1, 2)
        h.assert_nil(text:find(labels[row][lang], 1, true), key .. " (" .. lang .. ") repeats its row label: " .. text)
      end
    end
  end
end

-- What the formatting produces, in both languages.
function T.test_goldens()
  for _, g in ipairs({
    { "wake_failed", {}, "깨우기 실패: WoL 응답 없음", "Wake failed: no WoL response" },
    { "incompatible_service", { "1.1.0" }, "서비스 v1.1.0 이상 필요", "Requires service v1.1.0 or newer" },
    -- #97: the WoL warning names the adapter the service chose.
    { "wol_not_ready_on", { "이더넷" }, "이더넷 어댑터에 WoL이 꺼져 있습니다 · SmartThings 탭 확인",
      "Wake-on-LAN is off on 이더넷 · check the SmartThings tab" },
    { "wol_off_short_on", { "이더넷" }, "WoL 꺼짐 (이더넷)", "WoL off (이더넷)" },
    -- #89: the longest presets in the largest unit that fits.
    { "schedule_remaining", { 4 }, "4분 후", "in 4 min" },
    { "schedule_remaining_hm", { 1, 30 }, "1시간 30분 후", "in 1 h 30 min" },
    { "schedule_remaining_dh", { 1, 3 }, "1일 3시간 후", "in 1 d 3 h" },
    -- #87: the row writes the "v" itself (state.versions strips the tag's).
    { "versions", { "1.1.0", "1.0" }, "v1.1.0 · 드라이버 1.0", "v1.1.0 · Driver 1.0" },
    { "update_available", { "v1.2.0" }, "서비스 업데이트 v1.2.0 사용 가능", "Service update v1.2.0 available" },
    { "discovery_found", { 2 }, "PC 2대를 찾았습니다", "Found 2 PC(s)" },
    { "pc_label", { "DESKTOP-ABC" }, "DESKTOP-ABC 컴퓨터", "DESKTOP-ABC PC" },
  }) do
    local key, args, ko, en = g[1], g[2], g[3], g[4]
    h.assert_equal(i18n.t("ko", key, table.unpack(args)), ko, key .. " (ko)")
    h.assert_equal(i18n.t("en", key, table.unpack(args)), en, key .. " (en)")
  end
  h.assert_equal(i18n.command("ko", "forceshutdown"), "강제 종료")
  h.assert_equal(i18n.command("en", "shutdown"), "Shut down")
  h.assert_equal(i18n.power("en", "shuttingDown"), "Shutting down")
  h.assert_equal(i18n.origin("ko", "remote"), "SmartThings 명령")
  h.assert_equal(i18n.connection("ko", "unauthorized"), "시크릿 불일치")
end

return T
