// Package appid is the one place the app's toast identity lives: the
// AppUserModelID every toast is shown under, and the Start menu shortcut
// that makes Windows accept it.
//
// An unpackaged desktop app gets toast banners only when its AUMID is set
// on a shortcut in the Start menu (Microsoft's documented requirement).
// Measured on Windows 11: toasts from CreateToastNotifier("SmartThings PC
// Control") were accepted and landed in the notification center but never
// popped up, with or without an HKCU\Software\Classes\AppUserModelId entry;
// the same toast under an AUMID carried by a Start menu shortcut showed
// its banner at once, titled with the shortcut's name and the exe's icon.
//
// Both toast paths use AUMID: the tray app's grace-period toasts
// (gui/toast.go, through useraction.ShowToast) and the PC notification
// (useraction/notify.go).
// Both also call EnsureShortcut first, since either may be the first thing
// of this app a user session runs.
package appid

// AUMID is the AppUserModelID of every toast this app shows. It must
// match the System.AppUserModel.ID of the Start menu shortcut.
const AUMID = "Protomothis.SmartThingsPCControl"

// DisplayName is the shortcut's name, which is also the title Windows
// puts on the toasts.
const DisplayName = "SmartThings PC Control"

// ShortcutFile is the shortcut's file name in the Start menu Programs
// folder.
const ShortcutFile = DisplayName + ".lnk"

// shortcutArgs are the arguments the shortcut starts the exe with: the
// desktop app, window shown (a user clicking it in the Start menu).
const shortcutArgs = "gui"
