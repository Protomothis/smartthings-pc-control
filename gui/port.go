package gui

// Following a port change (#121). The app resolves the service's WebUI
// port (SmartThings port + 1) from tray.json (#131) at start. Saving a new
// port rewrites it at once, but the service keeps listening on the old
// one until it restarts — from the settings tab, the service section, or
// anywhere else. So the app does not switch when the port is saved: while
// the service is unreachable it looks at tray.json, and moves over only
// once something answers the API on the new port. A saved-but-not-yet-
// applied port never pulls the app away from a service that is still on
// the old one.

import "errors"

// diskWebUIPort is the port tray.json says the service uses now or
// after its next start; tests replace it.
var diskWebUIPort = localWebUIPort

// setPort points the app at the service API on webPort: u.client (kept as
// the same *Client, so the polling goroutines need no handover) and
// webUIPort for the status line, the WebUI button and the tray. Safe from
// any goroutine. Reports whether the port changed.
func (u *ui) setPort(webPort int) bool {
	if int(webUIPort.Swap(int32(webPort))) == webPort {
		return false
	}
	u.client.SetPort(webPort)
	return true
}

// followPortChange moves the client to the port in tray.json when that
// differs from the current one and a service answers there (a config or a
// login-required reply both count). Called by initialLoad after the
// current port did not answer; reports whether it moved.
func (u *ui) followPortChange() bool {
	disk := diskWebUIPort()
	if disk == currentWebUIPort() {
		return false
	}
	if _, err := NewClient(disk).GetConfig(); err != nil && !errors.Is(err, errUnauthorized) {
		return false
	}
	return u.setPort(disk)
}
