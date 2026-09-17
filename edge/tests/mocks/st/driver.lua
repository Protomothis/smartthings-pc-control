-- Minimal `st.driver` stand-in: records the template and provides the timer
-- API the driver uses. Timers are recorded, not run; a test drives them by
-- calling `driver:fire(name)`.

local Driver = {}
Driver.__index = Driver

local function new(name, template)
  local self = setmetatable({}, Driver)
  self.NAME = name
  self.template = template or {}
  self.devices = {}
  self.timers = {}
  self.running = false
  for k, v in pairs(self.template) do
    self[k] = v
  end
  return self
end

function Driver:call_with_delay(delay, fn, name)
  local timer = { kind = "delay", delay = delay, fn = fn, name = name, cancelled = false }
  self.timers[#self.timers + 1] = timer
  return timer
end

function Driver:call_on_schedule(interval, fn, name)
  local timer = { kind = "schedule", interval = interval, fn = fn, name = name, cancelled = false }
  self.timers[#self.timers + 1] = timer
  return timer
end

function Driver:cancel_timer(timer)
  if timer then
    timer.cancelled = true
  end
end

function Driver:get_devices()
  return self.devices
end

function Driver:try_create_device(spec)
  self.created = self.created or {}
  self.created[#self.created + 1] = spec
  return true
end

function Driver:try_delete_device(id)
  self.deleted = self.deleted or {}
  self.deleted[#self.deleted + 1] = id
  return true
end

--- Run the callback of the first timer named `name`.
function Driver:fire(name)
  for _, timer in ipairs(self.timers) do
    if timer.name == name and not timer.cancelled then
      return timer.fn()
    end
  end
  error("no timer named " .. tostring(name), 0)
end

function Driver:run()
  self.running = true
end

return setmetatable({}, {
  __call = function(_, name, template) return new(name, template) end,
})
