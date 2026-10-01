-- What a status body says about Wake-on-LAN (design doc §6.4). Pure.

local wolinfo = {}

-- The first MAC of a WoL-capable adapter, for a service too old to choose one.
local function first_wol_mac(status)
  local adapters = (status or {}).wol and status.wol.adapters
  if type(adapters) ~= "table" then
    return nil
  end
  local fallback
  for _, a in ipairs(adapters) do
    if type(a) == "table" and type(a.mac) == "string" and a.mac ~= "" then
      if a.wol_enabled == true then
        return a.mac
      end
      fallback = fallback or a.mac
    end
  end
  return fallback
end

--- The adapter the service picked for WoL, or nil. The PC knows which NIC the
--- hub reaches; "the first adapter with WoL on" lands on a Hyper-V or VPN
--- adapter as easily. `wol.selected`, or the adapter marked `selected` (a
--- service may carry either).
function wolinfo.wol_selected(status)
  local wol = (status or {}).wol
  if type(wol) ~= "table" then
    return nil
  end
  if type(wol.selected) == "table" and next(wol.selected) ~= nil then
    return wol.selected
  end
  if type(wol.adapters) == "table" then
    for _, a in ipairs(wol.adapters) do
      if type(a) == "table" and a.selected == true then
        return a
      end
    end
  end
  return nil
end

--- The MAC to wake this PC on: the chosen adapter's, else the old guess. The
--- `macAddress` preference outranks both (wol.lua: it is the user's own value).
function wolinfo.wol_mac(status)
  local selected = wolinfo.wol_selected(status)
  if selected and type(selected.mac) == "string" and selected.mac ~= "" then
    return selected.mac
  end
  return first_wol_mac(status)
end

--- The name of the chosen adapter ("이더넷"), or nil.
function wolinfo.wol_adapter(status)
  local selected = wolinfo.wol_selected(status)
  local name = selected and selected.name
  if type(name) == "string" and name ~= "" then
    return name
  end
  return nil
end

--- Is the "WoL is off" warning due? The chosen adapter's `wol_enabled` when
--- there is one - it is about the NIC the packet goes to - else the service's
--- own `wol.ready`.
function wolinfo.wol_off(status)
  local selected = wolinfo.wol_selected(status)
  if selected ~= nil and selected.wol_enabled ~= nil then
    return selected.wol_enabled ~= true
  end
  return ((status or {}).wol or {}).ready ~= true
end

return wolinfo
