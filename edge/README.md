# SmartThings Edge driver

Edge driver (Lua 5.3) for the PC Control service. It talks to the service's
`/st/v1` protocol described in [`docs/design/edge-driver.md`](../docs/design/edge-driver.md),
which is the binding contract for both sides — when code and document disagree,
the document is fixed first.

## Layout

```
config.yml          driver metadata (name, packageKey, permissions: lan)
profiles/pc.yml     main profile: capabilities + preferences (§5.1, §5.4)
profiles/pc-display.yml  display child profile: one switch (§5.2)
capabilities/       custom capability definitions + presentations (§5.1, §5.3)
src/
  init.lua          entry point: lifecycle and capability handlers only
  caps.lua          custom capability ids, one NAMESPACE constant
  client.lua        /st/v1 HTTP client, error classification (§4, §6.1)
  discovery.lua     SSDP search, manual add, multi-PC identity (§4.6, §13)
  display.lua       the display child device (§5.2)
  poll.lua          poll timer, health, event emission, stagger (§6.1, §13.3)
  push.lua          push listener and subscriptions (§4.5, §6.4)
  state.lua         pure: status JSON -> events, power state machine (§6.2)
  wol.lua           magic packet, wake sequence (§6.3)
  i18n.lua          ko/en strings for user-visible attribute values (§6.5)
  version.lua       driver version (User-Agent, release tag check)
tests/
  run.lua           test runner
  syntax.lua        compiles every module without running it
  helpers.lua       assertions and a fake device
  mocks/            log, ltn12, cosock, st.json, st.utils, st.driver, st.capabilities
  *_test.lua
tools/lua.js        fengari-based `lua <file>` runner
```

`src/` is the package root on the hub, so modules require each other by plain
name (`require "state"`), never `require "src.state"`.

## Running the tests

There is no Lua interpreter to install: [fengari](https://fengari.io/) is a
Lua 5.3 implementation in JavaScript and runs the same dialect as the hub.

```sh
cd edge
bun install                        # or: npm install
bun tools/lua.js tests/run.lua     # or: npm test
bun tools/lua.js tests/syntax.lua  # or: npm run syntax
```

Only `package.json` is committed; no lockfile is, so CI uses `npm install`
rather than `npm ci` until #74 decides to commit one.

`run.lua` discovers `tests/*_test.lua`, calls every function named `test_*` in
the table each file returns, prints one line per test and exits non-zero on the
first failure. Adding a test file needs no registration.

The hub modules (`st.*`, `cosock`, `ltn12`) do not exist locally, so they are
preloaded from `tests/mocks/` by `run.lua` and the modules under test require
them lazily. Anything that would touch a socket takes an injected dependency
instead: `client.get_status(device, { http = fake })`,
`wol.send(mac, broadcast, { socket = fake })`. `cosock.asyncify` deliberately
raises in tests so a real HTTP call cannot slip through.

`state.lua` is where the interesting logic lives and it is pure: it returns
`{ cap, attr, value }` records instead of emitting, so the status mapping and
the whole power state machine are testable without a hub.

## Custom capabilities

`capabilities/` holds the five custom capabilities of design doc §5.1, two
files each:

```
pcPowerState.json               definition   -> smartthings capabilities:create -i <file>
pcPowerState.presentation.json  presentation -> smartthings capabilities:presentation:create
```

`pcPowerState` carries the power state, `pcCommand` runs one of the eight
service commands, `pcSchedule` shows and edits the pending schedule, `pcStatus`
is the connection/version/message card and `pcSession` the opt-in lock and idle
block.

`tests/capabilities_test.lua` keeps them honest against the Lua: same ids, same
attributes as `state.attributes_used()`, same commands as the handlers in
`init.lua`, and presentations that reference nothing undefined. What it cannot
check is whether SmartThings accepts the *shape* of these files — the CLI is the
only authority on that, and these are the parts to confirm when creating them:

- `id`, `version`, `status` and `ephemeral` are sent in the definition; the
  CLI may ignore or reject them and assign its own.
- `enumCommands: []` is spelled out on every attribute, including the
  non-enum ones.
- the detail-view and automation-action entries for `pcCommand.execute` and
  `pcSchedule.schedule` use `displayType: "multiArgCommand"` with a
  per-argument `displayType`, and the `minutes` preset list (5/15/30/60/120)
  relies on `alternatives` being allowed on a command argument. If either is
  refused, fall back to a `numberField` for `minutes`.
- `state`, `list`, `pushButton` and `numberField` are assumed to be valid
  `displayType` values for a capability presentation.

## Placeholder capability namespace

`src/caps.lua`, `profiles/pc.yml` and the ids in `capabilities/*.json` all use
the placeholder namespace `pccontrol00000`. The real namespace is assigned by
SmartThings when the account owner creates the five custom capabilities
(`smartthings capabilities:create`, design doc §11.1) and all three have to be
rewritten with it — #74 ships `tools/apply-namespace.js` for that.

Until then the custom capabilities do not resolve on a hub. That is handled, not
ignored: `caps.load` skips ones it cannot find, `poll.emit` drops events for
missing capabilities with a debug log, and `switch` / `refresh` / `healthCheck`
keep working.

## Push, discovery and several PCs

The driver opens **one TCP listener per driver** on an ephemeral port
(`src/push.lua`) and subscribes each device to it with
`POST /st/v1/subscribe` after every successful poll, renewing at 80% of the
600 s TTL. Every event body carries `machine_id` at the top level, which is
how one hub routes several PCs to the right device (§13.3); an unknown
`machine_id` is logged and dropped. A push carries the whole §4.2 status, so it
goes through the very same `state.apply_status` path a poll uses. If the
subscription fails — wrong secret, callback refused, PC unreachable — it is
dropped and polling alone keeps the tiles correct.

Discovery multicasts `M-SEARCH` for `urn:smartthings-pc-control:device:pc:1`
and fetches `GET LOCATION` (`/st/v1/description`) from every responder. The
identity is the `machine_id` (§13.1): a hit whose machine_id is already known
updates that device's address instead of creating a second one, including for a
manually added device, which stores its machine_id from the first successful
status. `followDiscovery` (default on) lets the device follow the PC around the
LAN; a filled-in `ipAddress` preference always wins over what SSDP reports. A
device that goes unreachable does one targeted search before its next poll, at
most once every five minutes. Two responders sharing a machine_id but reporting
different hostnames are a cloned Windows image, and the driver says so in
`pcStatus.message` — the fix is to regenerate the MachineGuid on one of them.

Polls are staggered by a hash of the device network id (§13.3), so N PCs are
not all asked in the same second.

The display child (`src/display.lua`, `profiles/pc-display.yml`) appears once
the parent knows its machine_id and `createDisplayDevice` is on. Its switch
runs `turnscreenon` / `turnscreenoff` on the parent and follows `status.display`
(`unknown` leaves it alone). Turning the preference off, or removing the PC,
removes the child.

### Assumptions to confirm on a real hub

These are the parts no local test can check, because they are hub runtime
behaviour rather than logic:

- `cosock.socket.tcp()` bound to `0.0.0.0:0` plus `getsockname()` gives a port
  the LAN can reach, and `accept()` inside a `cosock.spawn` task does not
  starve the driver's other tasks.
- the hub IP: `driver:get_ip()` is used when the firmware has it, otherwise a
  UDP socket `setpeername`'d at the PC and read back with `getsockname()`.
  The service requires the callback host to equal the request source IP, so
  a wrong answer here shows up as a `400` on subscribe and nothing else.
- `EDGE_CHILD` device creation: `profile`, `parent_device_id` and
  `parent_assigned_child_key`, and whether `try_delete_device` exists on the
  device, on the driver, or both.
- `Switch` as the `categories` entry of `pc-display.yml`.
- `device:set_field(..., { persist = true })` keeps the discovered address and
  machine_id across a driver restart.
- multicast: whether the hub lets an Edge driver send to 239.255.255.250:1900
  and receive the unicast answers on the same socket.

## Not here yet

- CI workflow, CLI packaging and channel deployment, namespace script — #74

The `edge-vX.Y.Z` release tag must match `src/version.lua`; CI enforces it (#74).

## Adding a device by hand

When no PC answers the SSDP search — discovery turned off in the service, or a
network that drops multicast — "Scan nearby devices" creates one device labelled
*PC Control (set IP in settings)*. Open its settings and fill in the PC's IP
address, the secret from the PC Control settings tab, and — only if the service
cannot report a Wake-on-LAN capable adapter — the MAC address. Tapping Scan
again does not pile up blank devices: `discovery.has_unconfigured` refuses while
one is still missing an IP. A device added this way adopts its `machine_id` from
its first successful status, so a later SSDP hit updates it instead of adding a
duplicate (§13.1).

A device SSDP found arrives with its IP and port already filled in, so only the
secret (and, if needed, the MAC) is left to type.

Note that Edge has no password preference type, so the secret is a plain text
field and is visible while it is typed.
