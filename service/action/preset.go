package action

// Preset failures for the clients (review C6). A preset's path says where
// the user keeps things — a folder name, an account name, a project — and
// its URL may carry a token. Telegram and SmartThings answers, and the
// WebUI that may be open to the LAN, therefore get a fixed text plus the
// file's base name ("file not found: run.ps1"); the full error, path and
// all, stays in service.log, where the service's preset runner writes it.

import (
	"errors"
	"net/http"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// PresetFailure is Classify for a preset run: the same status and wire
// code, with a message that never carries p's path, folder, arguments or
// URL. Reason is the user session's reason (useraction.Preset*), "" for
// the failures that are not about the file (no session, timeout, …).
func PresetFailure(err error, p config.Preset) (f Failure, reason string) {
	f = Classify(err)
	var ue *session.ActionError
	if !errors.As(err, &ue) {
		// No session, a timeout, a start of the child that failed: their
		// messages are fixed texts already, and the last one is the
		// generic "could not be run" (Classify keeps details out).
		return f, ""
	}
	name := useraction.PresetFileName(p.Path)
	reason = ue.Reason
	switch {
	case p.Type == "url":
		reason = useraction.PresetURLFailed
		f.Message = "could not open the URL"
	case reason == useraction.PresetNotFound:
		f.Message = "file not found: " + name
	case reason == useraction.PresetAccessDenied:
		f.Message = "access denied: " + name
	case ue.Code == useraction.CodeBadArgs:
		reason = ""
		f.Message = "the preset cannot be run as saved: " + name
	case ue.Code == useraction.CodeUnsupported:
		reason = ""
		f.Message = "presets are not available on this PC"
	default:
		reason = useraction.PresetStartFailed
		f.Message = "could not start: " + name
	}
	f.Detail = f.Message
	return f, reason
}

// WritePresetError is WriteError for a preset run, with PresetFailure's
// message: {"error": code, "message": …}. Presets are not rate limited,
// so there is no Retry-After.
func WritePresetError(w http.ResponseWriter, err error, p config.Preset) {
	f, _ := PresetFailure(err, p)
	httpx.WriteJSON(w, f.Status, map[string]string{"error": f.Code, "message": f.Message})
}
