package secureacl

// Writable by others (review C6): a preset runs its program or script as
// the logged-in user whenever SmartThings or Telegram asks. If that file,
// or the folder it sits in, can be rewritten by some other account, that
// account decides what the remote button runs. WritableByOthers says so,
// in the style of NonAdminWritable but with an allow-list instead of a
// deny-list: any principal not on it that may write counts.

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Principals that may always write to a preset's file: they can replace
// anything on the PC anyway.
const (
	sidSystem           = "S-1-5-18"
	sidAdministrators   = "S-1-5-32-544"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	sidCreatorOwner     = "S-1-3-0" // only means something inherit-only; harmless on the object itself
	sidOwnerRights      = "S-1-3-4" // the owner, who is allowed anyway
	// sidEveryone stands for "anybody" when a file has no DACL at all.
	sidEveryone = "S-1-1-0"
)

// WritableByOthers reports a principal (as a SID string) that may write to
// path although it is none of SYSTEM, Administrators, TrustedInstaller,
// path's owner or the extra SIDs given (the user the preset runs as); ""
// when there is none. "Write" is writeRights — write or append data,
// delete, delete child, change the permissions or the owner — on an allow
// ACE that applies to path itself (inherit-only ACEs are skipped). A NULL
// or missing DACL lets everybody in and reports Everyone. Deny ACEs are
// not weighed, so the answer errs on the side of "writable".
func WritableByOthers(path string, allowed ...string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read security of %s: %w", path, err)
	}
	ok := map[string]bool{
		sidSystem: true, sidAdministrators: true, sidTrustedInstaller: true,
		sidCreatorOwner: true, sidOwnerRights: true,
	}
	for _, sid := range allowed {
		if sid != "" {
			ok[sid] = true
		}
	}
	if sd == nil {
		return sidEveryone, nil
	}
	if owner, _, err := sd.Owner(); err == nil && owner != nil {
		ok[owner.String()] = true
	}
	acl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && acl == nil) {
		return sidEveryone, nil
	}
	if err != nil {
		return "", fmt.Errorf("read DACL of %s: %w", path, err)
	}
	aces, err := aceList(acl)
	if err != nil {
		return "", err
	}
	return othersWithWrite(aces, ok), nil
}

// othersWithWrite is the decision WritableByOthers makes on aces: the
// first principal outside ok that an allow ACE on the object grants a
// write right.
func othersWithWrite(aces []ace, ok map[string]bool) string {
	for _, a := range aces {
		if a.typ != windows.ACCESS_ALLOWED_ACE_TYPE || a.flags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if !ok[a.sid] && a.mask&writeRights != 0 {
			return a.sid
		}
	}
	return ""
}
