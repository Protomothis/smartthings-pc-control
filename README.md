# SmartThings PC Control

Windows service for SmartThings PC power control. Drop-in replacement for [Remote Shutdown Manager](https://github.com/karpach/remote-shutdown-pc), compatible with the [PCControl Edge driver](https://github.com/toddaustin07/PCControl).

## Features

- **Windows Service** — runs without user login (unlike Karpach which requires a desktop session)
- **Single executable** — no .NET runtime or dependencies required
- **One-command install** — `install` sets up service + firewall + auto-start
- **SmartThings Edge driver compatible** — same HTTP API as Remote Shutdown Manager
- **Web UI** — configure port and secret key from browser
- **Auto-recovery** — service restarts automatically on failure

## Supported Commands

| Command | Action |
|---------|--------|
| `ping` | Health check (returns 200 OK) |
| `shutdown` | Graceful shutdown (5 sec delay) |
| `forceshutdown` | Immediate forced shutdown |
| `restart` | Restart (5 sec delay) |
| `hibernate` | Hibernate |
| `suspend` | Suspend (sleep) |
| `lock` | Lock all active sessions |
| `turnscreenoff` | Turn off monitor |

## Installation

1. Download `smartthings-pc-control.exe` from [Releases](https://github.com/Protomothis/smartthings-pc-control/releases)
2. Run as administrator:

```
smartthings-pc-control.exe install
```

Done! The service is now running and will auto-start on boot.

## Usage

```
smartthings-pc-control.exe install     # Install and start service
smartthings-pc-control.exe uninstall   # Remove service
smartthings-pc-control.exe status      # Show status
smartthings-pc-control.exe run         # Console mode (debug)
```

## Web UI

After installation, access settings at: http://127.0.0.1:5002

- Change port and secret key
- Test commands directly from browser

## Configuration

Settings are stored in `config.json` next to the executable:

```json
{
  "port": 5001,
  "secret": ""
}
```

## SmartThings Setup

1. Install the [PCControl Edge driver](https://github.com/toddaustin07/PCControl) on your SmartThings Hub
2. Set your PC's IP address in the device settings
3. Set the same port and secret (if any) as configured in this service

## Building from Source

```bash
go build -o smartthings-pc-control.exe .
```

## License

MIT
