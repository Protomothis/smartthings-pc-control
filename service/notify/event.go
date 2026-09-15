// Package notify carries service events to notification channels (v1.0,
// issue #55). See docs/design/v1.0-notifications.md — the type names and
// event catalogue there are the contract shared by every package.
package notify

import (
	"context"
	"time"
)

// Event is one thing that happened in the service, addressed by
// Category.Kind (see the catalogue in the design doc). Fields hold
// template variables whose values are already human-readable ("5분",
// "14:35:00"); Actions become inline buttons on channels that support them.
type Event struct {
	Category string
	Kind     string
	At       time.Time
	Fields   map[string]string
	Actions  []Action
}

// Action is an inline button. Data is the callback payload in the form
// "<verb>:<arg>", e.g. "cancel:", "runnow:", "cmd:shutdown", "confirm:shutdown".
type Action struct {
	Label string
	Data  string
}

// Sink is one delivery channel (Telegram, ...).
type Sink interface {
	Send(ctx context.Context, ev Event) error
}

// Key returns "category.kind", the identifier used for config keys,
// template file names and log lines.
func (e Event) Key() string { return e.Category + "." + e.Kind }
