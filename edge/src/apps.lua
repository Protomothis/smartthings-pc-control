-- #123: one child device per watched app (design doc §4.2).
--
-- The PC's watch list (media-notify.md §11) comes with every status and every
-- push as `activity = { enabled, apps = [ { id, label, running } ], top }`.
-- Each entry becomes an EDGE_CHILD of the PC on `pc-app.v1`, keyed by the
-- process name (`parent_assigned_child_key` = `apps[i].id`), whose one row -
-- `pcApp.running`, "실행 중 / 꺼짐" - is what a routine reads ("Steam이 실행
-- 중이 되면"). The PC itself keeps one summary row (features.apps_summary).
--
-- The children are never polled. The parent's poll and push hand every status
-- to `sync`, which creates the children the list gained, deletes the ones it
-- lost, and emits each child's `running` through `poll.emit` on the CHILD
-- device - so each child has its own "already sent" cache, its own "first
-- emit of the run is forced" mark and its own budget count (platform notes
-- "이벤트 예산"). An unchanged value is never re-sent.
--
-- What is deliberately left alone:
--   - a list that is switched off, a service too old to have one, a PC that
--     does not answer: no child is created, deleted or told anything, so the
--     last value stands (a made-up "stopped" would fire every "꺼지면" routine)
--   - a child's label after it was created: the user may have renamed it, and
--     the platform has no way to change a label later anyway
--     (`try_update_metadata` carries no label)
--
-- Platform facts this relies on (lua_libs api v13, st/driver.lua and
-- st/device.lua): `driver:try_create_device` with `type = "EDGE_CHILD"`
-- needs `parent_device_id` and takes `parent_assigned_child_key`, every value
-- a string, and refuses a `device_network_id` (platform notes "Edge 런타임");
-- `driver:try_delete_device(id)` deletes a LAN or EDGE_CHILD device and returns
-- `nil, "<why>"` on a hub without the feature; a child carries
-- `parent_device_id` and `parent_assigned_child_key`; `get_parent_device()`
-- must not be called from `init`/`added` (it can block on the parent's data),
-- so this module keeps the parents it has seen and looks them up itself.

local caps = require "caps"
local discovery = require "discovery"
local features = require "features"
local i18n = require "i18n"
local profiles = require "profiles"

local apps = {}

-- How long a create request may stay unanswered before the next status asks
-- again. The child arrives asynchronously (`added`), and the statuses keep
-- coming in the meantime (a push every flip, a poll every 30 s): without this
-- every one of them would ask for the same child once more.
apps.CREATE_RETRY_SECONDS = 300

-- The row key of the child's one row (`poll.row_key`).
apps.ROW = caps.APP .. ".running"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

local function poll_module()
  return require "poll"
end

-- Per driver run, in memory: the parents seen (weak, so a deleted PC goes),
-- create requests still waiting for their `added` (parent id -> key -> time),
-- the list each parent last reported (ids and labels, for `held`), the
-- children this run has put online, and the ones it failed to delete.
local parents = setmetatable({}, { __mode = "v" })
local pending = {}
local lists = {}
local onlined = {}
local undeletable = {}
local deleting = {}

--------------------------------------------------------------------------------
-- pure
--------------------------------------------------------------------------------

--- True when `device` is a child device - an app child of this driver, or a
--- leftover display child of an older one (profiles.is_legacy_child, which
--- init.lua checks first).
function apps.is_child(device)
  if type(device) ~= "table" then
    return false
  end
  local key = device.parent_assigned_child_key
  return type(key) == "string" and key ~= ""
end

--- The `try_create_device` metadata of the child for `app` under `parent`.
-- Every value is a string (the platform refuses anything else). The model is
-- the PC's own (`PC Control · 58bff996`), so the device information screen
-- says which PC an app child belongs to.
function apps.metadata(parent, app)
  return {
    type = "EDGE_CHILD",
    label = app.label,
    profile = profiles.APP,
    parent_device_id = parent.id,
    parent_assigned_child_key = app.id,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.model_for(discovery.machine_id_of(parent)),
    vendor_provided_label = app.label,
  }
end

--- The list's identity for `held`: ids and labels in order.
function apps.list_signature(list)
  local parts = {}
  for _, app in ipairs(list or {}) do
    parts[#parts + 1] = app.id .. "=" .. app.label
  end
  return table.concat(parts, "\n")
end

--- What `sync` does about one status.
--
-- @param list `features.apps_of(status)`, in priority order
-- @param children key -> child device (`children_of`)
-- @param opts `pending(key)` true while a create for `key` is outstanding;
--   `changed` true when the list itself (ids, labels, order) differs from the
--   previous status; `shown(child)` the value the child's row shows now
-- @return { create = {app}, delete = {child}, paint = { {child, app} },
--   held = {key} } - `paint` in priority order
--
-- `held`: right after the list is edited on the PC the service lists every
-- entry as `running: false` until its next scan (the #123 contract). A child
-- that shows "running" is therefore not moved to "stopped" by the status that
-- carries a changed list; the next status, with the list unchanged, decides.
-- A real stop that coincides with a list edit is late by one status; a fake
-- one would have fired every "꺼지면" routine.
function apps.plan(list, children, opts)
  opts = opts or {}
  local out = { create = {}, delete = {}, paint = {}, held = {} }
  local wanted = {}
  for _, app in ipairs(list or {}) do
    wanted[app.id] = true
    local child = (children or {})[app.id]
    if child then
      if opts.changed and not app.running and opts.shown
          and opts.shown(child) == features.APP_RUNNING then
        out.held[#out.held + 1] = app.id
      else
        out.paint[#out.paint + 1] = { child = child, app = app }
      end
    elseif not (opts.pending and opts.pending(app.id)) then
      out.create[#out.create + 1] = app
    end
  end
  local gone = {}
  for key, child in pairs(children or {}) do
    if not wanted[key] then
      gone[#gone + 1] = { key = key, child = child }
    end
  end
  -- Deterministic order (pairs is not), so the log and the tests read the same.
  table.sort(gone, function(a, b) return a.key < b.key end)
  for _, entry in ipairs(gone) do
    out.delete[#out.delete + 1] = entry.child
  end
  return out
end

--------------------------------------------------------------------------------
-- driver glue
--------------------------------------------------------------------------------

--- Remember `parent` for this run, so a child can find it from `added`/`init`
--- and `refresh` without asking the hub (see the header).
function apps.remember_parent(parent)
  if type(parent) == "table" and type(parent.id) == "string" and parent.id ~= "" then
    parents[parent.id] = parent
  end
end

local function devices_of(driver)
  if not driver then
    return nil
  end
  local ok, devices = pcall(function() return driver:get_devices() end)
  if ok and type(devices) == "table" then
    return devices
  end
  return nil
end

--- The PC `child` belongs to, or nil: the one this run has seen, else the
--- driver's device with the child's `parent_device_id`.
-- @param remembered_only true from a lifecycle handler (no device-list walk)
function apps.parent_of(driver, child, remembered_only)
  local id = (child or {}).parent_device_id
  if type(id) ~= "string" or id == "" then
    return nil
  end
  if parents[id] then
    return parents[id]
  end
  if remembered_only then
    return nil
  end
  for _, device in ipairs(devices_of(driver) or {}) do
    if device.id == id and not apps.is_child(device) then
      parents[id] = device
      return device
    end
  end
  return nil
end

--- The app children of `parent`: key -> child, plus a list of the extra
--- children that share a key with an earlier one (a create that was answered
--- twice). nil when the device list cannot be read - nothing is created or
--- deleted on a guess.
function apps.children_of(driver, parent)
  local devices = devices_of(driver)
  if not devices then
    return nil
  end
  local out, extra = {}, {}
  for _, device in ipairs(devices) do
    -- A child this run asked the hub to delete is already gone as far as the
    -- list is concerned: the same app coming back gets a new one.
    if apps.is_child(device) and device.parent_device_id == parent.id
        and not profiles.is_legacy_child(device) and not deleting[device.id] then
      local key = device.parent_assigned_child_key
      if out[key] then
        extra[#extra + 1] = device
      else
        out[key] = device
      end
    end
  end
  return out, extra
end

local function pending_of(parent)
  local map = pending[parent.id]
  if not map then
    map = {}
    pending[parent.id] = map
  end
  return map
end

--- Ask the hub for the child of `app`. Returns true when the request went out.
function apps.create(driver, parent, app, deps)
  local ok, err = pcall(function()
    return driver:try_create_device(apps.metadata(parent, app))
  end)
  -- Remembered either way: a hub that refuses is not asked again on every
  -- push for the next `CREATE_RETRY_SECONDS`.
  pending_of(parent)[app.id] = poll_module().clock(deps)
  if not ok then
    logger().warn(string.format("could not create the child for %s on %s: %s",
      app.id, tostring(parent.id), tostring(err)))
    return false
  end
  logger().info(string.format("creating the child for %s (%s) on %s",
    app.id, app.label, tostring(parent.id)))
  return true
end

--- Delete one child. Returns true when the hub took the request.
--
-- A hub whose `try_delete_device` does not exist, raises, or answers `nil,
-- "<why>"` keeps the child: it is put offline (it no longer stands for
-- anything) and the PC's message row says so once, so the user can delete it
-- by hand. Not asked again in this run.
function apps.delete(driver, parent, child)
  if undeletable[child.id] or deleting[child.id] then
    -- Refused before, or asked already: the child leaves the device list
    -- when its `removed` arrives, and the statuses in between ask nothing.
    return false
  end
  local ok, result, why = pcall(function()
    return driver:try_delete_device(child.id)
  end)
  if ok and not (result == nil and why ~= nil) then
    deleting[child.id] = true
    logger().info(string.format("deleting the child for %s on %s",
      tostring(child.parent_assigned_child_key), tostring((parent or {}).id)))
    return true
  end
  undeletable[child.id] = true
  logger().warn(string.format("could not delete the child for %s: %s",
    tostring(child.parent_assigned_child_key), tostring(ok and why or result)))
  pcall(function() child:offline() end)
  if parent then
    local poll = poll_module()
    poll.emit_message(parent, i18n.t(poll.lang(parent), "app_child_stale", child.label or
      child.parent_assigned_child_key))
  end
  return false
end

--- Put a child online once per run. It stands for an app, not for a network
--- link, so it is never offline while it exists (except `delete`'s fallback).
local function online(child)
  if onlined[child.id] then
    return
  end
  onlined[child.id] = true
  pcall(function() child:online() end)
end

--- Emit one child's `running`. Through `poll.emit` on the child: unchanged
--- values are not sent, the first of the run is forced.
function apps.paint(child, app)
  online(child)
  poll_module().emit(child, features.app_events(app))
end

--- Bring the children of `parent` in line with one status (poll or push).
--
-- Returns what was planned (`apps.plan`, plus `mode`), or nil for a device
-- that is not a PC. Only an "on" list (features.apps_mode) changes anything.
function apps.sync(driver, parent, status, deps)
  if type(parent) ~= "table" or apps.is_child(parent) then
    return nil
  end
  apps.remember_parent(parent)
  local mode = features.apps_mode(status)
  if mode ~= features.APPS_ON then
    return { mode = mode, create = {}, delete = {}, paint = {}, held = {} }
  end
  local list = features.apps_of(status)
  local children, extra = apps.children_of(driver, parent)
  if not children then
    return { mode = mode, create = {}, delete = {}, paint = {}, held = {} }
  end

  local poll = poll_module()
  local now = poll.clock(deps)
  local waiting = pending_of(parent)
  for key in pairs(children) do
    waiting[key] = nil
  end
  local signature = apps.list_signature(list)
  local previous = lists[parent.id]
  lists[parent.id] = signature

  local plan = apps.plan(list, children, {
    changed = previous ~= nil and previous ~= signature,
    pending = function(key)
      local at = waiting[key]
      return at ~= nil and now >= at and now - at < apps.CREATE_RETRY_SECONDS
    end,
    shown = function(child)
      return poll.sent_value(child, apps.ROW)
    end,
  })
  plan.mode = mode

  for _, child in ipairs(plan.delete) do
    apps.delete(driver, parent, child)
  end
  for _, child in ipairs(extra or {}) do
    plan.delete[#plan.delete + 1] = child
    apps.delete(driver, parent, child)
  end
  -- Priority order: when several apps flip at once, the most important one
  -- goes first. SmartThings does not promise to run routines in that order.
  for _, entry in ipairs(plan.paint) do
    apps.paint(entry.child, entry.app)
  end
  for _, app in ipairs(plan.create) do
    apps.create(driver, parent, app, deps)
  end
  return plan
end

--------------------------------------------------------------------------------
-- the child's lifecycle (init.lua dispatches here; no poll, no push)
--------------------------------------------------------------------------------

--- `added` and `init` of a child: online, and painted from what its PC last
--- said, if the PC is known in this run. Otherwise the PC's next status paints
--- it (`sync`).
function apps.child_init(_driver, child)
  local parent = apps.parent_of(nil, child, true)
  if parent then
    local waiting = pending[parent.id]
    if waiting then
      waiting[child.parent_assigned_child_key] = nil
    end
  end
  online(child)
  if not parent then
    return false
  end
  local extras = poll_module().extras(parent) or {}
  for _, app in ipairs(extras.apps or {}) do
    if app.id == child.parent_assigned_child_key then
      apps.paint(child, app)
      return true
    end
  end
  return false
end

--- `removed` of a child: forget it.
function apps.child_removed(_driver, child)
  onlined[child.id] = nil
  undeletable[child.id] = nil
  deleting[child.id] = nil
end

--- `refresh` on a child: the PC is the one to ask.
function apps.refresh(driver, child)
  local parent = apps.parent_of(driver, child)
  if not parent then
    return false
  end
  return poll_module().once(driver, parent)
end

--- `removed` of a PC: its children go with it. The platform may already do
--- this itself; a delete of a child that is gone is a no-op either way.
function apps.parent_removed(driver, parent)
  local children, extra = apps.children_of(driver, parent)
  for _, child in pairs(children or {}) do
    pcall(function() driver:try_delete_device(child.id) end)
  end
  for _, child in ipairs(extra or {}) do
    pcall(function() driver:try_delete_device(child.id) end)
  end
  parents[parent.id] = nil
  pending[parent.id] = nil
  lists[parent.id] = nil
end

--- Forget everything this run remembered (tests only).
function apps.reset()
  parents = setmetatable({}, { __mode = "v" })
  pending = {}
  lists = {}
  onlined = {}
  undeletable = {}
  deleting = {}
end

return apps
