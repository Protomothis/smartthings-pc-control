package webui

// Is the peer an administrator (#131, #168)? The local trusted session is
// for the people who may read config.json anyway — SYSTEM and
// BUILTIN\Administrators (C5) — so the question is whether the tray's
// user is a member of BUILTIN\Administrators, not whether this one
// process runs elevated. A tray is never elevated: it starts from the
// HKCU Run key or from the service, with the user's ordinary token.
//
// Where the membership shows depends on how Windows signed the user in:
//
//   - elevated, or UAC off: Administrators is in the token, enabled;
//   - UAC's Admin Approval Mode (the default): the filtered token lists it
//     as deny-only, and the linked token (TokenLinkedToken) has it enabled;
//   - Windows 11's Administrator protection, and anything else that hands
//     an administrator a fully standard token: the token has no trace of it
//     (elevation runs as a separate, system-managed account), but the
//     user's SID is still a member of the local Administrators group.
//
// Before #168 only the first two were looked at, so an administrator of
// the third kind — which nothing in Settings tells apart from the others;
// it says "관리자" — was refused as a standard user. Now the checks run in
// that order, cheapest first, and the last asks the local group itself
// (NetLocalGroupGetMembers), by SID, so the account's name ("MicrosoftAccount\…",
// "AzureAD\…") and the group's localized name do not matter. A group SID
// that is itself a member of Administrators counts when the token holds it
// (a domain or Entra ID group that makes its users administrators).
//
// A standard user fails all of them: not in the token, no elevated linked
// token, and neither their SID nor any of their groups in Administrators.

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// TOKEN_ELEVATION_TYPE values (TokenElevationType).
const (
	tokenElevationTypeDefault = 1 // no split token: UAC off, a standard user, or the built-in Administrator
	tokenElevationTypeFull    = 2 // the elevated half of a split token
	tokenElevationTypeLimited = 3 // the filtered half of a split token
)

// administratorsSID is BUILTIN\Administrators.
const administratorsSID = "S-1-5-32-544"

// tokenGroup is one SID_AND_ATTRIBUTES of a token, with the SID as a string.
type tokenGroup struct {
	SID  string
	Attr uint32
}

// tokenReader is what the admin check reads of a process token; the real
// one wraps a windows.Token (peer.go), the tests fake it.
type tokenReader interface {
	// groups are the token's groups (TokenGroups).
	groups() ([]tokenGroup, error)
	// elevationType is TokenElevationType.
	elevationType() (uint32, error)
	// linkedGroups are the groups of the linked token (TokenLinkedToken);
	// only asked of a limited token.
	linkedGroups() ([]tokenGroup, error)
}

// adminVerdict is the admin check's answer: Via names the check that found
// the membership ("" for none), Detail is for the log.
type adminVerdict struct {
	Admin  bool
	Via    string
	Detail string
}

// Via values.
const (
	adminViaEnabled  = "token"        // enabled in the token: elevated, or UAC off
	adminViaDenyOnly = "filtered"     // deny-only in the token: UAC's filtered token
	adminViaLinked   = "linked"       // enabled in the linked token
	adminViaMember   = "member"       // the user's SID is in the local group
	adminViaNested   = "nested_group" // one of the token's groups is in the local group
)

// checkAdmin decides whether the token's user is an administrator.
// members lists the SIDs in the local Administrators group; it is asked
// only when the token itself does not tell.
func checkAdmin(tr tokenReader, userSID string, members func() ([]string, error)) adminVerdict {
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	groups, err := tr.groups()
	if err != nil {
		note("token groups: %v", err)
	}
	for _, g := range groups {
		if g.SID != administratorsSID {
			continue
		}
		switch {
		case g.Attr&windows.SE_GROUP_ENABLED != 0:
			return adminVerdict{Admin: true, Via: adminViaEnabled}
		case g.Attr&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0:
			return adminVerdict{Admin: true, Via: adminViaDenyOnly}
		}
	}

	elev, err := tr.elevationType()
	switch {
	case err != nil:
		note("elevation type: %v", err)
	default:
		note("elevation %s", elevationName(elev))
	}
	if err == nil && elev == tokenElevationTypeLimited {
		linked, err := tr.linkedGroups()
		if err != nil {
			note("linked token: %v", err)
		}
		for _, g := range linked {
			if g.SID == administratorsSID && g.Attr&windows.SE_GROUP_ENABLED != 0 {
				return adminVerdict{Admin: true, Via: adminViaLinked, Detail: strings.Join(notes, ", ")}
			}
		}
	}

	m, err := members()
	if err != nil {
		note("Administrators members: %v", err)
		return adminVerdict{Detail: strings.Join(notes, ", ")}
	}
	inToken := make(map[string]bool, len(groups))
	for _, g := range groups {
		// Enabled or deny-only: either way the user belongs to it. A
		// logon SID or a capability is never a member of Administrators.
		if g.Attr&(windows.SE_GROUP_ENABLED|windows.SE_GROUP_USE_FOR_DENY_ONLY) != 0 {
			inToken[g.SID] = true
		}
	}
	for _, sid := range m {
		if userSID != "" && strings.EqualFold(sid, userSID) {
			return adminVerdict{Admin: true, Via: adminViaMember, Detail: strings.Join(notes, ", ")}
		}
	}
	for _, sid := range m {
		if inToken[sid] {
			return adminVerdict{Admin: true, Via: adminViaNested, Detail: strings.Join(notes, ", ") + ", via " + sid}
		}
	}
	note("not among %d Administrators members", len(m))
	return adminVerdict{Detail: strings.Join(notes, ", ")}
}

// elevationName is a TOKEN_ELEVATION_TYPE for the log.
func elevationName(t uint32) string {
	switch t {
	case tokenElevationTypeDefault:
		return "default"
	case tokenElevationTypeFull:
		return "full"
	case tokenElevationTypeLimited:
		return "limited"
	}
	return fmt.Sprintf("type %d", t)
}
