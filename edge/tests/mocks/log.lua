-- Minimal stand-in for the hub's `log` module. Messages are kept so a test can
-- assert on them; nothing is printed so the test output stays readable.
local log = { entries = {} }

for _, level in ipairs({ "trace", "debug", "info", "warn", "error", "fatal" }) do
  log[level] = function(...)
    log.entries[#log.entries + 1] = { level = level, ... }
  end
end

function log.reset()
  log.entries = {}
end

return log
