package power

import (
	"sync"
	"time"
)

// Last executed power command (edge-driver doc §3.5, "power.stopping"
// data.reason). Windows tells the service that it is stopping, and that
// the machine is suspending, but not why: SERVICE_CONTROL_SHUTDOWN looks
// the same for `shutdown /s` and `shutdown /r`, and PBT_APMSUSPEND looks
// the same for sleep and hibernation. The command this service ran just
// before is the best hint available, so Hint remembers it.

// commandReason maps a catalogue command to the reason the Edge driver's
// power state machine understands (§6.2).
var commandReason = map[string]string{
	"shutdown":      "shutdown",
	"forceshutdown": "shutdown",
	"restart":       "restart",
	"suspend":       "suspend",
	"hibernate":     "hibernate",
}

// hintTTL is how long a command stays a plausible explanation for a stop.
// The commands wait 5s (`shutdown /t 5`) and Windows then takes a while to
// tell the services, so this is generous.
const hintTTL = 2 * time.Minute

// Hint is the last power command and when it ran.
type Hint struct {
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	command string
	at      time.Time
}

func (h *Hint) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Note remembers command when it is one that ends the session; anything
// else (ping, lock, screen) leaves the hint alone.
func (h *Hint) Note(command string) {
	if _, ok := commandReason[command]; !ok {
		return
	}
	h.mu.Lock()
	h.command, h.at = command, h.now()
	h.mu.Unlock()
}

// Reason explains a stop for power.stopping. A power command run in the
// last two minutes wins; otherwise fallback is used, which is what the
// caller could work out on its own ("shutdown" for a system shutdown,
// "suspend" for a suspend broadcast, "unknown" for a plain service stop).
func (h *Hint) Reason(fallback string) string {
	h.mu.Lock()
	cmd, at := h.command, h.at
	h.mu.Unlock()
	if cmd != "" && h.now().Sub(at) <= hintTTL {
		if reason, ok := commandReason[cmd]; ok {
			return reason
		}
	}
	return fallback
}

// Reset forgets the hint (tests).
func (h *Hint) Reset() {
	h.mu.Lock()
	h.command, h.at = "", time.Time{}
	h.mu.Unlock()
}
