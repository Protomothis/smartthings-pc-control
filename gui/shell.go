package gui

// What the Gio app shell (package ui, #165) needs from here besides the
// client and the texts: the single-instance slot, the start-up chores, the
// app log, debug mode, the update check and the save model. Thin exports of
// the toolkit-free parts the Fyne window uses too, so both apps behave the
// same; no toolkit in this file.

import (
	"errors"

	"github.com/Protomothis/smartthings-pc-control/internal/release"
)

// --- single instance -------------------------------------------------------

// Instance is this process's claim on the one-app-per-session slot
// (singleinstance.go).
type Instance struct{ inst guiInstance }

// ClaimInstance claims the slot; First tells whether this process got it.
func ClaimInstance() Instance {
	return Instance{claimInstance(instanceMutexName, activateEventName)}
}

// First reports whether no other app holds the slot: this process is the
// app. Otherwise it should ActivateRunning (unless minimized) and exit.
func (i Instance) First() bool { return i.inst.first }

// ActivateRunning asks the running app to open its window.
func (i Instance) ActivateRunning() { activateRunning(i.inst) }

// WatchActivation calls fn (on a goroutine of its own) each time a later
// launch asks this app to open its window, until stop is called.
func (i Instance) WatchActivation(fn func()) (stop func()) {
	return watchActivation(i.inst.activate, fn)
}

// --- start-up chores and the log -----------------------------------------

// PrepareStart does what every app start does before the window: the
// stpc:// protocol of the toast buttons (re-pointed at this exe), the Start
// menu shortcut whose AUMID lets toasts show as banners, and the leftovers
// of a previous self-update. The last two run in the background.
func PrepareStart() {
	registerToastProtocol()
	go ensureToastShortcut()
	go cleanupStaleUpdateFiles()
}

// Logf appends a line to the app's own log (gui.log under
// %LOCALAPPDATA%\SmartThings PC Control), tagged with what wrote it.
func Logf(tag, format string, args ...any) { guiLog(tag, format, args...) }

// StartDebugMode follows tray.json's debug switch from the start, before
// the service answers (#133); StopDebugMode undoes it on a normal quit.
// FollowDebugMode tracks the saved config's switch afterwards.
func StartDebugMode(version string) { startDebugMode(version, readLocalConfig().Debug) }

// StopDebugMode ends debug mode at a normal quit.
func StopDebugMode() { stopDebugMode() }

// FollowDebugMode turns the crash record on or off with the config's
// debug switch.
func FollowDebugMode(on bool) { setDebugMode(on) }

// --- local session ---------------------------------------------------------

// LocalLoginRetryAfter holds back the automatic local login after a
// refusal: it will not change soon, and each attempt is a service log line.
const LocalLoginRetryAfter = localLoginRetryAfter

// IsLocalLoginRefused reports the service refusing to vouch for this
// process (a standard user): the presets and watch editors are locked.
func IsLocalLoginRefused(err error) bool { return errors.Is(err, errLocalLoginRefused) }

// IsLocalOnly reports the service's 403 local_only: the call needs the
// local trusted session (C5).
func IsLocalOnly(err error) bool { return errors.Is(err, errLocalOnly) }

// NeedsLocalSession reports whether err asks for a (new) local session: a
// 401, or a 403 local_only.
func NeedsLocalSession(err error) bool { return needsLocalSession(err) }

// --- updates ---------------------------------------------------------------

// UpdateCheck is the newest release on GitHub and what was learned with it.
type UpdateCheck = updateCheck

// Tag is the release's version tag ("v1.2.4").
func (c updateCheck) Tag() string { return c.rel.TagName }

// CheckLatestRelease asks GitHub for the newest release.
func CheckLatestRelease() (UpdateCheck, error) { return checkLatestRelease() }

// IsNewerRelease reports whether tag is newer than the running version;
// false for a dev build.
func IsNewerRelease(version, tag string) bool { return release.IsNewer(version, tag) }

// Preference keys of the update check, kept where the Fyne app keeps them.
const (
	PrefUpdateSkipped  = "update_skipped"  // a version the user chose to skip (#135)
	PrefUpdateNotified = "update_notified" // the version last notified about
)

// --- save model ------------------------------------------------------------

// WireConfig is what a save posts for sent: without the local session the
// presets and the watch list travel as null ("keep the stored ones").
func WireConfig(sent Config, locked bool) Config { return wireConfig(sent, locked) }

// SavedFallback is the baseline to adopt when the re-read after a save
// fails: what was sent, the bot token masked as the service would.
func SavedFallback(sent Config, oldMaskedToken string) Config {
	return savedFallback(sent, oldMaskedToken)
}

// ServiceNeedsRestart reports whether the service's save message says the
// change applies only after a restart (the port, browser access).
func ServiceNeedsRestart(serviceMsg string) bool { return serviceNeedsRestart(serviceMsg) }

// ShouldReloginAfterSave reports whether a save set a new, non-empty
// secret, which leaves the client without a valid session.
func ShouldReloginAfterSave(oldSecret, newSecret string) bool {
	return shouldReloginAfterSave(oldSecret, newSecret)
}

// SlotChangeQuestion is the question to ask before saving next over old
// when the save gives a preset or watch slot that SmartThings routines use
// a new owner (or empties it); "" when no slot changes hands.
func SlotChangeQuestion(l Lang, old, next Config) string {
	changes := configSlotChanges(old, next)
	if len(changes) == 0 {
		return ""
	}
	return slotChangeBody(l, changes)
}

// PresetWarningLines are the service's preset findings of a save, one
// localised paragraph each.
func PresetWarningLines(l Lang, ws []PresetWarning) []string { return presetWarningLines(l, ws) }
