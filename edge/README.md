# SmartThings Edge driver

Edge driver (Lua 5.3) for the PC Control service. It talks to the service's
`/st/v1` protocol described in [`docs/design/edge-driver.md`](../docs/design/edge-driver.md),
which is the binding contract for both sides — when code and document disagree,
the document is fixed first.

## Layout

```
config.yml          driver metadata (name, packageKey, permissions: lan)
profiles/pc.yml     main profile: capabilities + preferences (§5.1, §5.4)
src/
  init.lua          entry point: lifecycle and capability handlers only
  caps.lua          custom capability ids, one NAMESPACE constant
  client.lua        /st/v1 HTTP client, error classification (§4, §6.1)
  discovery.lua     manual add today, SSDP hook for #73 (§4.6)
  poll.lua          poll timer, health, event emission (§6.1)
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

## Placeholder capability namespace

`src/caps.lua` and `profiles/pc.yml` both use the placeholder namespace
`pccontrol00000`. The real namespace is assigned by SmartThings when the account
owner creates the five custom capabilities
(`smartthings capabilities:create`, design doc §11.1) and both files have to be
rewritten with it — #74 ships `tools/apply-namespace.js` for that.

Until then the custom capabilities do not resolve on a hub. That is handled, not
ignored: `caps.load` skips ones it cannot find, `poll.emit` drops events for
missing capabilities with a debug log, and `switch` / `refresh` / `healthCheck`
keep working.

## Not here yet

- Custom capability JSON and presentations — #72
- Push listener, subscription renewal, SSDP search, display child device — #73
- CI workflow, CLI packaging and channel deployment, namespace script — #74

The `edge-vX.Y.Z` release tag must match `src/version.lua`; CI enforces it (#74).

## 방화벽/네트워크 요구사항 (Firewall and network requirements)

SSDP discovery is multicast, so it only works when the LAN lets it through:

- **UDP 1900 inbound** must be allowed on the PC. `pc-control.exe install`
  adds the rule *SmartThings PC Control SSDP*, and the service re-checks it at
  every start while `smartthings.discovery` is on (#76). Disabling discovery
  later leaves the rule in place; uninstall removes it.
- The network profile must be **Private**. On a Public profile Windows blocks
  inbound multicast whatever the rule says.
- Hub and PC must sit on the **same L2 segment** (same subnet/VLAN, no
  AP isolation, no client isolation on the Wi-Fi SSID). Routers do not forward
  239.255.255.250 between segments.
- Virtual adapters (Hyper-V, WSL, VPN taps) may log
  `SSDP: <name> did not join 239.255.255.250` at startup. That is normal — the
  responder skips them and keeps serving the real LAN adapters.

When discovery still finds nothing, add the device by hand as below; the
service works identically either way.

## Adding a device by hand

Until SSDP lands, "Scan nearby devices" creates one device labelled
*PC Control (set IP in settings)*. Open its settings and fill in the PC's IP
address, the secret from the PC Control settings tab, and — only if the service
cannot report a Wake-on-LAN capable adapter — the MAC address. Tapping Scan
again does not pile up blank devices: `discovery.has_unconfigured` refuses while
one is still missing an IP.

Note that Edge has no password preference type, so the secret is a plain text
field and is visible while it is typed.
