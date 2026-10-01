-- The driver's two clocks (#129: one place for what tests replace).

local clock = {}

--- `pcInfo.lastSeen`: the hub's local time of day, "14:05:12" - a value that
--- is at most a few minutes old needs no date.
function clock.now()
  return os.date("%H:%M:%S")
end

--- What `clock.epoch` falls back to without `deps.now`. A test replaces it and
--- puts it back.
clock.wallclock = os.time

--- Epoch seconds. `deps.now` wins when a caller passes one (the injection
--- client.lua, discovery.lua and push.lua take as well).
function clock.epoch(deps)
  return ((deps or {}).now or clock.wallclock)()
end

return clock
