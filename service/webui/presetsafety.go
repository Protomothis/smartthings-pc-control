package webui

// Preset safety on the /api side (review C5, C6).
//
//   - GET /api/config without the local trusted session masks what a
//     preset runs: path "" and no args. A secret login (the browser, the
//     app's login dialog) still sees slots, names and types.
//   - A save that changes the presets or the watch list needs the local
//     trusted session (serveConfig).
//   - A save or a [테스트] of a program/script preset whose file, or the
//     folder it is in, may be rewritten by someone other than SYSTEM,
//     Administrators, TrustedInstaller, the file's owner or the user it
//     runs as is still allowed, but answers with a writable_by_others
//     warning: whoever can rewrite that file decides what the remote
//     button runs.

import (
	"slices"
	"strings"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/secureacl"
)

// PresetWarning is one entry of a save's or a test's "warnings".
type PresetWarning struct {
	Slot int    `json:"slot"`
	Code string `json:"code"` // writable_by_others
	Path string `json:"path"` // the file or folder that is writable
}

// maskPresets is the presets as a session that is not the local trusted
// one sees them: what runs is left out. It never aliases ps.
func maskPresets(ps []config.Preset) []config.Preset {
	out := make([]config.Preset, 0, len(ps))
	for _, p := range ps {
		p.Path, p.Args = "", nil
		out = append(out, p)
	}
	return out
}

// localOnlyChange reports whether a save from old to new touches what only
// the local trusted session may change: the presets or the watch list.
// Both are compared in their normalised forms, so a body that leaves them
// out (the WebUI page) or repeats them unchanged is not a change.
func localOnlyChange(old, new config.Config) bool {
	if len(config.ChangedPresetSlots(old.Presets, new.Presets)) > 0 {
		return true
	}
	return !slices.Equal(old.Activity.WithDefaults().Watch, new.Activity.WithDefaults().Watch)
}

// targetUserSID is the SID of the user the presets run as (the target
// session's), "" when nobody is logged in.
func (s *Server) targetUserSID() string {
	id, err := s.d.Heartbeat.Target()
	if err != nil {
		return ""
	}
	tok, err := s.d.UserToken(id)
	if err != nil {
		return ""
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}

// presetWarnings checks the program and script presets in ps for a file or
// parent folder that others may write to. URL presets have no file; a UNC
// path is not looked at (the share's own rules apply, and a slow server
// must not hold up a save); a file that cannot be read (missing, no
// access) says nothing here — running it will.
func (s *Server) presetWarnings(ps []config.Preset) []PresetWarning {
	var out []PresetWarning
	user, looked := "", false
	for _, p := range ps {
		if (p.Type != "program" && p.Type != "script") || strings.HasPrefix(p.Path, `\\`) {
			continue
		}
		if !looked {
			user, looked = s.presetUserSID(), true
		}
		for _, path := range []string{p.Path, parentDir(p.Path)} {
			if path == "" {
				continue
			}
			who, err := secureacl.WritableByOthers(path, user)
			if err != nil || who == "" {
				continue
			}
			logx.Printf("WARNING: preset %d (%s): %s is writable by %s", p.Slot, p.Name, path, who)
			out = append(out, PresetWarning{Slot: p.Slot, Code: "writable_by_others", Path: path})
			break
		}
	}
	return out
}

// parentDir is the folder of a Windows path ("C:\" for a file in a drive
// root), "" when there is none.
func parentDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i <= 0 {
		return ""
	}
	d := p[:i]
	if len(d) == 2 && d[1] == ':' {
		d += `\`
	}
	return d
}
