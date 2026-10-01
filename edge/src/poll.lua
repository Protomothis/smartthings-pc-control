-- Polling / health (design doc §6.1) and the device-layer glue that turns the
-- pure event records from state.lua into `device:emit_event` calls.
--
-- This module owns the per-device runtime state (a plain table in a device
-- field) and the poll timer. wol.lua reuses the glue functions so the wake
-- timeout can update the device without knowing about st.capabilities.

local caps = require "caps"
local client = require "client"
local features = require "features"
local i18n = require "i18n"
local profiles = require "profiles"
local state = require "state"

local poll = {}

poll.STATE_FIELD = "pc_state"
poll.TIMER_FIELD = "poll_timer"
poll.START_TIMER_FIELD = "poll_start_timer"
poll.MAC_FIELD = "wol_mac"
-- #82: the last `pcRemote.lastAction` value emitted for this device.
poll.ACTION_FIELD = "last_action"
-- #93 follow-up: a `lastAction` value that changed is re-sent once more on the
-- next poll, and this is what remembers that the repeat is owed. Measured on
-- the hub: a forced event can be dropped between hub and cloud, and the one
-- that must not be is the `none` that says a transition is over.
poll.ACTION_CONFIRM_FIELD = "last_action_confirm"
-- #84: the `pcDefer.planCommand` the user picked for the schedule row.
poll.PLAN_FIELD = "plan_command"
-- #85: which generation of capability ids this device's rows were painted for.
-- A renamed capability (pcRun -> pcExec, pcPlan -> pcPlanner) starts with
-- every attribute unset on the hub, so the persisted "already painted" fields
-- would otherwise skip a device that has been migrated (platform notes "허브의 정의 캐시").
poll.ROWS_FIELD = "rows_painted"
-- #92: the last `service_version` a successful poll saw. Persisted, because the
-- whole point is to still know it after the hub restarts while the PC is off.
poll.SERVICE_VERSION_FIELD = "service_version"
-- #102: when the PC last answered a poll, in epoch seconds (`poll.clock`).
-- Persisted for the same reason as the version: "마지막 확인 12분 전" is read
-- while the PC is off, which is when a hub restart would otherwise forget it.
poll.LAST_SEEN_FIELD = "last_seen"
-- #102: how stale the stored time may get before a successful poll writes it
-- again. The row only ever says whole minutes, and a persisted field is a hub
-- write, so a 10-second poll interval does not become six writes a minute.
poll.LAST_SEEN_STEP = 60
-- The generation stamp itself. #90: it starts fresh at the first channel
-- release, because no device outside development ever carried an older one -
-- every device installed from the channel paints its rows once on `added` and
-- then matches. Bump it (to "2", "3", …) whenever a capability id changes or a
-- new one is added, so `ensure_rows` repaints every already-installed device.
-- #107: "2" - the move to `pc.v2` brings new capabilities whose rows start
-- unset (the standard audio/media ones now, pcPreset/pcActivity (#114) and the
-- awake/battery components with the rest of edge-v1.1.0).
-- "3" - the move to `pc.v3` (pcMessage in place of the standard notification
-- pair). pcMessage itself has no attribute to paint, but a device on its new
-- profile starts with an empty cloud record, and the rule is one generation per
-- capability set, so every row goes out once more.
-- "4" - the move to `pc.v4` (pcNotify, send only, in place of pcMessage).
-- Again nothing new to paint, again a new profile and a new capability set.
-- "5" - the move to `pc.v5` (pcToast in place of pcNotify), whose
-- `lastMessage` row starts unset.
-- "6" - the move to `pc.v6` (#123: pcApps in place of pcActivity), whose
-- `summary` row starts unset.
poll.ROWS_VERSION = "6"
poll.WOL_READY_FIELD = "wol_ready"
-- #97: the name of the adapter the service chose for WoL, so the message the
-- wake sequence writes can name it while the PC is off and there is no status
-- body to read (state.wol_adapter).
poll.WOL_ADAPTER_FIELD = "wol_adapter"
poll.DEFAULT_INTERVAL = 30
-- First service release that speaks protocol 1 (§3).
poll.MIN_SERVICE_VERSION = "1.1.0"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  -- Loading poll.lua outside the hub (tests, syntax check) must not fail.
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

-- Resolve a capability object from an id produced by state.lua. Custom
-- capabilities do not exist until the account owner creates them (#72), so a
-- miss is logged and skipped rather than raised.
local function capability_for(id)
  local ok, capabilities = pcall(require, "st.capabilities")
  if not ok then
    return nil
  end
  if id == state.CAP_SWITCH then
    return capabilities.switch
  end
  local found, cap = pcall(function() return capabilities[id] end)
  if found then
    return cap
  end
  return nil
end

--- `pcInfo.lastSeen`: the hub's local clock time of the last good poll.
--- A tile the user glances at wants "14:05:12", not an ISO timestamp, and the
--- date is never interesting for a value that is at most a few minutes old.
function poll.now()
  return os.date("%H:%M:%S")
end

--- #102: epoch seconds for the last-seen bookkeeping. `deps.now` wins when a
--- caller passes one (the same injection discovery.lua and push.lua take), so
--- tests never read the real clock.
function poll.clock(deps)
  return ((deps or {}).now or poll.wallclock)()
end

--- The clock `poll.clock` falls back to when no `deps.now` is given: what the
--- emit funnel's budget guard and the resync windows read (they are reached
--- from every command handler, none of which carries `deps`). A test replaces
--- it and puts it back.
poll.wallclock = os.time

function poll.lang(device)
  return ((device or {}).preferences or {}).language
end

--- Runtime state for `device`, created on first use.
function poll.get_state(device)
  return device:get_field(poll.STATE_FIELD) or state.new()
end

function poll.set_state(device, s)
  device:set_field(poll.STATE_FIELD, s)
end

--- Re-exported so wol.lua does not have to require state.lua as well.
function poll.transition(s, event, arg)
  return state.transition(s, event, arg)
end

-- #86: what a record's `force = true` becomes on the hub. An event whose value
-- equals the current one is dropped by the platform, and the app - which is
-- waiting for exactly that attribute to change - keeps its spinner up until it
-- gives up with an error. Cancelling with nothing scheduled and re-picking the
-- value a list already shows are both that case (measured on the phone
-- 2026-09-22, platform notes "상세 화면(detailView) 위젯"), so every emit that answers an app command is forced.
poll.FORCE = { state_change = true }

-- #86: the rows the schedule list is bound to. `schedule` and `cancel` are
-- answered on them, and cancelling with nothing scheduled leaves every one of
-- them exactly as it was, which is the case that used to spin and then fail.
poll.SCHEDULE_ROWS = {
  [caps.SCHEDULE .. ".status"] = true,
  [caps.SCHEDULE .. ".active"] = true,
  [caps.SCHEDULE .. ".summary"] = true,
}

--- Mark every event as forced (a repaint after a profile change: the hub
--- de-duplicates unchanged values, but the cloud record of the new profile is
--- empty until it receives them).
function poll.force_all(events)
  for _, e in ipairs(events or {}) do
    e.force = true
  end
  return events
end

--- The key `force_rows` matches an event record by: `<cap>.<attr>` on the main
--- component, `<component>/<cap>.<attr>` on any other (#107 - the `awake`
--- component's switch is not the main switch).
function poll.row_key(e)
  local key = tostring(e.cap) .. "." .. tostring(e.attr)
  if e.component ~= nil and e.component ~= "main" then
    return tostring(e.component) .. "/" .. key
  end
  return key
end

--- Mark the events whose row key (`poll.row_key`) is in `keys` as forced (#86).
-- Returns the same list, so it can wrap a call.
function poll.force_rows(events, keys)
  if type(keys) ~= "table" then
    return events
  end
  for _, e in ipairs(events or {}) do
    if keys[poll.row_key(e)] then
      e.force = true
    end
  end
  return events
end

--- #107: the component object `device` has for `id`, or nil when its profile
--- has no such component.
--
-- `device.profile.components` is keyed by component id on the hub; a list of
-- `{ id = … }` (the shape some tests build) is searched as well. nil is the
-- normal answer for a device that is still on a profile without the
-- component - a v1 profile before its migration, or a desktop's profile for a
-- battery row - and the event is then skipped rather than emitted into a
-- component the hub would reject.
function poll.component(device, id)
  local components = (((device or {}).profile) or {}).components
  if type(components) ~= "table" then
    return nil
  end
  if type(components[id]) == "table" then
    return components[id]
  end
  for _, component in ipairs(components) do
    if type(component) == "table" and component.id == id then
      return component
    end
  end
  return nil
end

--- Emit a list of `{ cap, attr, value, force?, component? }` records.
-- `force` marks an emit that answers a command from the app: it goes out with
-- `{ state_change = true }` so the platform delivers it even when the value did
-- not change. Ordinary poll updates stay unforced, and one whose value equals
-- what this run last emitted on the row is not emitted at all (`SENT_FIELD`,
-- the event budget).
-- #107: `component` names a component other than `main` (`awake`, `battery`).
-- Such an event goes out with `emit_component_event`, and only when the
-- device's profile has that component.
-- Rows this driver run has already sent once with `state_change` (not
-- persisted: a new run starts empty). Measured on the Dev channel with 1.1.0
-- (2026-09-30): after a v1 -> v2 profile move the hub already held
-- `audioMute.mute = "unmuted"` and dropped every unforced "unmuted" as
-- unchanged, while the cloud had never stored it - the row stayed null (the
-- app then warns that the device does not report all its state) until a
-- forced emit. So the first emit of every row in a run is forced; after that
-- the hub's own dedup is fine, because the cloud has seen the value once.
poll.FIRST_FIELD = "rows_forced_this_run"

local function first_emit(device, key)
  local seen
  pcall(function() seen = device:get_field(poll.FIRST_FIELD) end)
  if type(seen) ~= "table" then
    seen = {}
  end
  if seen[key] then
    return false
  end
  seen[key] = true
  pcall(function() device:set_field(poll.FIRST_FIELD, seen) end)
  return true
end

--------------------------------------------------------------------------------
-- The event budget (platform notes "이벤트 예산(rate limit)")
--------------------------------------------------------------------------------

-- Measured on the hub 2026-10-01 (logcat + the cloud's history): the platform
-- counts every `emit_event` against a per-device budget whether or not the hub
-- then drops it as unchanged. Four mute/unmute commands 1.5-2 s apart made
-- ~230 emits in 8 s; the cloud stored the first batch and then dropped EVERY
-- event of the device for ~40 s - while the hub's own state cache took the
-- dropped values, so the unforced repeats of the right value that followed
-- were deduplicated by the hub and the cloud stayed wrong.
--
-- So the driver deduplicates itself: `SENT_FIELD` holds, per row key, a
-- canonical serialization of the last value this driver run emitted, and an
-- unforced record whose value matches is not emitted at all. Forced records
-- always go out (they answer a command, or repaint a new profile) and update
-- the cache. In memory only: a new run starts empty, and its first emit of
-- every row is forced anyway (`FIRST_FIELD`).
poll.SENT_FIELD = "rows_sent_this_run"

-- The budget guard: how many emits in how many seconds the driver itself may
-- make before it says so in logcat. Log only - nothing is dropped here.
poll.BUDGET_EVENTS = 20
poll.BUDGET_SECONDS = 10
poll.BUDGET_FIELD = "emit_budget"

-- Turn a value into a string that is equal exactly when the value is: tables
-- by sorted keys, numbers so that 30 and 30.0 agree, strings quoted so that
-- "1" and 1 do not.
local function signature(value, depth)
  local kind = type(value)
  if kind == "string" then
    return string.format("%q", value)
  end
  if kind == "number" then
    return "n" .. string.format("%.17g", value)
  end
  if kind == "boolean" or kind == "nil" then
    return tostring(value)
  end
  if kind ~= "table" then
    return kind
  end
  depth = (depth or 0) + 1
  if depth > 8 then
    return "{…}"
  end
  local keys = {}
  for k in pairs(value) do
    keys[#keys + 1] = k
  end
  table.sort(keys, function(a, b)
    local ta, tb = type(a), type(b)
    if ta ~= tb then
      return ta < tb
    end
    if ta == "number" or ta == "string" then
      return a < b
    end
    return tostring(a) < tostring(b)
  end)
  local parts = {}
  for _, k in ipairs(keys) do
    parts[#parts + 1] = signature(k, depth) .. "=" .. signature(value[k], depth)
  end
  return "{" .. table.concat(parts, ",") .. "}"
end
poll.signature = signature

local function sent_cache(device)
  local sent
  pcall(function() sent = device:get_field(poll.SENT_FIELD) end)
  if type(sent) ~= "table" then
    sent = {}
    pcall(function() device:set_field(poll.SENT_FIELD, sent) end)
  end
  return sent
end

--- Forget what this run has emitted for `device`, so every row goes out again
--- once (a profile change: the cloud record of the new profile is empty).
-- #129 (W2): and start a new "first emit is forced" generation (`FIRST_FIELD`)
-- with the hub's record no longer trusted (`SEED_FIELD`): a row the repaint
-- does not carry itself is forced by the poll that follows it, instead of
-- that poll forcing every row a second time.
function poll.forget_sent(device)
  pcall(function()
    device:set_field(poll.SENT_FIELD, nil)
    device:set_field(poll.FIRST_FIELD, nil)
    device:set_field(poll.SEED_FIELD, true)
  end)
end

-- #129 (W2): the first poll of a run used to send every row, forced - about
-- forty events in one go on every driver start, hub reboot or driver update,
-- although nothing about the device's profile had changed and the cloud
-- already held all of them. The hub keeps what this driver emitted last in a
-- persistent state cache (`device:get_latest_state`, lua_libs st/device.lua:
-- "persisted through restart"), so on such a start a row whose value is the
-- one in that cache is taken as already sent and not emitted at all.
--
-- What is kept, and why:
--   - after a profile change or a new generation of rows (`poll.repaint`,
--     which sets this field) the cache is no evidence: the new profile's cloud
--     record is empty (the 2026-09-30 v1 -> v2 measurement), so every row's
--     first emit is forced again (`FIRST_FIELD`)
--   - the rows whose loss the user notices (`RESYNC_ROWS`) are never seeded:
--     they go out forced once per run whatever the cache says, because the
--     cache can hold a value the cloud never stored (an event lost to the
--     budget, or a flash write lost to a power cut)
--   - a value that differs from the cache is the first emit of the run, forced
--   - an app child (#123) is never seeded: its one row is forced once per run
poll.SEED_FIELD = "rows_seed_off"

local function loss_matters(key)
  for _, k in ipairs(poll.RESYNC_ROWS or {}) do
    if k == key then
      return true
    end
  end
  return false
end

--- True when this run may take the hub's state cache as "already sent" for
--- `device` (see `SEED_FIELD`).
function poll.seeding(device)
  if type(device) ~= "table" then
    return false
  end
  local key = device.parent_assigned_child_key
  if type(key) == "string" and key ~= "" then
    return false
  end
  local off
  pcall(function() off = device:get_field(poll.SEED_FIELD) end)
  return off ~= true
end

-- The value the hub's state cache holds for one row, or nil.
local function cached_state(device, cap, e, component)
  local value
  pcall(function()
    value = device:get_latest_state(component and component.id or "main",
      cap.ID or e.cap, e.attr)
  end)
  return value
end

--- The value this run last emitted on `key` (`poll.row_key`), or nil.
function poll.sent_value(device, key)
  local entry = sent_cache(device)[key]
  if entry then
    return entry.value
  end
  return nil
end

-- Count one emit against the device's budget window and warn once per window
-- when the driver goes over it.
local function spend(device, log)
  local now = poll.clock()
  local budget
  pcall(function() budget = device:get_field(poll.BUDGET_FIELD) end)
  if type(budget) ~= "table" or now < budget.start or now - budget.start >= poll.BUDGET_SECONDS then
    budget = { start = now, count = 0, warned = false }
    pcall(function() device:set_field(poll.BUDGET_FIELD, budget) end)
  end
  budget.count = budget.count + 1
  if budget.count > poll.BUDGET_EVENTS and not budget.warned then
    budget.warned = true
    log.warn(string.format("event budget: %s emitted more than %d events in %d s",
      tostring((device or {}).id), poll.BUDGET_EVENTS, poll.BUDGET_SECONDS))
  end
end

function poll.emit(device, events)
  local log = logger()
  local sent = sent_cache(device)
  for _, e in ipairs(events or {}) do
    local cap = capability_for(e.cap)
    local attr = cap and cap[e.attr]
    local component
    if e.component ~= nil and e.component ~= "main" then
      component = poll.component(device, e.component)
    elseif e.component == nil and features.MEDIA_CAPS[e.cap] then
      -- #118: the alternative profile layout keeps the media group on a
      -- component of its own. The record stays main-shaped (its row key does
      -- not change); only where it is emitted does.
      component = poll.component(device, features.MEDIA_COMPONENT)
    end
    if not attr then
      log.debug(string.format("capability %s.%s not available, skipped", tostring(e.cap), tostring(e.attr)))
    elseif e.component ~= nil and e.component ~= "main" and not component then
      log.debug(string.format("component %s not in the profile, %s.%s skipped",
        tostring(e.component), tostring(e.cap), tostring(e.attr)))
    else
      local key = poll.row_key(e)
      local sig = signature(e.value)
      local last = sent[key]
      if not last and not e.force and not loss_matters(key) and poll.seeding(device) then
        -- #129 (W2): the first time this run meets the row, and the hub's
        -- persisted record already says exactly this (`SEED_FIELD`).
        local cached = cached_state(device, cap, e, component)
        if cached ~= nil and signature(cached) == sig then
          last = { sig = sig, value = e.value, cap = e.cap, attr = e.attr, component = e.component }
          sent[key] = last
        end
      end
      -- Unchanged and not answering anything: the hub would drop it, but it
      -- would still cost budget (see above), so it is not emitted. Not even
      -- logged - that is most rows of every poll.
      if e.force or not last or last.sig ~= sig then
        local ok, err = pcall(function()
          local force = e.force or first_emit(device, key)
          local event = force and attr(e.value, poll.FORCE) or attr(e.value)
          if component then
            device:emit_component_event(component, event)
          else
            device:emit_event(event)
          end
        end)
        if ok then
          -- The record as well as its signature: `resync` re-sends it.
          sent[key] = { sig = sig, value = e.value, cap = e.cap, attr = e.attr,
            component = e.component }
          spend(device, log)
        else
          log.warn(string.format("emit %s failed: %s", key, tostring(err)))
        end
      end
    end
  end
end

--- #107: what the last status said about the v1.2.0 features
--- (`features.remember`), or nil when no status has been read in this run.
function poll.extras(device)
  return poll.get_state(device).extras
end

--- Emit `switch` + `powerState` for a runtime state (§6.2).
-- #93: `force` for the emit that answers a `switch off` the driver refused to
-- forward. The switch is derived from the power state and a PC that is already
-- shutting down still reads "on", so without `state_change` the toggle the user
-- just flipped would keep its spinner and then fail (platform notes
-- "상세 화면(detailView) 위젯").
function poll.emit_power(device, s, force)
  local power = (s or {}).power_state or state.UNKNOWN
  poll.emit(device, {
    { cap = state.CAP_SWITCH, attr = "switch", value = state.switch_for(power),
      force = force == true },
    { cap = caps.POWER_STATE, attr = "powerState", value = power,
      force = force == true },
  })
end

--- Emit only `pcInfo.message`.
function poll.emit_message(device, message)
  poll.emit(device, { { cap = caps.STATUS, attr = "message", value = message or "" } })
end

--- #93: a one-off notice on both pcInfo rows, as the answer to a command the
--- driver refused to forward ("종료 진행 중 · 끝난 뒤 다시 시도").
--
-- `message` is the sentence row and `summary` is the line the user is already
-- looking at, so the notice goes on both - the point of it is to be seen at the
-- moment the command does nothing. Forced, like every emit that answers a
-- command (platform notes "상세 화면(detailView) 위젯"). Neither row is repainted
-- here afterwards: the next poll (at most `pollInterval` away, and immediately
-- after the transition ends) writes the normal wording back.
function poll.emit_note(device, note)
  poll.emit(device, {
    { cap = caps.STATUS, attr = "message", value = note or "", force = true },
    { cap = caps.STATUS, attr = "summary", value = note or "", force = true },
  })
end

--- Emit `pcInfo.connection` + `pcInfo.message` + `pcInfo.summary` (#78).
--- The summary is the only status row the detail view still shows, so a failed
--- poll has to rewrite it as well. #82: the power word is not in it any more -
--- the `pcPower` row right above says that.
-- #85: the `versions` row goes out here as well. A row that was never emitted
-- reads as "-" (platform notes "상세 화면(detailView) 위젯"), and the driver half is still the answer to
-- "did my update land?".
-- #92: the service half is the last version a successful poll saw, not "v?".
-- A PC that is off has not changed its version, so the row keeps saying
-- "v1.1.0 · 드라이버 1.0" instead of losing half of itself every night;
-- `state.versions` falls back to "v?" only for a PC that never answered.
-- The update suffix is deliberately not remembered: whether a newer service is
-- out is something only a live answer can claim.
-- #102: the summary says when the PC last answered ("응답 없음 · 마지막 확인
-- 12분 전"), from the time `remember_last_seen` kept; a PC that never answered
-- keeps "연결 안 됨 · 응답 없음".
-- @param deps optional; `deps.now` replaces the clock (poll.clock)
function poll.emit_connection(device, connection, message, deps)
  local lang = poll.lang(device)
  local versions = state.versions(poll.last_service_version(device), lang)
  local seen = poll.last_seen(device)
  local seen_ago = seen and (poll.clock(deps) - seen) or nil
  poll.emit(device, {
    { cap = caps.STATUS, attr = "connection", value = connection },
    { cap = caps.STATUS, attr = "message", value = message or "" },
    { cap = caps.STATUS, attr = "summary",
      value = state.status_summary(connection, lang, nil, nil, { seen_ago = seen_ago }) },
    -- #86: the row lives on its own capability now; pcInfo keeps the attribute
    -- (and the emit) because its definition cannot change without a rename.
    { cap = caps.VERSION, attr = "versions", value = versions },
    { cap = caps.STATUS, attr = "versions", value = versions },
  })
end

--- The last `service_version` a successful poll saw (#92), or nil when this
--- device has never answered one.
function poll.last_service_version(device)
  local seen
  pcall(function() seen = device:get_field(poll.SERVICE_VERSION_FIELD) end)
  if type(seen) == "string" and seen ~= "" then
    return seen
  end
  return nil
end

--- Remember `status.service_version` (#92). Returns true when it changed.
--
-- Written only on a change: a persisted field is a hub write, and the version
-- moves once per service update while the poll runs every 30 seconds.
function poll.remember_service_version(device, body)
  local version = (body or {}).service_version
  if type(version) ~= "string" or version == "" then
    return false
  end
  if poll.last_service_version(device) == version then
    return false
  end
  pcall(function()
    device:set_field(poll.SERVICE_VERSION_FIELD, version, { persist = true })
  end)
  return true
end

--- When the PC last answered (#102), in epoch seconds, or nil when this device
--- never has.
function poll.last_seen(device)
  local seen
  pcall(function() seen = device:get_field(poll.LAST_SEEN_FIELD) end)
  seen = tonumber(seen)
  if seen and seen > 0 then
    return seen
  end
  return nil
end

--- Remember that the PC answered just now (#102). Returns true when the field
--- was written.
--
-- Only once the stored time is `LAST_SEEN_STEP` old (or in the future - a hub
-- clock that was set back): the row counts in whole minutes, and every write of
-- a persisted field is a hub write.
function poll.remember_last_seen(device, deps)
  local now = poll.clock(deps)
  local seen = poll.last_seen(device)
  if seen and now >= seen and now - seen < poll.LAST_SEEN_STEP then
    return false
  end
  pcall(function()
    device:set_field(poll.LAST_SEEN_FIELD, now, { persist = true })
  end)
  return true
end

--- Emit `pcRemote.lastAction` and remember it (#82, #84).
--
-- #84: the only value this is ever called with is `none`. Closing the detail
-- view's command list without picking anything sends the row's CURRENT value
-- as the `execute` argument (measured on the phone 2026-09-22, platform notes "상세 화면(detailView) 위젯"), so the
-- row has to rest on a value that is both a valid argument and a no-op -
-- otherwise the cloud rejects the command with "network or server error"
-- before it ever reaches the hub. What ran is told by `lastCommand` instead.
-- Anything that is not a `lastAction` enum value is coerced to `none` rather
-- than emitted, because the hub rejects a value outside the enum.
-- @param action a `lastAction` enum value
-- @param force #86: true when this emit answers an `execute` from the app. The
--   row rests on `none` and therefore never changes value, so without
--   `state_change` the platform drops the event and the app spins until it
--   errors out (platform notes "상세 화면(detailView) 위젯").
function poll.emit_action(device, action, force)
  local value = state.is_action(action) and action or state.ACTION_NONE
  pcall(function() device:set_field(poll.ACTION_FIELD, value, { persist = true }) end)
  poll.emit(device, {
    { cap = caps.COMMAND, attr = "lastAction", value = value, force = force == true },
  })
  return value
end

--- #93: the value the command list should be resting on right now.
--
-- `none` ("명령 선택…") normally, and one of the five `busyX` values while the
-- PC is shutting down, going to sleep or coming up (state.is_transitioning).
-- The app has no disabled row, so this is how the list says "in progress".
function poll.resting_action(device)
  return state.resting_action(poll.get_state(device))
end

--- #86: answer an `execute` on the row the app is watching.
--
-- The command list rests on `none` whatever is picked (#84), so the attribute
-- it is bound to never changes and the app's spinner would run out into an
-- error. A forced re-emit of the resting value ends it.
-- #93: which resting value that is depends on the power state - a command that
-- arrives mid-transition is answered with `busyX`, not with `none`.
--
-- #93 follow-up: this is the ONLY place a forced re-emit of an unchanged
-- `lastAction` comes from now, and it is the one place that has a reason - the
-- app is holding a spinner open waiting for it. A repeat that `ensure_action`
-- still owed is settled by it, because this event is that repeat.
function poll.answer_action(device)
  local resting = poll.resting_action(device)
  poll.owe_action_repeat(device, nil)
  return poll.emit_action(device, resting, true)
end

--- Keep `lastAction` on the value the row rests on (#82, #84, #93).
--
-- Originally: paint `none` once on a device that has never been told one, so
-- the detail-view list reads "-" nowhere. Since #93 the resting value moves -
-- `none` while the PC is idle, `busyX` while it is in a transition - so this
-- runs on every poll, every push and every wake instead of only once.
--
-- Two rules, both of them measured on the hub 2026-09-26 (#93 follow-up):
--
-- 1. **A value that did not change is not re-sent.** It used to be re-sent
--    forced, on the theory that the row had to keep saying "in progress". It
--    does not - the hub keeps the attribute - and the repeats did harm: a
--    refused command plus the pushes a grace period produces put four forced
--    `busyOff` events out inside five seconds, and the `none` that followed
--    them never reached the cloud. The device record sat on `busyOff` for 90
--    seconds, and because this function then saw "already rests on `none`" it
--    never tried again.
-- 2. **A value that DID change goes out forced, and once more on the next
--    poll.** Forcing a changed value costs nothing (`state_change` only adds
--    "deliver this even if it looks unchanged") and it is the one event that
--    must not be dropped - especially the `none` that ends a transition, which
--    is what got lost. `ACTION_CONFIRM_FIELD` remembers that one repeat is
--    owed, so the next poll sends it again and then stops.
--
-- The repeat is a field rather than a timer on purpose: a timer needs a
-- `driver` this function is not given (it is called from push.lua and wol.lua
-- as well), and the next poll is never far away.
-- Returns true when something was emitted.
function poll.ensure_action(device)
  local seen
  pcall(function() seen = device:get_field(poll.ACTION_FIELD) end)
  local resting = poll.resting_action(device)

  if seen ~= resting then
    -- The row has somewhere new to be. Send it forced, and promise one repeat.
    poll.emit_action(device, resting, true)
    poll.owe_action_repeat(device, resting)
    return true
  end

  -- Unchanged. Nothing to say - except the one repeat promised above, which is
  -- what makes a dropped "the transition is over" recoverable.
  local owed
  pcall(function() owed = device:get_field(poll.ACTION_CONFIRM_FIELD) end)
  if owed ~= nil and owed == resting then
    poll.owe_action_repeat(device, nil)
    poll.emit_action(device, resting, true)
    return true
  end
  return false
end

--- Remember that one forced re-emit of `value` is still owed (nil clears it).
--
-- Not persisted: it is worth one extra event on the next poll, not a hub write,
-- and a driver that restarted mid-transition repaints everything anyway.
function poll.owe_action_repeat(device, value)
  pcall(function() device:set_field(poll.ACTION_CONFIRM_FIELD, value) end)
  return value
end

--------------------------------------------------------------------------------
-- #113: pcPreset.lastPreset
--------------------------------------------------------------------------------

-- What the preset list shows right now ("none", or the slot that just ran),
-- when it started showing a slot (epoch seconds), and whether one forced
-- repeat of the return to "none" is owed - the same two rules as `lastAction`
-- (`ensure_action`, platform notes "강제 이벤트 연발").
poll.PRESET_FIELD = "last_preset"
poll.PRESET_AT_FIELD = "last_preset_at"
poll.PRESET_CONFIRM_FIELD = "last_preset_confirm"
-- How long the row says "프리셋 3 실행함" before it goes back on "none".
-- `hold_preset`'s timer ends the flash after exactly this long; a poll or push
-- that comes first leaves the row alone until it is this old. That check is
-- why it is not shorter: it has to outlast the poll that answers the command
-- (at once, or when a shared answer window closes 1.5 s later, plus however
-- long the PC takes to reply), and `poll.clock` counts whole seconds, so a
-- 3 s hold could already have run out 2 s after the run.
poll.PRESET_HOLD_SECONDS = 5
-- The one owed repeat of the return to "none" follows this much later, on a
-- timer of its own, instead of waiting for the next poll (up to 30 s).
poll.PRESET_REPEAT_SECONDS = 2
-- The device's pending reset or repeat timer (`hold_preset`). Not persisted.
poll.PRESET_TIMER_FIELD = "last_preset_timer"

--- The value the preset list shows now.
function poll.shown_preset(device)
  local shown
  pcall(function() shown = device:get_field(poll.PRESET_FIELD) end)
  if shown == features.PRESET_NONE or features.is_preset_slot(shown) then
    return shown
  end
  return features.PRESET_NONE
end

--- Emit `pcPreset.lastPreset` and remember it. Not persisted: a driver that
--- restarts repaints the row on "none", which is where it belongs.
function poll.emit_preset(device, value, force, deps)
  if not features.is_preset_slot(value) then
    value = features.PRESET_NONE
  end
  pcall(function()
    device:set_field(poll.PRESET_FIELD, value)
    device:set_field(poll.PRESET_AT_FIELD, value ~= features.PRESET_NONE and poll.clock(deps) or nil)
  end)
  poll.emit(device, {
    { cap = caps.PRESET, attr = "lastPreset", value = value, force = force == true },
  })
  return value
end

--- Answer a `run` on the row the app is watching: the value it shows, forced.
function poll.answer_preset(device)
  pcall(function() device:set_field(poll.PRESET_CONFIRM_FIELD, nil) end)
  local shown = poll.shown_preset(device)
  poll.emit(device, {
    { cap = caps.PRESET, attr = "lastPreset", value = shown, force = true },
  })
  return shown
end

--- Put the preset list back on "none" once the slot it shows has been shown
--- for `PRESET_HOLD_SECONDS` (#113). Called from `hold_preset`'s timers and,
--- as the fallback, from every poll and push, like `ensure_action`: the change
--- goes out forced and once more on the next call, and nothing is sent while
--- the value stays where it is.
-- @param held true from the hold timer: the hold is over, whatever the clock's
--   whole seconds say about a timer that fired a moment early.
-- Returns true when something was emitted.
function poll.ensure_preset(device, deps, held)
  local shown = poll.shown_preset(device)
  if shown ~= features.PRESET_NONE then
    local at
    pcall(function() at = device:get_field(poll.PRESET_AT_FIELD) end)
    at = tonumber(at)
    if not held and at and poll.clock(deps) - at < poll.PRESET_HOLD_SECONDS
        and poll.clock(deps) >= at then
      return false
    end
    poll.emit_preset(device, features.PRESET_NONE, true, deps)
    pcall(function() device:set_field(poll.PRESET_CONFIRM_FIELD, true) end)
    return true
  end
  local owed
  pcall(function() owed = device:get_field(poll.PRESET_CONFIRM_FIELD) end)
  if owed then
    pcall(function() device:set_field(poll.PRESET_CONFIRM_FIELD, nil) end)
    poll.emit_preset(device, features.PRESET_NONE, true, deps)
    return true
  end
  return false
end

-- Make `fn` after `delay` the device's one pending preset timer, cancelling the
-- one it replaces; `fn` runs only while it is still the current one.
local function preset_timer(driver, device, delay, name, fn)
  local previous
  pcall(function() previous = device:get_field(poll.PRESET_TIMER_FIELD) end)
  if type(previous) == "table" and previous.timer then
    pcall(function() driver:cancel_timer(previous.timer) end)
  end
  pcall(function() device:set_field(poll.PRESET_TIMER_FIELD, nil) end)
  local token = {}
  local ok, timer = pcall(function()
    return driver:call_with_delay(delay, function()
      local current
      pcall(function() current = device:get_field(poll.PRESET_TIMER_FIELD) end)
      if current ~= token then
        return
      end
      pcall(function() device:set_field(poll.PRESET_TIMER_FIELD, nil) end)
      fn()
    end, name)
  end)
  if not ok then
    return false
  end
  token.timer = timer
  pcall(function() device:set_field(poll.PRESET_TIMER_FIELD, token) end)
  return true
end

--- After a preset ran: put the row back on "none" `PRESET_HOLD_SECONDS` from
--- now and send the owed repeat `PRESET_REPEAT_SECONDS` after that, instead of
--- on the first poll after the hold - up to 35 s with the default 30 s
--- interval, all of it a list that will not run that preset again (init.lua,
--- `handle_preset_run`). Another run meanwhile replaces the pending timer, so
--- they never stack. Still two forced events per run; without a timer the
--- polls and pushes do the same, later.
-- Returns false when no timer could be set.
function poll.hold_preset(driver, device)
  if not driver then
    return false
  end
  return preset_timer(driver, device, poll.PRESET_HOLD_SECONDS, "preset-reset", function()
    poll.ensure_preset(device, nil, true)
    -- A poll that got there first leaves only the repeat, just sent above.
    local owed
    pcall(function() owed = device:get_field(poll.PRESET_CONFIRM_FIELD) end)
    if owed then
      preset_timer(driver, device, poll.PRESET_REPEAT_SECONDS, "preset-repeat", function()
        poll.ensure_preset(device)
      end)
    end
  end)
end

--------------------------------------------------------------------------------
-- #108: pcToast.lastMessage
--------------------------------------------------------------------------------

-- The last text `send` delivered to the PC. Persisted: the row shows it, and
-- a driver restart must not put "없음" back over a message the cloud already
-- shows (every row's first emit in a run is forced, `FIRST_FIELD`).
poll.TOAST_FIELD = "last_toast"

--- What `pcToast.lastMessage` shows now: the last text that went out, else
--- the translated "없음" - never "" (the cloud would record null and the row
--- would read "-", platform notes "상세 화면(detailView) 위젯").
function poll.shown_toast(device)
  local sent
  pcall(function() sent = device:get_field(poll.TOAST_FIELD) end)
  if type(sent) == "string" and sent ~= "" then
    return sent
  end
  return i18n.t(poll.lang(device), "toast_none")
end

--- The `lastMessage` record for `value` (default: what the row shows now).
function poll.toast_row(device, value, force)
  return { cap = caps.TOAST, attr = "lastMessage", value = value or poll.shown_toast(device),
    force = force == true }
end

--- A text `send` delivered: remember it and put it on the row, forced - the
--- app's spinner waits for exactly this event, and sending the same text twice
--- changes nothing the hub would otherwise pass on.
function poll.emit_toast(device, text)
  if type(text) == "string" and text ~= "" then
    pcall(function() device:set_field(poll.TOAST_FIELD, text, { persist = true }) end)
  end
  local value = poll.shown_toast(device)
  poll.emit(device, { poll.toast_row(device, value, true) })
  return value
end

--- Answer a `send` that did not go out (empty text, refused, failed): the row
--- keeps what it shows, re-emitted forced so the spinner ends. The reason is
--- `pcInfo.message`'s.
function poll.answer_toast(device)
  local value = poll.shown_toast(device)
  poll.emit(device, { poll.toast_row(device, value, true) })
  return value
end

--- Keep the row painted: unforced, so after the first emit of a driver run
--- (forced by `FIRST_FIELD`, which is what gives a migrated device its value)
--- the hub drops the unchanged repeats. Called with every poll and push.
function poll.ensure_toast(device)
  poll.emit(device, { poll.toast_row(device) })
end

--- Emit `pcDefer.planCommand` and remember it (#84, moved in #85).
--
-- The command a `pcDefer.schedule` without an explicit command runs. It is
-- the user's own choice, made on the detail view, so it is persisted rather
-- than derived from a status body. An unschedulable value is coerced (§3.3).
--
-- #85: emitted under the schedule capability, because the app puts a detail row
-- in the card of the capability that owns it and this row belongs next to the
-- schedule it configures.
-- #86: `force` for the same reason as `emit_action` - picking the value the row
-- already shows (restart -> restart) changes nothing, and the app waits for a
-- change it will never see.
function poll.emit_plan_command(device, command, force)
  local value = state.plan_command_for(command)
  pcall(function() device:set_field(poll.PLAN_FIELD, value, { persist = true }) end)
  poll.emit(device, {
    { cap = caps.SCHEDULE, attr = "planCommand", value = value, force = force == true },
  })
  return value
end

--- #88: answer a `schedule` on the row the 예약 시간 list is bound to.
--
-- The list rests on `minutesPick`, whose only value is "-1" (the no-op
-- `minutes`), so the attribute never changes and the platform would drop the
-- event - leaving the app spinning until it fails, exactly as the command row
-- did before #86. Forced, the re-emit ends the spinner, and it goes out for
-- every `schedule` the app sends: a picked delay, the Cancel entry and the
-- dismissed picker alike.
function poll.answer_minutes_pick(device)
  poll.emit(device, {
    { cap = caps.SCHEDULE, attr = "minutesPick", value = state.MINUTES_PICK, force = true },
  })
  return state.MINUTES_PICK
end

--- The command this device schedules when none is given: the picked one, else
--- the `offAction` preference when that is schedulable, else `shutdown`.
function poll.plan_command(device)
  local picked
  pcall(function() picked = device:get_field(poll.PLAN_FIELD) end)
  return state.plan_command_for(picked, ((device or {}).preferences or {}).offAction)
end

--- Paint `planCommand` on a device that has never picked one (#84): an
--- attribute that was never emitted reads as "-" on the phone, and the row is
--- a list, which does not open at all without a value (platform notes "상세 화면(detailView) 위젯").
function poll.ensure_plan_command(device)
  local seen
  pcall(function() seen = device:get_field(poll.PLAN_FIELD) end)
  if state.is_plan_command(seen) then
    return false
  end
  poll.emit_plan_command(device, poll.plan_command(device))
  return true
end

--- #85: paint every pcRemote and pcDefer attribute once, so no row of either
--- card reads "-" and the app stops saying the device has not reported all of
--- its state.
--
-- Called from `added` and from `init`: a device that was migrated onto the new
-- capability ids (platform notes "허브의 정의 캐시") has never emitted any of them, even though the
-- `last_action` / `plan_command` fields from the old ones survived, so the
-- version stamp forces one repaint per generation instead of trusting them.
function poll.ensure_rows(device)
  local painted
  pcall(function() painted = device:get_field(poll.ROWS_FIELD) end)
  if painted == poll.ROWS_VERSION then
    return false
  end
  pcall(function() device:set_field(poll.ROWS_FIELD, poll.ROWS_VERSION, { persist = true }) end)

  poll.repaint(device)
  return true
end

--- Repaint now and look again a little later (a profile change: migration,
--- icon or battery switch).
--
-- #129 (W2): every row is forced ONCE. Before, this was three whole forced
-- batches in the same second - `ensure_rows`' repaint, this repaint and a
-- `force = true` poll, about 80 events - and the same repaint plus forced poll
-- again at 15 s and at 90 s (about 70 each). Now:
--   - now: the repaint forces every row it carries (`repaint`, which also
--     starts a new "first emit is forced" generation), then an ordinary poll
--     sends what the status says on top of that: rows the repaint did not
--     carry go out forced as the first of the generation, rows it painted with
--     the same value are not sent again. `opts.painted` skips the repaint when
--     the caller has just done it (`ensure_rows` in `init`).
--   - at 15 s and 90 s: an ordinary poll (what changed) and `resync` - the
--     rows whose loss the user notices (`RESYNC_ROWS`), forced. The follow-ups
--     exist because the first batch can race the cloud applying the new
--     profile; the profile's landing fires `infoChanged`, which repaints
--     once more anyway.
--   - a repaint_soon cancels the follow-ups of the one before it, so two
--     profile changes in a row (a migration, then its landing) do not stack
--     their timers.
--   - and one that comes less than `REPAINT_SPACING_SECONDS` after the last
--     whole repaint waits until that many seconds have passed: the
--     migration's repaint in `init` and the repaint of its landing
--     (`infoChanged`) are both needed - the second is the one the new profile
--     keeps - but not in the same budget window. A second deferred request
--     replaces the first.
poll.LATE_REPAINT_SECONDS = { 15, 90 }
poll.LATE_TIMERS_FIELD = "repaint_late_timers"
poll.REPAINT_SPACING_SECONDS = poll.BUDGET_SECONDS
poll.REPAINT_AT_FIELD = "repaint_at"
poll.REPAINT_DEFERRED_FIELD = "repaint_deferred_timer"
function poll.repaint_soon(driver, device, opts)
  opts = opts or {}
  local now = poll.clock()
  local last
  pcall(function() last = tonumber(device:get_field(poll.REPAINT_AT_FIELD)) end)
  local pending
  pcall(function() pending = device:get_field(poll.REPAINT_DEFERRED_FIELD) end)
  if pending then
    pcall(function() driver:cancel_timer(pending) end)
    pcall(function() device:set_field(poll.REPAINT_DEFERRED_FIELD, nil) end)
  end
  if not opts.painted and driver and last and now >= last
      and now - last < poll.REPAINT_SPACING_SECONDS then
    local ok, timer = pcall(function()
      return driver:call_with_delay(poll.REPAINT_SPACING_SECONDS - (now - last), function()
        pcall(function() device:set_field(poll.REPAINT_DEFERRED_FIELD, nil) end)
        poll.repaint_soon(driver, device)
      end, "repaint-deferred")
    end)
    if ok then
      pcall(function() device:set_field(poll.REPAINT_DEFERRED_FIELD, timer) end)
      return
    end
  end
  pcall(function() device:set_field(poll.REPAINT_AT_FIELD, now) end)
  if not opts.painted then
    poll.repaint(device)
  end
  pcall(poll.once, driver, device)
  local previous
  pcall(function() previous = device:get_field(poll.LATE_TIMERS_FIELD) end)
  for _, timer in ipairs(type(previous) == "table" and previous or {}) do
    pcall(function() driver:cancel_timer(timer) end)
  end
  local timers = {}
  for _, delay in ipairs(poll.LATE_REPAINT_SECONDS) do
    local ok, timer = pcall(function()
      return driver:call_with_delay(delay, function()
        pcall(poll.once, driver, device)
        poll.resync(device)
      end, "repaint-late-" .. delay)
    end)
    if ok and timer then
      timers[#timers + 1] = timer
    end
  end
  pcall(function() device:set_field(poll.LATE_TIMERS_FIELD, timers) end)
end

--------------------------------------------------------------------------------
-- The event budget, part 2: answering commands, and recovering lost events
--------------------------------------------------------------------------------

-- Commands that land close together share their answer polls. The first one
-- polls at once (a single command is answered as fast as before) and opens a
-- window of `ANSWER_WINDOW_SECONDS`; every command that arrives inside it adds
-- its rows to the window and is answered by ONE poll when the window closes,
-- which opens the next window. So a burst of commands costs at most one poll
-- per window, and every command's rows still get their forced emit within one
-- window (the spinner, platform notes "상세 화면(detailView) 위젯").
poll.ANSWER_FIELD = "answer_window"
poll.ANSWER_WINDOW_SECONDS = 1.5

-- A burst of commands (`BURST_COMMANDS` inside `BURST_SECONDS`) is when the
-- budget is most likely to have run out, so `BURST_RESYNC_SECONDS` after one
-- the resync below runs once more, ahead of its schedule.
poll.COMMANDS_FIELD = "recent_commands"
poll.BURST_TIMER_FIELD = "burst_resync_timer"
poll.BURST_COMMANDS = 3
poll.BURST_SECONDS = 10
poll.BURST_RESYNC_SECONDS = 60

-- A dropped event leaves the hub's state cache holding the value the cloud
-- never stored, and from then on the hub drops every unforced repeat of it as
-- unchanged - the cloud stays wrong until the value changes again. So every
-- `RESYNC_SECONDS` the rows whose loss the user notices are sent once more,
-- forced, with the value this run last emitted on them. Six events at most.
poll.RESYNC_FIELD = "resync_at"
poll.RESYNC_SECONDS = 600
poll.RESYNC_ROWS = {
  state.CAP_SWITCH .. ".switch",
  caps.POWER_STATE .. ".powerState",
  features.CAP_MUTE .. ".mute",
  features.CAP_VOLUME .. ".volume",
  features.CAP_PLAYBACK .. ".playbackStatus",
  features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch",
}

--- Re-send the `RESYNC_ROWS` this run has emitted, forced. Returns how many.
function poll.resync(device, deps)
  pcall(function() device:set_field(poll.RESYNC_FIELD, poll.clock(deps)) end)
  local sent = sent_cache(device)
  local events = {}
  for _, key in ipairs(poll.RESYNC_ROWS) do
    local entry = sent[key]
    if entry then
      events[#events + 1] = { cap = entry.cap, attr = entry.attr, value = entry.value,
        component = entry.component, force = true }
    end
  end
  poll.emit(device, events)
  return #events
end

--- `resync` when the last one is `RESYNC_SECONDS` old. Called at the end of
--- every poll, so it needs no timer of its own; the first call of a run only
--- starts the clock (the run's first emits are forced anyway, `FIRST_FIELD`).
-- Returns how many events went out.
function poll.resync_due(device, deps)
  local now = poll.clock(deps)
  local at
  pcall(function() at = tonumber(device:get_field(poll.RESYNC_FIELD)) end)
  if not at or now < at then
    pcall(function() device:set_field(poll.RESYNC_FIELD, now) end)
    return 0
  end
  if now - at < poll.RESYNC_SECONDS then
    return 0
  end
  return poll.resync(device, deps)
end

--- Count a command towards the burst that brings the resync forward.
function poll.note_command(driver, device)
  local now = poll.clock()
  local times
  pcall(function() times = device:get_field(poll.COMMANDS_FIELD) end)
  local kept = {}
  for _, t in ipairs(type(times) == "table" and times or {}) do
    if now >= t and now - t < poll.BURST_SECONDS then
      kept[#kept + 1] = t
    end
  end
  kept[#kept + 1] = now
  pcall(function() device:set_field(poll.COMMANDS_FIELD, kept) end)
  if #kept < poll.BURST_COMMANDS or not driver then
    return false
  end
  local pending
  pcall(function() pending = device:get_field(poll.BURST_TIMER_FIELD) end)
  if pending then
    return false
  end
  local ok, timer = pcall(function()
    return driver:call_with_delay(poll.BURST_RESYNC_SECONDS, function()
      pcall(function() device:set_field(poll.BURST_TIMER_FIELD, nil) end)
      poll.resync(device)
    end, "resync-burst")
  end)
  if ok then
    pcall(function() device:set_field(poll.BURST_TIMER_FIELD, timer or true) end)
  end
  return ok
end

local function merge_rows(into, rows)
  if type(rows) == "table" then
    for key, wanted in pairs(rows) do
      if wanted then
        into[key] = true
      end
    end
  end
  return into
end

-- Open an answer window on `device`; when it closes, the commands that arrived
-- inside it are answered by one poll, which opens the next window. Returns
-- false when no timer could be set (no coalescing then: every command polls).
local function open_window(driver, device)
  if not driver then
    return false
  end
  local window = { rows = {}, owed = false }
  local ok = pcall(function()
    driver:call_with_delay(poll.ANSWER_WINDOW_SECONDS, function()
      local current
      pcall(function() current = device:get_field(poll.ANSWER_FIELD) end)
      if current ~= window then
        return
      end
      pcall(function() device:set_field(poll.ANSWER_FIELD, nil) end)
      if window.owed then
        open_window(driver, device)
        pcall(poll.once, driver, device, { force = window.rows, note = window.note })
      end
    end, "answer-poll")
  end)
  if ok then
    pcall(function() device:set_field(poll.ANSWER_FIELD, window) end)
  end
  return ok
end

--- The poll that answers a command that went out (event budget, part 2).
-- `rows` are the row keys (`row_key`) the app is watching; they go out forced.
-- #129 (W2): `note` is the one-off confirmation `poll.once` puts on
-- `pcInfo.message` ("예약을 취소했습니다"); in a shared window the last
-- command's note is the one shown. Every command takes this path now - the
-- power commands, `switch off`, `schedule` and `cancel` as well as the v1.2.0
-- ones - so a routine that fires several at once shares one answer poll.
-- Returns what the poll returned, or true when the command joined a window
-- whose poll is still to come.
function poll.answer(driver, device, rows, note)
  poll.note_command(driver, device)
  local window
  pcall(function() window = device:get_field(poll.ANSWER_FIELD) end)
  if type(window) == "table" then
    merge_rows(window.rows, rows)
    if note ~= nil then
      window.note = note
    end
    window.owed = true
    return true
  end
  open_window(driver, device)
  return poll.once(driver, device, { force = rows, note = note })
end

--- #116: follow `status.battery.present` onto the plain or the `-battery`
--- profile (`profiles.apply_battery`, which waits for two statuses that
--- agree). Called after every status a poll or a push applied. A switch is
--- repainted like an icon switch, one second later so the poll or push that
--- got us here finishes first instead of nesting a second request in it.
-- Returns the new profile name, or nil.
function poll.follow_battery(driver, device, status)
  local moved = profiles.apply_battery(device, features.battery_present(status))
  if moved and driver then
    pcall(function()
      driver:call_with_delay(1, function()
        poll.repaint_soon(driver, device)
      end, "battery-profile")
    end)
  end
  return moved
end

--- Repaint every row with forced events: after a profile change (`infoChanged`)
--- the cloud starts the new profile with empty states, and the hub would
--- otherwise drop the re-emit of values it considers unchanged.
function poll.repaint(device)
  -- The event budget: a new profile starts with an empty cloud record, so
  -- nothing this run emitted for the old one counts as "already sent". The
  -- rows below go out forced anyway; this is for the ones only a later poll
  -- carries.
  poll.forget_sent(device)
  -- #93: the resting value, not the remembered one - a repaint in the middle of
  -- a transition has to leave the row saying "in progress". A repeat
  -- `ensure_action` still owed is settled here too: this forced event is it.
  poll.owe_action_repeat(device, nil)
  poll.emit_action(device, poll.resting_action(device), true)
  poll.emit_plan_command(device, poll.plan_command(device), true)
  -- #113: the preset list's resting value, like `lastAction` above.
  poll.answer_preset(device)
  -- #108: the message row, on the last text sent (or "없음").
  poll.answer_toast(device)
  -- #92: a repaint of a device that has answered before keeps its version on
  -- the row; only one that never answered falls back to "v?".
  -- #107: and the v1.2.0 rows from the last status, when there was one.
  poll.emit(device, poll.force_all(
    state.initial_rows(poll.lang(device), poll.last_service_version(device),
      (poll.extras(device) or {}).last_status)))
end

--- err_kind (client.lua) -> `pcInfo.connection` enum value (§4), or nil
--- when the failure says nothing about the connection and the last state
--- should stand.
function poll.connection_for(kind)
  if kind == "unauthorized" or kind == "unreachable" or kind == "incompatible" then
    return kind
  end
  -- 403: the secret was accepted, the hub is not on the allow-list. The enum
  -- has no separate value for it (§3.1), so it shares `unauthorized` and the
  -- message tells the two apart.
  if kind == "forbidden" then
    return "unauthorized"
  end
  -- "badrequest" means the service answered but refused; from the app's point
  -- of view that is a protocol problem, not a connection problem.
  if kind == "badrequest" then
    return "incompatible"
  end
  if kind == "ratelimited" then
    -- §3.1: nothing has changed about the PC, so do not repaint anything.
    return nil
  end
  return "ok"
end

--- Human-readable text for an err_kind (§3.1). `body` is the decoded response
--- when there was one: a higher `protocol` means our driver is the old side,
--- and a missing or lower one means the service is.
function poll.message_for(kind, body, lang)
  body = body or {}
  if kind == "incompatible" then
    local protocol = tonumber(body.protocol)
    if protocol and protocol > client.PROTOCOL then
      return i18n.t(lang, "incompatible_driver")
    end
    return i18n.t(lang, "incompatible_service", poll.MIN_SERVICE_VERSION)
  end
  if kind == "badrequest" then
    -- The service says which command or argument it refused (§3.3); quoting it
    -- is more use than "the service rejected the command" on its own.
    local text = i18n.t(lang, "badrequest")
    if type(body.error) == "string" and body.error ~= "" then
      return text .. " · " .. body.error
    end
    return text
  end
  if kind == "unauthorized" or kind == "forbidden" or kind == "unreachable"
      or kind == "ratelimited" then
    return i18n.t(lang, kind)
  end
  return ""
end

--- One poll cycle: GET /st/v1/status, advance the state machine, emit, and set
--- health online/offline. Also used by `refresh` and after a command.
--- `opts.note` is a one-off confirmation to show in `pcInfo.message` when
--- nothing more important applies (§4, state.MESSAGE_ORDER); `opts.deps` is
--- the injected http/json/ltn12 the tests use instead of a socket.
function poll.once(driver, device, opts)
  opts = opts or {}
  local prefs = device.preferences or {}
  local lang = prefs.language
  local current = poll.get_state(device)

  if not client.device_base_url(device) then
    -- Freshly added device: nothing to poll until the user fills in the IP.
    poll.emit_connection(device, "unreachable", i18n.t(lang, "no_ip"), opts.deps)
    -- Never offline: the driver on the hub is what acts, and SmartThings
    -- greys out an offline device, which would take Wake-on-LAN away exactly
    -- when it is needed. "PC off" is powerState/switch, not health.
    pcall(function() device:online() end)
    return false, "no ip"
  end

  local ok, body, kind = client.get_status(device, opts.deps)

  if ok then
    local nxt = state.transition(current, "status_ok")
    -- #93: `active` plus the countdown and the command, because the PC's own
    -- grace period reaches the driver as nothing but a schedule about to fire
    -- (state.grace_limit). Before `apply_status`, which reads it back out for
    -- `supportedCommands`.
    state.remember_schedule(nxt, body)
    -- #107: and which v1.2.0 features the PC offers, for the command handlers.
    features.remember(nxt, body)
    poll.set_state(device, nxt)
    -- A successful status while waking means the PC is up: drop the 90s timeout.
    local wol = require "wol"
    wol.cancel_wake(driver, device)
    -- §6.4: remember the MAC to wake this PC on - `wol.selected.mac` when the
    -- service chose an adapter (#97), else the WoL-capable adapter we guessed.
    -- A driver cannot write its own preferences, so this is kept as a field and
    -- used when `macAddress` is left empty.
    local mac = state.wol_mac(body)
    if mac then
      -- Persisted: the MAC must survive a hub or driver restart while the PC
      -- is off, or "wake" has nothing to send to.
      device:set_field(poll.MAC_FIELD, mac, { persist = true })
    end
    -- §6.4: remembered so `switch on` can say "WoL is off on the adapter"
    -- right away instead of at the next poll. #97: about the chosen adapter,
    -- and with its name, so the warning points at the NIC to go and open.
    device:set_field(poll.WOL_READY_FIELD, not state.wol_off(body))
    local adapter = state.wol_adapter(body)
    if adapter then
      -- Persisted for the same reason as the MAC: the wake happens while the
      -- PC is off, which is exactly when no status body is available. Only
      -- written when there is a name, so a service too old to send one leaves
      -- the last known name rather than a blank.
      device:set_field(poll.WOL_ADAPTER_FIELD, adapter, { persist = true })
    end
    -- §6.5: the identity. A manually added device learns its machine_id here,
    -- so SSDP can later recognise it instead of creating a duplicate.
    poll.remember_identity(device, body)
    -- #92: and the version, so the row survives the PC being switched off.
    -- Before `ensure_rows`, which repaints that very row.
    poll.remember_service_version(device, body)
    -- #102: and when, so the failure path can say "마지막 확인 12분 전".
    poll.remember_last_seen(device, opts.deps)
    -- #82/#84/#85: no status body carries `lastAction` or `planCommand`, and a
    -- migrated device has emitted nothing at all under the new capability ids,
    -- so the resting values go out first and the status body overwrites the
    -- rows it does know about.
    poll.ensure_rows(device)
    -- #86: `opts.force` names the rows this poll is answering a command on
    -- (the schedule rows after `schedule` / `cancel`). They go out with
    -- `state_change = true` so the app's spinner ends even when the value is
    -- the one it already had; everything else stays an ordinary update.
    local events = state.apply_status(nxt, body, {
      now = poll.now(),
      lang = lang,
      note = opts.note,
    })
    -- opts.force == true: a repaint after a profile change. The hub drops
    -- re-emits of values it considers unchanged, so every row goes out with
    -- state_change = true or the cloud record of the new profile stays empty.
    if opts.force == true then
      poll.force_all(events)
    else
      poll.force_rows(events, opts.force)
    end
    poll.emit(device, events)
    -- #123: the watch list's child devices - created, deleted and painted
    -- from this status. Never on a failed poll: an unreachable PC leaves every
    -- child on its last value.
    local synced, sync_err = pcall(function() require("apps").sync(driver, device, body, opts.deps) end)
    if not synced then
      logger().warn("app children not updated: " .. tostring(sync_err))
    end
    poll.ensure_action(device)
    poll.ensure_plan_command(device)
    -- #113: a preset that just ran shows for a moment, then the list rests.
    poll.ensure_preset(device, opts.deps)
    -- #108: no status body carries `lastMessage` either.
    poll.ensure_toast(device)
    -- The event budget: the rows a dropped event could have left wrong.
    poll.resync_due(device, opts.deps)
    -- #116: a laptop moves to the profile with the battery card, and back.
    poll.follow_battery(driver, device, body)
    pcall(function() device:online() end)
    -- §6.3: with the PC answering, ask it to push instead of waiting for the
    -- next poll. A failure here only means the driver keeps polling.
    pcall(function() require("push").ensure(driver, device, opts.deps) end)
    return true
  end

  local connection = poll.connection_for(kind)
  if not connection then
    -- §3.1: rate limited. The PC is fine, we simply asked too often (the poll
    -- right after a command can land inside the same second), so leave every
    -- attribute and the health status as they were.
    logger().warn(string.format("poll skipped: %s", i18n.t("en", kind)))
    return false, kind
  end

  local nxt = current
  if kind == "unreachable" then
    nxt = state.transition(current, "unreachable")
    pcall(function() device:online() end) -- see above: health stays online so the switch can wake the PC
    -- §6.5: the PC may just have moved to another address. One targeted SSDP
    -- search (rate limited to once per 5 minutes per device) before the next
    -- poll is cheaper than waiting for the user to notice.
    pcall(function() require("discovery").refresh(driver, device, opts.deps) end)
  end
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  -- #93: a PC that is waking, or one that has already gone, answers nothing -
  -- so this is the path the command list's resting value has to be kept on
  -- while a transition is running, and the one that hands it back to `none`
  -- when `waking` gives up and becomes `off`.
  poll.ensure_action(device)
  poll.ensure_preset(device, opts.deps)
  poll.ensure_toast(device)
  poll.emit_connection(device, connection, poll.message_for(kind, body, lang), opts.deps)
  poll.resync_due(device, opts.deps)
  return false, kind
end

--- Store what a status body says about the PC's identity (§6.5). Returns true
--- when something changed.
function poll.remember_identity(device, body)
  body = body or {}
  local discovery = require "discovery"
  local changed = false
  if type(body.machine_id) == "string" and body.machine_id ~= ""
      and device:get_field(discovery.MACHINE_FIELD) ~= body.machine_id then
    device:set_field(discovery.MACHINE_FIELD, body.machine_id, { persist = true })
    changed = true
  end
  if type(body.hostname) == "string" and body.hostname ~= ""
      and device:get_field(discovery.HOSTNAME_FIELD) ~= body.hostname then
    device:set_field(discovery.HOSTNAME_FIELD, body.hostname, { persist = true })
    changed = true
  end
  -- #94: with the identity confirmed, put its first eight characters in the
  -- device's `model` - once per device, guarded by a persisted field, so a
  -- device added before #94 stops reading "PC Control" in the app's device
  -- information screen.
  discovery.ensure_model(device)
  return changed
end

--- Poll interval in seconds from the `pollInterval` preference (§7).
function poll.interval(prefs)
  local seconds = tonumber((prefs or {}).pollInterval)
  if seconds and seconds >= 5 then
    return math.floor(seconds)
  end
  return poll.DEFAULT_INTERVAL
end

--- §6.7: spread the devices' polls over the interval so N PCs are not all
--- asked in the same second. A hash of the DNI is deterministic (the same
--- device keeps its slot across restarts) and needs no coordination between
--- devices, unlike an index that would have to be recomputed on every add and
--- remove. FNV-1a, 32 bit.
function poll.offset(dni, interval)
  interval = math.floor(tonumber(interval) or poll.DEFAULT_INTERVAL)
  if interval <= 1 or type(dni) ~= "string" or dni == "" then
    return 0
  end
  -- Hex literals so the constants are integers on every Lua 5.3 build
  -- (offset basis 2166136261, prime 16777619).
  local hash = 0x811C9DC5
  for i = 1, #dni do
    hash = (hash ~ dni:byte(i)) & 0xFFFFFFFF
    hash = (hash * 0x01000193) & 0xFFFFFFFF
  end
  -- The hub's integers are 64 bit, so `hash` is the unsigned 32-bit value
  -- there; fengari's (the local test runner) are 32 bit, where the same bits
  -- read as a negative number and `%` gives another slot. Dropping bit 31
  -- leaves a value both read alike.
  return (hash & 0x7FFFFFFF) % interval
end

function poll.stop(driver, device)
  for _, field in ipairs({ poll.TIMER_FIELD, poll.START_TIMER_FIELD }) do
    local timer = device:get_field(field)
    if timer then
      pcall(function() driver:cancel_timer(timer) end)
      device:set_field(field, nil)
    end
  end
end

--- (Re)start the poll timer. Safe to call on init and on every infoChanged.
function poll.start(driver, device)
  poll.stop(driver, device)
  local interval = poll.interval(device.preferences)
  local offset = poll.offset(device.device_network_id, interval)

  local function begin()
    device:set_field(poll.START_TIMER_FIELD, nil)
    local timer = driver:call_on_schedule(interval, function()
      poll.once(driver, device)
    end, "pc-poll")
    device:set_field(poll.TIMER_FIELD, timer)
    return timer
  end

  if offset > 0 then
    -- The schedule itself starts late; the first poll below still happens now,
    -- so the tiles do not wait for the offset.
    local starter = driver:call_with_delay(offset, begin, "pc-poll-start")
    device:set_field(poll.START_TIMER_FIELD, starter)
  else
    begin()
  end

  -- Prime the tiles instead of waiting a whole interval for the first tick.
  driver:call_with_delay(1, function()
    poll.once(driver, device)
  end, "pc-poll-initial")
  return device:get_field(poll.TIMER_FIELD)
end

return poll
