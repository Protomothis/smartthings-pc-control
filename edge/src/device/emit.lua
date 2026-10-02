-- The emit funnel (#129): every event the driver sends goes through `emit.rows`,
-- and every rule about when an event is forced (`state_change = true`) or not
-- sent at all lives here.
--
-- A record is `{ cap = <capability id>, attr = <attribute>, value = …,
-- component? = <component id> }` (model/*.lua and features.lua make them).
-- Its row key is `<cap>.<attr>` on main, `<component>/<cap>.<attr>` elsewhere.
--
-- The rules, each measured on the hub (platform notes "상세 화면(detailView)
-- 위젯" and "이벤트 예산(rate limit)"):
--
--   reason     forced  why
--   answer     yes     the app spins on the row until an event arrives, and
--                      an unchanged value is otherwise dropped
--   paint      yes     a new profile's cloud record is empty (sent in batches)
--   rotate     yes     re-sends what the cloud may have lost (every row once
--   resync             per `ROTATE_SECONDS`; the rows the user notices after a
--                      burst of commands)
--   poll       first   an ordinary update: the first emit of each row in a run
--                      is forced (after a profile move the hub may hold a value
--                      the cloud never stored); after that an unchanged value
--                      is not sent at all, because the platform counts dropped
--                      events against the device's budget too; on a restart a
--                      row whose value equals the hub's state cache counts as
--                      sent (`seeding`)

local caps = require "caps"
local clock = require "device.clock"
local features = require "features"
local fields = require "device.fields"
local state = require "state"

local emit = {}

local FORCE = { state_change = true }

-- The reasons that force every record.
emit.FORCED = { answer = true, paint = true, rotate = true, resync = true }

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--- The row key of a record.
function emit.row_key(e)
  local key = tostring(e.cap) .. "." .. tostring(e.attr)
  if e.component ~= nil and e.component ~= "main" then
    return tostring(e.component) .. "/" .. key
  end
  return key
end

--- Mark the records whose row key is in `keys` as forced. Returns the list.
function emit.force_rows(records, keys)
  if type(keys) ~= "table" then
    return records
  end
  for _, e in ipairs(records or {}) do
    if keys[emit.row_key(e)] then
      e.force = true
    end
  end
  return records
end

--- A string that is equal exactly when the values are: tables by sorted keys,
--- 30 and 30.0 alike, "1" and 1 not.
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
emit.signature = signature

--------------------------------------------------------------------------------
-- where a record goes
--------------------------------------------------------------------------------

-- The capability object for an id. A custom capability the account owner has
-- not created yet does not resolve; the record is skipped, not raised.
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

--- The component object `device` has for `id`, or nil when its profile has
--- none (a v1 profile before its migration, a desktop's battery row): the
--- record is then skipped rather than sent into a component the hub rejects.
--- `device.profile.components` is keyed by id on the hub; a list of `{ id }`
--- is searched as well.
function emit.component(device, id)
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

-- The capability, the attribute's event constructor and the component object
-- (nil for main) of one record, or nil when it cannot go out on this profile.
local function resolve(device, e, log)
  local cap = capability_for(e.cap)
  local attr = cap and cap[e.attr]
  local component
  if e.component ~= nil and e.component ~= "main" then
    component = emit.component(device, e.component)
  elseif e.component == nil and features.MEDIA_CAPS[e.cap] then
    -- #118: a profile may keep the media group on a component of its own; the
    -- record (and its row key) stays main-shaped.
    component = emit.component(device, features.MEDIA_COMPONENT)
  end
  if not attr then
    log.debug(string.format("capability %s.%s not available, skipped", tostring(e.cap), tostring(e.attr)))
    return nil
  end
  if e.component ~= nil and e.component ~= "main" and not component then
    log.debug(string.format("component %s not in the profile, %s.%s skipped",
      tostring(e.component), tostring(e.cap), tostring(e.attr)))
    return nil
  end
  return cap, attr, component
end

--------------------------------------------------------------------------------
-- what this run has sent
--------------------------------------------------------------------------------

-- `fields.ROWS_SENT`: row key -> the record last sent and its signature, in
-- memory only. `fields.ROWS_FORCED`: the rows whose first emit of the run (or
-- of the profile generation, `forget`) has gone out.

local function sent_cache(device)
  return fields.table(device, fields.ROWS_SENT)
end

local function first_emit(device, key)
  local seen = fields.get(device, fields.ROWS_FORCED)
  if type(seen) ~= "table" then
    seen = {}
  end
  if seen[key] then
    return false
  end
  seen[key] = true
  fields.set(device, fields.ROWS_FORCED, seen)
  return true
end

--- The value this run last sent on row `key`, or nil.
function emit.sent_value(device, key)
  local entry = sent_cache(device)[key]
  if entry then
    return entry.value
  end
  return nil
end

--- Forget what this run has sent (a profile change: the new profile's cloud
--- record is empty), start a new "first emit is forced" generation and stop
--- trusting the hub's state cache.
function emit.forget(device)
  fields.set(device, fields.ROWS_SENT, nil)
  fields.set(device, fields.ROWS_FORCED, nil)
  fields.set(device, fields.ROWS_SEED_OFF, true)
end

-- The rows whose loss the user notices first. They are never taken from the
-- hub's state cache, and a burst of commands or a repaint's follow-ups re-send
-- them (`resync`).
emit.RESYNC_ROWS = {
  state.CAP_SWITCH .. ".switch",
  caps.POWER_STATE .. ".powerState",
  features.CAP_MUTE .. ".mute",
  features.CAP_VOLUME .. ".volume",
  features.CAP_PLAYBACK .. ".playbackStatus",
  features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch",
}

local function loss_matters(key)
  for _, k in ipairs(emit.RESYNC_ROWS) do
    if k == key then
      return true
    end
  end
  return false
end

--- True when this run may take the hub's state cache (`device:get_latest_state`,
--- persisted across restarts) as "already sent" for `device`: on a restart
--- with the profile unchanged. Not after a repaint (`forget`). The cache can
--- hold a value the cloud never stored; the rotation corrects that within one
--- cycle (platform notes "상태 캐시와 infoChanged").
function emit.seeding(device)
  if type(device) ~= "table" then
    return false
  end
  return fields.get(device, fields.ROWS_SEED_OFF) ~= true
end

local function cached_state(device, cap, e, component)
  local value
  pcall(function()
    value = device:get_latest_state(component and component.id or "main",
      cap.ID or e.cap, e.attr)
  end)
  return value
end

--- The value row `e` (a record without a value) last had: what this run sent,
--- else the hub's state cache, else nil. #123: a repaint keeps the watch
--- card's slots on what they showed instead of inventing one (an "off" list
--- moves no slot).
function emit.last_value(device, e)
  local value = emit.sent_value(device, emit.row_key(e))
  if value ~= nil then
    return value
  end
  local component = e.component ~= nil and e.component ~= "main" and { id = e.component } or nil
  return cached_state(device, {}, e, component)
end

--------------------------------------------------------------------------------
-- the budget guard
--------------------------------------------------------------------------------

-- How many emits in how many seconds before the driver says so in logcat. A
-- warning line only: nothing is dropped here.
emit.BUDGET_EVENTS = 20
emit.BUDGET_SECONDS = 10

local function spend(device, log)
  local now = clock.epoch()
  local budget = fields.get(device, fields.EMIT_BUDGET)
  if type(budget) ~= "table" or now < budget.start or now - budget.start >= emit.BUDGET_SECONDS then
    budget = { start = now, count = 0, warned = false }
    fields.set(device, fields.EMIT_BUDGET, budget)
  end
  budget.count = budget.count + 1
  if budget.count > emit.BUDGET_EVENTS and not budget.warned then
    budget.warned = true
    log.warn(string.format("event budget: %s emitted more than %d events in %d s",
      tostring((device or {}).id), emit.BUDGET_EVENTS, emit.BUDGET_SECONDS))
  end
end

--------------------------------------------------------------------------------
-- the spread repaint
--------------------------------------------------------------------------------

-- A profile change needs every row once, forced, and ~49 events in one second
-- lost one of them on the Dev hub (2026-10-01). So `paint` queues the rows and
-- sends `PAINT_BATCH` of them every `PAINT_SECONDS`, the rows the user sees
-- first (`PAINT_FIRST`) in the first batch. Two batches fit in one budget
-- window with room for the poll's own changes.
--
-- While a paint runs, an ordinary emit of a row still waiting - or of a row
-- this generation has not sent at all - only updates the value its batch will
-- carry; a forced one goes out at once and leaves the queue.
emit.PAINT_BATCH = 9
emit.PAINT_SECONDS = 5
-- The first batch waits for the poll that follows the repaint (`paint_start`)
-- at most this long, so the status's rows are in it.
emit.PAINT_START_SECONDS = 1
emit.PAINT_FIRST = {
  state.CAP_SWITCH .. ".switch",
  caps.POWER_STATE .. ".powerState",
  caps.STATUS .. ".summary",
  features.WATCH_COMPONENT .. "/" .. features.CAP_WATCH .. ".summary",
  features.CAP_MUTE .. ".mute",
  features.CAP_VOLUME .. ".volume",
  features.CAP_PLAYBACK .. ".playbackStatus",
  features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch",
}

-- While `emit.gather` runs, the device's records are collected instead of sent.
local gathering

--- Run `fn` and return the records it sent for `device`, unsent.
function emit.gather(device, fn)
  local outer = gathering
  gathering = { device = device, records = {} }
  local ok, err = pcall(fn)
  local records = gathering.records
  gathering = outer
  if not ok then
    logger().warn("repaint: " .. tostring(err))
  end
  return records
end

--- The paint queue of `device` while a spread repaint is under way, else nil.
function emit.painting(device)
  local queue = fields.get(device, fields.PAINT_QUEUE)
  if type(queue) == "table" then
    return queue
  end
  return nil
end

local function enqueue(queue, key, e)
  if not queue.rows[key] then
    queue.order[#queue.order + 1] = key
  end
  queue.rows[key] = { cap = e.cap, attr = e.attr, value = e.value, component = e.component }
end

-- The next `n` waiting rows, `PAINT_FIRST` first, as forced records.
local function take(queue, n)
  local batch = {}
  local function pick(key)
    local record = queue.rows[key]
    if record and #batch < n then
      queue.rows[key] = nil
      record.force = true
      batch[#batch + 1] = record
    end
  end
  for _, key in ipairs(emit.PAINT_FIRST) do
    pick(key)
  end
  for _, key in ipairs(queue.order) do
    pick(key)
  end
  local order, kept = {}, {}
  for _, key in ipairs(queue.order) do
    if queue.rows[key] and not kept[key] then
      kept[key] = true
      order[#order + 1] = key
    end
  end
  queue.order = order
  return batch
end

-- Everything still waiting, at once (no timer could be set).
local function paint_all(device, queue, batch)
  for _, record in ipairs(take(queue, math.huge)) do
    batch[#batch + 1] = record
  end
  fields.set(device, fields.PAINT_QUEUE, nil)
  return batch
end

local paint_next

-- The queue's one pending batch timer; a new one turns the previous into a
-- no-op (`token`). False when no timer could be set.
local function paint_timer(driver, device, queue, delay)
  local token = {}
  queue.token = token
  local ok, timer = pcall(function()
    return driver:call_with_delay(delay, function()
      if emit.painting(device) == queue and queue.token == token then
        paint_next(driver, device)
      end
    end, "repaint-batch")
  end)
  if ok then
    queue.timer = timer
  end
  return ok
end

paint_next = function(driver, device)
  local queue = emit.painting(device)
  if not queue then
    return 0
  end
  queue.started = true
  local batch = take(queue, emit.PAINT_BATCH)
  if next(queue.rows) == nil then
    fields.set(device, fields.PAINT_QUEUE, nil)
  elseif not paint_timer(driver, device, queue, emit.PAINT_SECONDS) then
    paint_all(device, queue, batch)
  end
  emit.rows(device, batch)
  return #batch
end

--- Queue `records` - every row, forced - to be painted in batches. The first
--- batch goes at `paint_start` or `PAINT_START_SECONDS` later. Without a
--- `driver` (no timers) they all go at once. A paint that comes while one is
--- under way (a migration, then its landing) puts every row back in the queue
--- and keeps the running cadence. Returns how many went out now.
function emit.paint(driver, device, records)
  local log = logger()
  local queue = emit.painting(device)
  if not driver then
    if queue then
      fields.set(device, fields.PAINT_QUEUE, nil)
    end
    emit.rows(device, records, { reason = "paint" })
    return #(records or {})
  end
  local fresh = queue == nil
  queue = queue or { rows = {}, order = {}, started = false }
  for _, e in ipairs(records or {}) do
    if resolve(device, e, log) then
      enqueue(queue, emit.row_key(e), e)
    end
  end
  fields.set(device, fields.PAINT_QUEUE, queue)
  if fresh and not paint_timer(driver, device, queue, emit.PAINT_START_SECONDS) then
    local batch = paint_all(device, queue, {})
    emit.rows(device, batch)
    return #batch
  end
  return 0
end

--- Send the first batch of a paint that has not started yet. Returns how many.
function emit.paint_start(driver, device)
  local queue = emit.painting(device)
  if not queue or queue.started or not driver then
    return 0
  end
  pcall(function() driver:cancel_timer(queue.timer) end)
  return paint_next(driver, device)
end

--------------------------------------------------------------------------------
-- the funnel
--------------------------------------------------------------------------------

--- Send `records` on `device` for `opts.reason` (the table at the top).
-- `opts.answer`: row keys forced within a "poll" (a command's answer poll).
-- A record's own `force = true` forces that record whatever the reason.
-- A `component` other than main goes out with `emit_component_event`, and
-- only when the device's profile has it.
function emit.rows(device, records, opts)
  opts = opts or {}
  if gathering and gathering.device == device then
    for _, e in ipairs(records or {}) do
      gathering.records[#gathering.records + 1] = e
    end
    return
  end
  local all_forced = emit.FORCED[opts.reason or "poll"] == true
  local answer = opts.answer
  local log = logger()
  local sent = sent_cache(device)
  local queue = emit.painting(device)
  for _, e in ipairs(records or {}) do
    local cap, attr, component = resolve(device, e, log)
    if cap then
      local key = emit.row_key(e)
      local forced = all_forced or not not e.force or (type(answer) == "table" and not not answer[key])
      local sig = signature(e.value)
      local last = sent[key]
      if not last and not forced and not loss_matters(key) and emit.seeding(device) then
        local cached = cached_state(device, cap, e, component)
        if cached ~= nil and signature(cached) == sig then
          last = { sig = sig, value = e.value, cap = e.cap, attr = e.attr, component = e.component }
          sent[key] = last
        end
      end
      if queue and not forced and not last then
        -- This row's batch has not gone yet: it carries the latest value.
        enqueue(queue, key, e)
      elseif forced or not last or last.sig ~= sig then
        if queue then
          queue.rows[key] = nil
        end
        local ok, err = pcall(function()
          local force = forced or first_emit(device, key)
          local event = force and attr(e.value, FORCE) or attr(e.value)
          if component then
            device:emit_component_event(component, event)
          else
            device:emit_event(event)
          end
        end)
        if ok then
          -- `at`: the rotation skips a row this very poll sent.
          sent[key] = { sig = sig, value = e.value, cap = e.cap, attr = e.attr,
            component = e.component, at = clock.epoch() }
          spend(device, log)
        else
          log.warn(string.format("emit %s failed: %s", key, tostring(err)))
        end
      end
      -- Unchanged and not forced: not sent, not even logged.
    end
  end
end

--------------------------------------------------------------------------------
-- re-sending what may have been lost
--------------------------------------------------------------------------------

--- Re-send the `RESYNC_ROWS` this run has sent. Returns how many.
function emit.resync(device)
  local sent = sent_cache(device)
  local records = {}
  for _, key in ipairs(emit.RESYNC_ROWS) do
    local entry = sent[key]
    if entry then
      records[#records + 1] = { cap = entry.cap, attr = entry.attr, value = entry.value,
        component = entry.component }
    end
  end
  emit.rows(device, records, { reason = "resync" })
  return #records
end

-- The rotation: every row this run has sent goes out forced again at least
-- once per `ROTATE_SECONDS`, in a stable order (sorted row keys) - a lost
-- event then stays lost for one cycle at most, whatever the hub's cache says.
-- A step sends `rotate_size` rows, never more than `ROTATE_MAX`, and passes
-- over a row this very poll has just sent.
emit.ROTATE_SECONDS = 600
emit.ROTATE_MAX = 5

--- How many rows a step re-sends so that `rows` rows go round in
--- `ROTATE_SECONDS` at one step per `interval` seconds: 1 to `ROTATE_MAX`.
function emit.rotate_size(rows, interval)
  rows = tonumber(rows) or 0
  if rows <= 0 then
    return 0
  end
  local steps = math.max(1, math.floor(emit.ROTATE_SECONDS / math.max(1, tonumber(interval) or 1)))
  return math.min(emit.ROTATE_MAX, math.max(1, math.ceil(rows / steps)))
end

--- One rotation step at `interval` seconds per step: re-send the next rows
--- after the cursor. Returns how many went out.
function emit.rotate(device, interval)
  local sent = sent_cache(device)
  local keys = {}
  for key in pairs(sent) do
    keys[#keys + 1] = key
  end
  if #keys == 0 then
    return 0
  end
  table.sort(keys)
  local size = emit.rotate_size(#keys, interval)
  local cursor = fields.get(device, fields.ROTATE_CURSOR)
  local start = 1
  if type(cursor) == "string" then
    for i, key in ipairs(keys) do
      if key > cursor then
        start = i
        break
      end
    end
  end
  local now = clock.epoch()
  local records, last = {}, cursor
  for n = 0, #keys - 1 do
    if #records >= size then
      break
    end
    local key = keys[(start - 1 + n) % #keys + 1]
    last = key
    local entry = sent[key]
    local just_sent = entry.at ~= nil and now >= entry.at and now - entry.at < 1
    if not just_sent then
      records[#records + 1] = { cap = entry.cap, attr = entry.attr, value = entry.value,
        component = entry.component }
    end
  end
  fields.set(device, fields.ROTATE_CURSOR, last)
  emit.rows(device, records, { reason = "rotate" })
  return #records
end

return emit
