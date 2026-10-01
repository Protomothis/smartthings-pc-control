-- SmartThings Edge driver for PC Control.
--
-- Entry point only: lifecycle and capability handlers wire the modules
-- together, all logic lives in client/state/poll/wol/discovery so it can be
-- unit-tested without a hub (design doc §2).

local Driver = require "st.driver"
local capabilities = require "st.capabilities"
local log = require "log"

local apps = require "apps"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local features = require "features"
local i18n = require "i18n"
local poll = require "poll"
local profiles = require "profiles"
local push = require "push"
local state = require "state"
local version = require "driver_version"
local wol = require "wol"

-- Custom capabilities only resolve once the account owner has created them and
-- `caps.NAMESPACE` holds the real namespace (#72). Missing ones are logged and
-- their handlers are left unregistered; switch/refresh keep working either way.
local custom, missing = caps.load(capabilities)
if #missing > 0 then
  log.warn("custom capabilities not available yet: " .. table.concat(missing, ", "))
end

--------------------------------------------------------------------------------
-- lifecycle
--------------------------------------------------------------------------------

local function device_init(driver, device)
  log.info(string.format("init %s (driver %s)", device.id, version))
  -- #81: the display child is gone. A hub that ran an older driver still has
  -- the children it created, and their profiles are no longer in the package,
  -- so they are deleted here — once per device per driver run.
  if profiles.remove_legacy_child(driver, device) then
    return
  end
  -- #123: an app child is painted from its PC and nothing else - no profile
  -- migration, no poll timer, no push subscription of its own.
  if apps.is_child(device) then
    apps.child_init(driver, device)
    return
  end
  apps.remember_parent(device)
  -- platform notes "프로필과 화면 생성": a device keeps the screen definition it was created with, so a
  -- device left on an older profile is moved to the current one, once.
  -- #107: pc*.v1 -> the current pc*.vN, with the style kept; a development
  -- device on pc*.v2 - pc*.v4 also keeps the battery half (pcToast: v5).
  local migrated = profiles.ensure(device)
  -- #100: the `iconStyle` preference changed but the driver restarted before
  -- the switch to that style's profile landed (or the hub refused it then).
  local switched = profiles.apply_style(device)
  -- #85: a migration onto the new capability ids leaves every attribute of
  -- pcRemote and pcDefer unset, which reads as "-" and keeps the app saying
  -- the device has not reported all of its state. Paint them once.
  local fresh_rows = poll.ensure_rows(device, driver)
  if fresh_rows or switched or migrated then
    -- First run on this generation of rows, or a new profile whose cloud
    -- record starts empty: every row forced once, and a look again shortly
    -- (poll.repaint_soon). #129: `ensure_rows` has just repainted when it
    -- says so - not a second time.
    poll.repaint_soon(driver, device, { painted = fresh_rows })
  end
  -- §6.3: one listener per driver, opened on the first device that needs it.
  push.start(driver)
  poll.start(driver, device)
end

local function device_added(driver, device)
  log.info("added " .. device.id)
  -- #123: a child `apps.sync` asked for has arrived.
  if apps.is_child(device) then
    apps.child_init(driver, device)
    return
  end
  -- A device that is being added was created by this driver run, so it is on
  -- the current profile: record the name now (platform notes "프로필과 화면 생성", the hub does not always
  -- expose it) and let `ensure` confirm there is nothing to migrate.
  profiles.remember(device)
  profiles.ensure(device)
  -- §6.5: a device SSDP just created arrives with the address it was found at.
  discovery.adopt(device)
  -- Paint the tiles immediately; the first poll fills in the real values.
  local initial = state.new()
  poll.set_state(device, initial)
  poll.emit_power(device, initial)
  -- #82: the command list shows `lastAction`, and an attribute that was never
  -- emitted reads as "-" on the phone. #84: the row rests on `none` for good.
  -- #85: and the same goes for every other pcRemote / pcDefer attribute,
  -- including the "command to schedule" row, whose default is the `offAction`
  -- preference.
  poll.ensure_rows(device, driver)
  if not client.device_base_url(device) then
    poll.emit_connection(device, "unreachable", i18n.t(poll.lang(device), "no_ip"))
  end
  -- The event budget: the repaint is queued (poll.paint); its first batch
  -- goes now, the rest a few seconds apart.
  poll.paint_start(driver, device)
end

local function device_removed(driver, device)
  log.info("removed " .. device.id)
  if apps.is_child(device) then
    apps.child_removed(driver, device)
    return
  end
  -- #123: the PC's app children go with it.
  apps.parent_removed(driver, device)
  poll.stop(driver, device)
  wol.cancel_wake(driver, device)
  push.stop(driver, device)
end

--- #129: did this `infoChanged` move the device onto another profile?
--
-- The hub passes the device record from before the change as
-- `args.old_st_store` (lua_libs st/driver.lua). When its profile is the one
-- the device is on now, only preferences (or the label) changed, and nothing
-- needs repainting: the cloud record is the same one, and the poll that
-- `poll.start` runs a second later sends whatever a new preference changed
-- (a new language rewrites every sentence row). When the old record is not
-- there to compare, it is the profile change it used to be assumed to be.
local function profile_changed(device, args)
  local old = (((args or {}).old_st_store) or {}).profile
  if type(old) ~= "table" then
    return true
  end
  local now = device.profile
  if type(now) ~= "table" then
    return true
  end
  if old.id ~= nil and now.id ~= nil then
    return old.id ~= now.id
  end
  if old.name ~= nil and now.name ~= nil then
    return old.name ~= now.name
  end
  return true
end

local function device_info_changed(driver, device, _event, args)
  -- Preferences are already updated on `device` here; restarting the timer
  -- picks up a new pollInterval and a poll picks up a new IP/secret/port.
  log.info("preferences changed for " .. device.id)
  -- #123: a child has no preferences; this is the user renaming it, which
  -- is theirs to do (the driver never writes a child's label after creating it).
  if apps.is_child(device) then
    return
  end
  -- #100: a new `iconStyle` moves the device onto the profile with that
  -- category (the icon). The switch fires infoChanged once more; by then
  -- `apply_style` remembers the profile it asked for and does nothing, so
  -- there is exactly one `try_update_metadata` per change.
  local switched = profiles.apply_style(device)
  -- infoChanged also fires when a profile migration (or the switch above) has
  -- landed: the cloud's record of the new profile is empty until every row is
  -- sent again, so that is repainted. #129: a preference change that moved no
  -- profile is not - it used to be a whole forced repaint (about 70 events)
  -- for, say, a new poll interval.
  poll.start(driver, device)
  if switched or profile_changed(device, args) then
    poll.repaint_soon(driver, device)
  end
end

local function device_do_configure(driver, device)
  -- #123: children are never polled themselves.
  if apps.is_child(device) then
    return
  end
  poll.start(driver, device)
end

--------------------------------------------------------------------------------
-- capability handlers
--------------------------------------------------------------------------------

-- Report a failed command through pcInfo instead of failing silently (§1).
-- A rate-limited request (§3.1) says nothing about the connection, so it is
-- logged and the tiles keep what the last poll put there.
local function report_error(device, kind, body)
  local connection = poll.connection_for(kind)
  if not connection then
    log.warn(string.format("command refused (%s) on %s", tostring(kind), device.id))
    return
  end
  local lang = poll.lang(device)
  poll.emit_connection(device, connection, poll.message_for(kind, body, lang))
end

--------------------------------------------------------------------------------
-- #93: the transition guard
--------------------------------------------------------------------------------

--- The busy value of the transition this device is in, or nil when it is in
--- none. Every handler that would move the PC's power asks this first.
--
-- While the PC is shutting down, going to sleep or coming up there is nothing
-- useful a second power command can do: it either races the one already running
-- or lands on a PC that is not there any more. The app has no way to grey a row
-- out (platform notes "상세 화면(detailView) 위젯"), so the list says "진행 중…"
-- (poll.resting_action) and the driver is what actually holds the commands back.
local function transition_of(device)
  local s = poll.get_state(device)
  if not state.is_transitioning(s) then
    return nil
  end
  return state.busy_action(s)
end

--- Answer a command the transition swallowed.
--
-- Two things have to happen, and neither of them is sending anything to the PC.
-- The row the app is watching gets a forced re-emit of its resting value, or
-- the spinner runs out into "네트워크 또는 서버 오류" (#86); and both pcInfo rows
-- say why nothing happened, until the next poll writes the normal wording back
-- (the same one-off-notice shape `opts.note` has in `poll.once`).
-- @param answer a function that re-emits the row this command arrived on
local function refuse(device, busy_action, answer)
  if answer then
    answer(device)
  end
  poll.emit_note(device, i18n.busy(poll.lang(device), busy_action))
  log.info(string.format("command held back on %s: %s",
    tostring(device.id), tostring(busy_action)))
end

--- switch.on: WoL sequence, device goes to `waking` (§6.2/§6.4).
-- #93: allowed during a transition, both of them on purpose. While `waking`,
-- re-sending the magic packet is free; while `shuttingDown`, this is the "switch
-- on cancels the grace period" path of §6.2, which is the one thing a user in
-- front of a PC that is about to go away actually wants.
local function handle_switch_on(driver, device)
  local nxt = state.transition(poll.get_state(device), "switch_on")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  -- The PC is now `waking`, so the command list says "켜는 중…" straight away
  -- rather than at the next poll - and the polls that follow will fail until
  -- the PC is up, so this is the only place that can move it.
  poll.ensure_action(device)
  wol.wake(driver, device)
end

-- The rows `switch off` is answered on (`poll.answer`).
local POWER_ROWS = {
  [state.CAP_SWITCH .. ".switch"] = true,
  [caps.POWER_STATE .. ".powerState"] = true,
}

--- switch.off: the configured off action with the service's own grace handling.
--
-- #93: not while the PC is already on its way out or coming up. The toggle is a
-- standard capability and cannot be greyed out, so the off it just sent is
-- answered by re-emitting the switch value the power state implies - which is
-- still "on" during `shuttingDown`, so the toggle springs back - and the reason
-- goes on the status rows.
local function handle_switch_off(driver, device)
  local busy_action = transition_of(device)
  if busy_action then
    return refuse(device, busy_action, function(d)
      poll.emit_power(d, poll.get_state(d), true)
    end)
  end
  local prefs = device.preferences or {}
  local command = prefs.offAction or "shutdown"
  local ok, body, kind = client.command(device, command, "default", 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  -- #129 (W2): through the answer window, like every command. The toggle is
  -- answered on the rows it is bound to: with a grace period the PC is still
  -- "on" (§6.2 `shuttingDown`), and an unchanged value would leave the
  -- toggle's spinner without an event (platform notes "상세 화면").
  poll.answer(driver, device, POWER_ROWS)
end

local function handle_refresh(driver, device)
  -- #123: pulling an app child down to refresh asks its PC.
  if apps.is_child(device) then
    return apps.refresh(driver, device)
  end
  poll.once(driver, device)
end

--- The no-argument commands of §4, mapped to the service command name of
--- §3.3. `wake` is not a service command at all — it is the WoL sequence, the
--- same thing `switch on` does.
---
--- #82 took their `pushButton` rows off the detail view (a button has no value,
--- so the phone drew "-" next to each of the eight); the screen sends
--- `execute(command)` from one list instead. The commands stay in the
--- definition and keep their handlers: a device created on an older profile
--- still shows the buttons, and they are the shape a hub-local automation or a
--- scene can call.
local BUTTONS = {
  wake = "wake",
  suspend = "suspend",
  hibernate = "hibernate",
  restart = "restart",
  shutdown = "shutdown",
  lock = "lock",
  screenOff = "turnscreenoff",
  screenOn = "turnscreenon",
}

--- §3.3 `mode` for a command sent without one, from the `buttonMode`
--- preference (§7). `default` follows whatever grace period the PC is
--- configured with (so the toast is still cancellable); `immediate` skips it.
--- The detail-view list sends `command` only, so it lands here too (#82).
local function button_mode(device)
  local mode = (device.preferences or {}).buttonMode
  if mode == "immediate" then
    return "immediate"
  end
  return "default"
end

--- Run one service command and report what happened (#82, #84).
--
-- The one path every command row takes: send it, then poll straight away so
-- the tiles show the result instead of the state from up to `pollInterval`
-- ago. What ran appears on the `lastCommand` row, which the poll fills in from
-- the service's own `last_command`; `lastAction` stays on `none` (#84).
-- `wake` never reaches the service — it is the WoL sequence.
--
-- #84: `none` is a command in the enum and does nothing on purpose. Closing
-- the detail view's list without picking anything sends the row's current
-- value (platform notes "상세 화면(detailView) 위젯"), and the row rests on `none`, so this is the path a dismissed
-- picker takes: refresh the tiles and leave the PC alone.
--
-- #86: whatever happens next, the command row is answered first with a forced
-- re-emit of its resting value. The row never changes value (it rests on
-- `none`), so without that the app keeps a spinner up until it fails with an
-- error - measured on the phone 2026-09-22 (platform notes "상세 화면(detailView) 위젯").
--
-- #93: `busyOff` and its four siblings take exactly the `none` path. They are
-- what the row rests on during a transition, so they are what a dismissed list
-- sends then, and they have to be as harmless as `none` is the rest of the
-- time. Everything else - `lock` and the screen commands included, because a PC
-- that is leaving or not up yet cannot do those either - is held back until the
-- transition is over. `wake` is the one exception: it is the WoL sequence, the
-- same thing `switch on` does, and re-sending a magic packet is free.
local function run_command(driver, device, service_command, mode, minutes)
  poll.answer_action(device)
  if service_command == nil or service_command == "" or service_command == state.ACTION_NONE
      or state.is_busy_action(service_command) then
    -- #129 (W2): the dismissed picker's refresh shares the answer window too.
    return poll.answer(driver, device, nil)
  end
  if service_command == "wake" then
    return handle_switch_on(driver, device)
  end
  local busy_action = transition_of(device)
  if busy_action then
    -- The command row was already answered by `answer_action` above.
    return refuse(device, busy_action)
  end
  minutes = math.floor(tonumber(minutes) or 0)
  local ok, body, kind = client.command(device, service_command, mode, minutes)
  if not ok then
    report_error(device, kind, body)
    return
  end
  -- #129 (W2): the row was answered above; the poll shows what the command did.
  poll.answer(driver, device, nil)
end

--- Build the handler for one no-argument command.
local function button_handler(service_command)
  return function(driver, device)
    return run_command(driver, device, service_command, button_mode(device), 0)
  end
end

--- pcRemote.execute(command, mode, minutes) — capabilities/pcRemote.json.
--- `minutes > 0` turns the same endpoint into a schedule (§3.3).
--
--- The detail-view list (#82) sends `command` alone: the mode then follows the
--- `buttonMode` preference, exactly as the buttons it replaced did, and the
--- delay is 0. An automation fills all three in.
local function handle_execute(driver, device, cmd)
  local args = (cmd or {}).args or {}
  return run_command(driver, device, args.command,
    args.mode or button_mode(device), args.minutes or 0)
end

--- pcDefer.setPlanCommand(command): what a schedule without a command of
--- its own runs (#84; moved onto the schedule capability in #85).
--
-- The detail view's schedule list can only pick the minutes (one argument per
-- list, platform notes "상세 화면(detailView) 위젯"), so the command is picked on its own row and kept in a device
-- field. Every value it can hold is a valid argument, so a dismissed picker
-- simply re-sends the current one.
--
-- #85: the row sits in the schedule card, because the app groups detail rows by
-- the capability that owns them and the user read the schedule card as
-- "minutes only" while this row lived with the PC commands.
--
-- #86: emitted with `state_change = true`. Re-picking the value the row already
-- shows (restart -> restart) changes nothing, and the app then spins until it
-- gives up with an error (platform notes "상세 화면(detailView) 위젯").
--
-- #93: and not while the PC is in a transition. Nothing goes to the service
-- here - the choice is a device field - but the row belongs to the schedule
-- that is about to run, and letting the user re-aim it mid-shutdown is the same
-- kind of "the app took it but nothing happened" the guard exists to avoid. The
-- row is answered with the value it already holds.
local function handle_set_plan_command(_driver, device, cmd)
  local args = (cmd or {}).args or {}
  local busy_action = transition_of(device)
  if busy_action then
    return refuse(device, busy_action, function(d)
      poll.emit_plan_command(d, poll.plan_command(d), true)
    end)
  end
  poll.emit_plan_command(device, args.command, true)
end

--- The command `pcDefer.schedule` runs when it carries none of its own:
--- the automation's argument first, then the `planCommand` the user picked,
--- then the `offAction` preference, else shutdown (§3.3).
local function schedule_command(device, requested)
  return state.plan_command_for(requested, poll.plan_command(device))
end

--- pcDefer.cancel(): DELETE /st/v1/schedule (§3.4). The service answers
--- `{"cancelled": false}` when there was nothing to cancel.
local handle_cancel

--- pcDefer.schedule(minutes, command?): same endpoint, minutes > 0 (§3.3).
--- `command` is optional (SmartThings list presentations send one argument);
--- see schedule_command for the fallback. An existing schedule is replaced by
--- the service, which is worth saying.
---
--- #82: the detail view is one list with the presets and a `Cancel` entry that
--- sends `minutes = 0`, because a `pushButton` row has no value and drew "-".
--- #85: and the definition now says `minimum: 0`, because the cloud validates
--- a command's arguments against the definition before the hub ever sees them -
--- with `minimum: 1` the Cancel entry only ever produced a "system error"
--- popup (platform notes "상세 화면(detailView) 위젯"). Zero minutes takes the `cancel()` path, which is still in the
--- definition and still handled for devices on an older profile. The list sends
--- the key as a string on some firmwares, hence the `tonumber`.
---
--- #88: `minutes` starts at -1, which does nothing at all. Closing the list
--- without picking anything sends the row's current value (platform notes "상세 화면(detailView) 위젯"), and the
--- row rests on `minutesPick` = "-1", so that is the path a dismissed picker
--- takes: refresh the tiles and leave the schedule alone. Before it, the row
--- rested on `status` and the phone sent `schedule(minutes: "idle")`, which the
--- cloud rejected with a network-error popup. Whatever the argument turns out
--- to be, the row is answered first with a forced re-emit of "-1" - it never
--- changes value, so an unforced event is dropped and the app spins (#86).
---
--- #91, measured on the phone: the dismissed picker's value is not merely "a
--- string on some firmwares" - it is ALWAYS a string. Closing the list bypasses
--- the presentation's `argumentType` conversion, so `schedule("-1")` went out
--- against a definition that said `minutes: integer` and the cloud answered
--- 422 (`string found, integer expected`) - the network-error popup again. The
--- argument is a string enum now (`"-1"`, `"0"`, and the sixteen presets), so
--- every value the app can send is valid on arrival. Nothing changes here: the
--- `tonumber` below has always read the string keys, and it still accepts the
--- integers an older profile's automation may carry.
local function handle_schedule(driver, device, cmd)
  local args = (cmd or {}).args or {}
  poll.answer_minutes_pick(device)
  -- A `schedule` with no minutes at all is the same "nothing was picked" case.
  local minutes = math.floor(tonumber(args.minutes) or state.MINUTES_NONE)
  if minutes < 0 then
    return poll.answer(driver, device, nil)
  end
  if minutes == 0 then
    return handle_cancel(driver, device)
  end
  -- #93: cancelling (above) stays open during a transition - it is the one
  -- thing that can still help - but setting a NEW schedule does not. The delay
  -- row has already been answered by `answer_minutes_pick`.
  local busy_action = transition_of(device)
  if busy_action then
    return refuse(device, busy_action)
  end
  local had_schedule = poll.get_state(device).schedule_active == true
  local ok, body, kind = client.command(device, schedule_command(device, args.command), "default", minutes)
  if not ok then
    report_error(device, kind, body)
    return
  end
  -- #86: the rows the schedule list is bound to answer this command. #129
  -- (W2): through the shared answer window, like every other command.
  poll.answer(driver, device, poll.SCHEDULE_ROWS,
    had_schedule and i18n.t(poll.lang(device), "schedule_replaced") or nil)
end

function handle_cancel(driver, device)
  local ok, body, kind = client.cancel(device)
  if not ok then
    report_error(device, kind, body)
    return
  end
  local cancelled = (body or {}).cancelled == true
  -- §6.2: cancelling the grace period brings the switch back on.
  local nxt = state.transition(poll.get_state(device), "schedule_cancelled")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  -- #86: cancelling with nothing scheduled leaves every schedule row exactly
  -- as it was, which is precisely when the app's spinner used to end in an
  -- error. Forced, the rows go out anyway and the spinner finishes (platform
  -- notes "상세 화면(detailView) 위젯"). #129 (W2): through the answer window.
  poll.answer(driver, device, poll.SCHEDULE_ROWS,
    i18n.t(poll.lang(device), cancelled and "schedule_cancelled" or "schedule_none"))
end

--------------------------------------------------------------------------------
-- #107: the v1.2.0 commands (volume, mute, media keys, …)
--------------------------------------------------------------------------------

--- Run one v1.2.0 command (media-notify.md §3, §5).
--
-- Unlike the power commands these need something the PC may not have: a
-- service new enough to know them (`status.features`), the feature itself, and
-- for audio and media a logged-in user. The last status already said which
-- (`features.remember`), so a command that cannot work is not sent at all -
-- the row it came from is answered with its current value (a spinner that
-- never gets an event ends in an error, platform notes "상세 화면(detailView)
-- 위젯") and both pcInfo rows say why, until the next poll writes them back.
-- A command the service refuses (`409 no_user_session`, `403 media_disabled`)
-- is answered the same way; any other failure is `report_error`'s.
--
-- Nothing here is held back by a power transition (#93): a volume change on a
-- PC that is about to shut down does no harm, and one on a PC that is still
-- waking fails as unreachable like any other request.
-- @param answer re-emits, forced, the row the command arrived on
-- @param rows the row keys (`poll.row_key`) the poll after a success answers
local function run_feature(driver, device, service_command, value, answer, rows)
  local lang = poll.lang(device)
  if poll.extras(device) == nil then
    -- Nothing read in this driver run yet (the hub has just restarted): ask
    -- once, rather than calling a v1.2.0 PC too old.
    poll.once(driver, device)
  end
  local refusal = features.refusal(poll.extras(device), service_command)
  if refusal then
    if answer then
      answer(device)
    end
    poll.emit_note(device, i18n.t(lang, refusal))
    log.info(string.format("%s not sent on %s: %s", tostring(service_command),
      tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.action(device, service_command, value)
  if not ok then
    if answer then
      answer(device)
    end
    local note = features.error_note(kind, body)
    if note then
      poll.emit_note(device, i18n.t(lang, note))
      return false
    end
    report_error(device, kind, body)
    return false
  end
  -- The event budget (platform notes "이벤트 예산(rate limit)"): commands that
  -- land within `poll.ANSWER_WINDOW_SECONDS` of each other share one answer
  -- poll, and that poll sends only what changed plus `rows`, forced.
  poll.answer(driver, device, rows)
  return true
end

-- The rows a successful command is answered on: the poll that follows it emits
-- them forced, so the spinner ends even when the value did not change (volume
-- already at 100, mute pressed on a muted PC).
local AUDIO_ROWS = {
  [features.CAP_VOLUME .. ".volume"] = true,
  [features.CAP_MUTE .. ".mute"] = true,
}
local MEDIA_ROWS = {
  [features.CAP_PLAYBACK .. ".supportedPlaybackCommands"] = true,
  [features.CAP_PLAYBACK .. ".playbackStatus"] = true,
  [features.CAP_TRACK .. ".supportedTrackControlCommands"] = true,
}

-- What a refused command is answered with: the value the row already had.
local function answer_volume(device)
  local audio = (poll.extras(device) or {}).audio or {}
  if audio.volume ~= nil then
    poll.emit(device, { { cap = features.CAP_VOLUME, attr = "volume", value = audio.volume, force = true } })
  end
end

local function answer_mute(device)
  local audio = (poll.extras(device) or {}).audio or {}
  if audio.muted ~= nil then
    poll.emit(device, { { cap = features.CAP_MUTE, attr = "mute",
      value = audio.muted and "muted" or "unmuted", force = true } })
  end
end

-- The media rows answer with what the last status said is playing (#118), and
-- with the constant attributes, which are all there is before #117.
local function answer_media(device)
  poll.emit(device, poll.force_all(features.media_events((poll.extras(device) or {}).playback)))
end

local function audio_command(service_command, answer)
  return function(driver, device)
    return run_feature(driver, device, service_command, nil, answer, AUDIO_ROWS)
  end
end

local function media_command(service_command)
  return function(driver, device)
    return run_feature(driver, device, service_command, nil, answer_media, MEDIA_ROWS)
  end
end

--- audioVolume.setVolume(volume): the slider. 0-100, rounded.
local function handle_set_volume(driver, device, cmd)
  local volume = features.volume_of({ volume = ((cmd or {}).args or {}).volume })
  if volume == nil then
    return answer_volume(device)
  end
  return run_feature(driver, device, "volume", volume, answer_volume, AUDIO_ROWS)
end

--- audioMute.setMute(state): the routine action's "muted"/"unmuted".
local function handle_set_mute(driver, device, cmd)
  local wanted = ((cmd or {}).args or {}).state
  local service_command = wanted == "muted" and "mute" or "unmute"
  return run_feature(driver, device, service_command, nil, answer_mute, AUDIO_ROWS)
end

-- mediaPlayback.setPlaybackStatus(status) -> the media key that asks for it.
local PLAYBACK_FOR_STATUS = { playing = "play", paused = "pause", stopped = "stop" }

local function handle_set_playback_status(driver, device, cmd)
  local wanted = ((cmd or {}).args or {}).status
  local service_command = PLAYBACK_FOR_STATUS[tostring(wanted or "")]
  if not service_command then
    return answer_media(device)
  end
  return run_feature(driver, device, service_command, nil, answer_media, MEDIA_ROWS)
end

--- #113: pcPreset.run(slot) — `/st/v1/command {command:"preset", value:N}`.
--
-- The list rests on "none" (platform notes "상세 화면(detailView) 위젯": a
-- dismissed list sends the row's current value), so `run("none")` is the
-- dismissed picker and does nothing but answer the row.
--
-- A preset that ran shows as "프리셋 3 실행함" for `poll.PRESET_HOLD_SECONDS`,
-- then a timer puts the row back on "none" (`poll.hold_preset`, with the
-- polls as the fallback - `poll.ensure_preset`, the `lastAction` rules).
-- During that moment the row
-- rests on "3", so a dismissed list sends `run("3")` - which must not start
-- the preset a second time. A `run` of the very slot the row is showing is
-- therefore the same no-op as `none`. Picking the same preset again on purpose
-- works as soon as the row is back on "프리셋 선택…".
local function handle_preset_run(driver, device, cmd)
  local slot = tostring((((cmd or {}).args or {}).slot) or features.PRESET_NONE)
  if slot == features.PRESET_NONE or not features.is_preset_slot(slot)
      or slot == poll.shown_preset(device) then
    return poll.answer_preset(device)
  end
  local lang = poll.lang(device)
  if poll.extras(device) == nil then
    poll.once(driver, device)
  end
  local extras = poll.extras(device)
  local refusal = features.refusal(extras, "preset")
  if not refusal and features.has(extras, features.PRESETS)
      and not ((extras or {}).preset_slots or {})[slot] then
    refusal = "preset_empty"
  end
  if refusal then
    poll.answer_preset(device)
    poll.emit_note(device, i18n.t(lang, refusal, slot))
    log.info(string.format("preset %s not sent on %s: %s", slot, tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.action(device, "preset", tonumber(slot))
  if not ok then
    poll.answer_preset(device)
    local note = features.error_note(kind, body)
    if note then
      poll.emit_note(device, i18n.t(lang, note))
      return false
    end
    report_error(device, kind, body)
    return false
  end
  -- The row the app is watching changes value, forced as every answer is.
  poll.emit_preset(device, slot, true)
  -- And back on "none" a few seconds later, not at the next scheduled poll.
  poll.hold_preset(driver, device)
  -- The event budget: shared with any command that lands right after it.
  poll.answer(driver, device, nil)
  return true
end

--------------------------------------------------------------------------------
-- #108: PC notifications
--------------------------------------------------------------------------------

--- `pcToast.send(text)` -> `POST /st/v1/notify {text}`.
--
-- The row is bound to `lastMessage`, and the app spins until an event on it
-- arrives (a row bound to nothing ended in "네트워크 오류", platform notes
-- "상세 화면(detailView) 위젯"). So every `send` is answered there, forced: a
-- text that went out becomes the row's value, and on every other path the row
-- re-emits what it already shows. The outcome in words goes to
-- `pcInfo.message` only - a routine may send several a minute, and the summary
-- row is the one the user reads the PC's state from: "PC에 메시지를
-- 보냈습니다", or why not. Gated like the other v1.2.0 commands (`features
-- "notify"`), and the text is cleaned and cut to the service's 200 characters
-- before it goes out.
local function handle_toast_send(driver, device, cmd)
  local text = ((cmd or {}).args or {}).text
  local lang = poll.lang(device)
  local cleaned = features.notify_text(text)
  if not cleaned then
    poll.answer_toast(device)
    poll.emit_message(device, i18n.t(lang, "notify_empty"))
    return false
  end
  if poll.extras(device) == nil then
    poll.once(driver, device)
  end
  local refusal = features.refusal(poll.extras(device), nil, features.NOTIFY)
  if refusal then
    poll.answer_toast(device)
    poll.emit_message(device, i18n.t(lang, refusal))
    log.info(string.format("notification not sent on %s: %s", tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.notify(device, cleaned)
  if not ok then
    poll.answer_toast(device)
    local note = features.notify_error_note(kind, body)
    if note then
      poll.emit_message(device, i18n.t(lang, note))
      return false
    end
    report_error(device, kind, body)
    return false
  end
  poll.emit_toast(device, cleaned)
  poll.emit_message(device, i18n.t(lang, "notify_sent"))
  return true
end

--------------------------------------------------------------------------------
-- #115: the `awake` component's switch
--------------------------------------------------------------------------------

local AWAKE_ROWS = {
  [features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch"] = true,
}

--- Spring the keep-awake toggle back to what the PC last said.
local function answer_awake(device)
  local on = (poll.extras(device) or {}).awake_on == true
  poll.emit(device, { {
    cap = features.CAP_SWITCH, attr = "switch", value = on and "on" or "off",
    component = features.AWAKE_COMPONENT, force = true,
  } })
end

--- switch.on on `awake`: keep the PC from idle sleep for `awakeMinutes`
--- (0 = until switched off). Sent again while it is on, it starts a new period
--- from now (§12). Not held back by a power transition - it does not move the
--- PC's power, and on a PC that is going away it simply fails.
local function handle_awake_on(driver, device)
  return run_feature(driver, device, "awake", features.awake_minutes(device.preferences),
    answer_awake, AWAKE_ROWS)
end

local function handle_awake_off(driver, device)
  return run_feature(driver, device, "awakeoff", nil, answer_awake, AWAKE_ROWS)
end

--- The standard `switch` is on two components now: `main` is the PC's power,
--- `awake` is keep-awake. The hub hands both to the same capability handler,
--- with `command.component` saying which.
local function is_awake(cmd)
  return type(cmd) == "table" and cmd.component == features.AWAKE_COMPONENT
end

local function handle_any_switch_on(driver, device, cmd)
  if is_awake(cmd) then
    return handle_awake_on(driver, device)
  end
  return handle_switch_on(driver, device)
end

local function handle_any_switch_off(driver, device, cmd)
  if is_awake(cmd) then
    return handle_awake_off(driver, device)
  end
  return handle_switch_off(driver, device)
end

local capability_handlers = {
  [capabilities.switch.ID] = {
    [capabilities.switch.commands.on.NAME] = handle_any_switch_on,
    [capabilities.switch.commands.off.NAME] = handle_any_switch_off,
  },
  [capabilities.refresh.ID] = {
    [capabilities.refresh.commands.refresh.NAME] = handle_refresh,
  },
  -- #107: standard capabilities, so their ids and command names are the
  -- platform's own and resolve on every hub.
  [capabilities.audioVolume.ID] = {
    [capabilities.audioVolume.commands.setVolume.NAME] = handle_set_volume,
    [capabilities.audioVolume.commands.volumeUp.NAME] = audio_command("volumeup", answer_volume),
    [capabilities.audioVolume.commands.volumeDown.NAME] = audio_command("volumedown", answer_volume),
  },
  [capabilities.audioMute.ID] = {
    [capabilities.audioMute.commands.mute.NAME] = audio_command("mute", answer_mute),
    [capabilities.audioMute.commands.unmute.NAME] = audio_command("unmute", answer_mute),
    [capabilities.audioMute.commands.setMute.NAME] = handle_set_mute,
  },
  [capabilities.mediaPlayback.ID] = {
    [capabilities.mediaPlayback.commands.play.NAME] = media_command("play"),
    [capabilities.mediaPlayback.commands.pause.NAME] = media_command("pause"),
    [capabilities.mediaPlayback.commands.stop.NAME] = media_command("stop"),
    [capabilities.mediaPlayback.commands.setPlaybackStatus.NAME] = handle_set_playback_status,
  },
  [capabilities.mediaTrackControl.ID] = {
    [capabilities.mediaTrackControl.commands.nextTrack.NAME] = media_command("next"),
    [capabilities.mediaTrackControl.commands.previousTrack.NAME] = media_command("prev"),
  },
}

-- Command names are literals: they are what `capabilities/pcRemote.json` and
-- `capabilities/pcDefer.json` declare, and the generated capability object
-- only carries them once the account owner has created the capabilities.
if custom.command then
  local handlers = { execute = handle_execute }
  for name, service_command in pairs(BUTTONS) do
    handlers[name] = button_handler(service_command)
  end
  capability_handlers[custom.command.ID] = handlers
end
if custom.preset then
  capability_handlers[custom.preset.ID] = { run = handle_preset_run }
end
if custom.toast then
  capability_handlers[custom.toast.ID] = { send = handle_toast_send }
end
if custom.schedule then
  capability_handlers[custom.schedule.ID] = {
    -- #85: `setPlanCommand` moved here with the row it drives.
    setPlanCommand = handle_set_plan_command,
    cancel = handle_cancel,
    schedule = handle_schedule,
  }
end

--------------------------------------------------------------------------------

--- Driver shutdown (hub restart, driver update): drop every device's push
--- subscription and close the listener so the service does not keep posting
--- to a port nobody listens on until the TTL runs out.
local function driver_lifecycle(driver, event)
  if event ~= "shutdown" then
    return
  end
  local ok, devices = pcall(function() return driver:get_devices() end)
  for _, device in ipairs(ok and devices or {}) do
    -- #123: an app child has no subscription of its own.
    if not apps.is_child(device) then
      pcall(function() push.stop(driver, device) end)
    end
  end
  pcall(function() push.shutdown(driver) end)
  log.info("driver shutting down: push subscriptions released")
end

local pc_driver = Driver("smartthings-pc-control", {
  discovery = discovery.handle,
  driver_lifecycle = driver_lifecycle,
  lifecycle_handlers = {
    init = device_init,
    added = device_added,
    removed = device_removed,
    infoChanged = device_info_changed,
    doConfigure = device_do_configure,
  },
  capability_handlers = capability_handlers,
})

log.info("starting smartthings-pc-control driver " .. version)
pc_driver:run()

-- The hub ignores what this chunk returns; the driver object is returned so
-- tests can call the lifecycle handlers exactly as the hub would.
return pc_driver
