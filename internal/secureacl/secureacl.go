// Package secureacl locks the install folder down (#126).
//
// The service runs as SYSTEM straight from the folder the exe was put in,
// and reads config.json and state.json from the same place. A folder made
// directly under C:\ (C:\PC Control) inherits "Authenticated Users: Modify"
// from the drive root, so any logged-in standard user could replace the exe
// or edit the config and get code running as SYSTEM. Apply gives the folder
// a protected DACL of its own — SYSTEM and Administrators full control,
// Users read & execute — so nothing from C:\ leaks in, and resets every
// file and folder below it to inherit only that. Files made private on
// purpose (config.json, state.json: private.go, #131) are tighter than the
// folder and left as they are.
//
// LockInstallDir is what the elevated installer and the service call: it
// first refuses folders that are not the app's own (a drive root, Windows,
// a user profile, a shared Program Files root, a large tree), because
// tightening those would lock users out of their own files.
package secureacl

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// installACEs is the DACL Apply writes, as SDDL ACEs. Every ACE is
// container- and object-inherit, so files the service or the updater
// create later get the same rights. Masks are spelled out (not generic
// rights) so the ACL reads back exactly as written:
//
//	0x1f01ff  FILE_ALL_ACCESS                       SYSTEM, Administrators
//	0x1200a9  FILE_GENERIC_READ|FILE_GENERIC_EXECUTE  Users
//
// OWNER RIGHTS (OW) gets read & execute too: without it the owner of a file
// keeps an implicit WRITE_DAC and could grant itself write access again.
// That matters here because a folder copied in by a standard user (or one
// of its files) is owned by that user.
const installACEs = "(A;OICI;0x1f01ff;;;SY)(A;OICI;0x1f01ff;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;0x1200a9;;;OW)"

// MaxEntries bounds the folder LockInstallDir accepts. The app's own folder
// holds a handful of files (exe, config.json, state.json, service.log.*);
// a tree this large is somebody's tools or source folder, not ours.
const MaxEntries = 500

// ErrNotDedicated wraps every reason LockInstallDir refuses a folder.
var ErrNotDedicated = errors.New("not a dedicated install folder")

// Apply gives dir the protected install DACL and resets everything below
// it to inherit from dir only. It is idempotent: changed is false when dir
// already had exactly that DACL and no descendant carried ACEs of its own.
// Reparse points (symlinks, junctions) below dir are neither followed nor
// modified. Apply does no safety checks of its own; see LockInstallDir.
func Apply(dir string) (changed bool, err error) {
	return apply(dir, "")
}

// LockInstallDir checks that dir is a folder only this app uses (see
// Lockable) and then Applies the install DACL to it.
func LockInstallDir(dir string) (changed bool, err error) {
	if err := Lockable(dir); err != nil {
		return false, err
	}
	return Apply(dir)
}

// apply is Apply with extra SDDL ACEs appended to the DACL. Tests use it to
// keep their own account able to delete the temp folder afterwards.
func apply(dir, extraACEs string) (bool, error) {
	desired, err := windows.SecurityDescriptorFromString("D:P" + installACEs + extraACEs)
	if err != nil {
		return false, fmt.Errorf("build DACL: %w", err)
	}
	dacl, _, err := desired.DACL()
	if err != nil {
		return false, fmt.Errorf("build DACL: %w", err)
	}
	want, err := aceList(dacl)
	if err != nil {
		return false, err
	}

	changed := false
	ok, err := rootMatches(dir, want)
	if err != nil {
		return false, err
	}
	if !ok {
		// SetNamedSecurityInfo also pushes the new inheritable ACEs down
		// to every child that only inherits; the walk below handles the
		// ones that carry ACEs of their own.
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil, nil, dacl, nil); err != nil {
			return false, fmt.Errorf("set DACL on %s: %w", dir, err)
		}
		changed = true
	}

	// "D:" is an empty, present DACL: set together with
	// UNPROTECTED_DACL_SECURITY_INFORMATION it leaves only what the parent
	// passes down (what `icacls /reset` does).
	emptySD, err := windows.SecurityDescriptorFromString("D:")
	if err != nil {
		return changed, err
	}
	empty, _, err := emptySD.DACL()
	if err != nil {
		return changed, err
	}

	var errs []error
	// WalkDir is pre-order, so a folder is reset before anything in it is
	// looked at.
	werr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			if d != nil && d.IsDir() && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		if path == dir {
			return nil
		}
		if isReparse(d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		clean, err := inheritsOnly(path, want)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if clean {
			return nil
		}
		// A private file (LockFile, #131) is tighter than the folder on
		// purpose; resetting it would hand config.json back to Users.
		if !d.IsDir() {
			if tight, err := tighterThan(path, want); err == nil && tight {
				return nil
			}
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			nil, nil, empty, nil); err != nil {
			errs = append(errs, fmt.Errorf("reset DACL on %s: %w", path, err))
			return nil
		}
		changed = true
		return nil
	})
	if werr != nil {
		errs = append(errs, werr)
	}
	return changed, errors.Join(errs...)
}

// ace is one access-control entry, reduced to what Apply compares.
type ace struct {
	typ   uint8
	flags uint8
	mask  uint32
	sid   string
}

// aceList reads every ACE of acl. A nil acl (NULL DACL: everyone has full
// access) has no entries.
func aceList(acl *windows.ACL) ([]ace, error) {
	if acl == nil {
		return nil, nil
	}
	out := make([]ace, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &a); err != nil {
			return nil, fmt.Errorf("read ACE %d: %w", i, err)
		}
		// Allowed and denied ACEs share this layout; other ACE types (object,
		// callback) are never written here and only need to compare unequal.
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		out = append(out, ace{typ: a.Header.AceType, flags: a.Header.AceFlags, mask: uint32(a.Mask), sid: sid.String()})
	}
	return out, nil
}

// readDACL returns the DACL of path and whether it is protected. A missing
// DACL is reported as nil, like a NULL DACL.
func readDACL(path string) (acl *windows.ACL, protected bool, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, false, fmt.Errorf("read DACL of %s: %w", path, err)
	}
	if sd == nil {
		return nil, false, nil
	}
	control, _, err := sd.Control()
	if err != nil {
		return nil, false, err
	}
	acl, _, err = sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return acl, control&windows.SE_DACL_PROTECTED != 0, nil
}

// rootMatches reports whether dir is protected and holds exactly want
// (in any order).
func rootMatches(dir string, want []ace) (bool, error) {
	acl, protected, err := readDACL(dir)
	if err != nil {
		return false, err
	}
	if !protected || acl == nil {
		return false, nil
	}
	have, err := aceList(acl)
	if err != nil {
		return false, err
	}
	if len(have) != len(want) {
		return false, nil
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// inheritsOnly reports whether path is unprotected and every ACE on it is
// inherited and grants what one of the folder's own ACEs grants. That
// catches explicit ACEs, protected children, and files moved in from
// elsewhere that still carry their old inherited ACEs.
func inheritsOnly(path string, want []ace) (bool, error) {
	acl, protected, err := readDACL(path)
	if err != nil {
		return false, err
	}
	if protected || acl == nil {
		return false, nil
	}
	have, err := aceList(acl)
	if err != nil {
		return false, err
	}
	for _, h := range have {
		if h.flags&windows.INHERITED_ACE == 0 {
			return false, nil
		}
		ok := false
		for _, w := range want {
			if h.typ == w.typ && h.mask == w.mask && h.sid == w.sid {
				ok = true
				break
			}
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// isReparse reports whether d is a symlink, junction or other reparse point.
func isReparse(d fs.DirEntry) bool {
	if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	info, err := d.Info()
	if err != nil {
		return false
	}
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// Lockable returns nil when dir looks like a folder only this app uses, so
// that tightening it cannot lock anyone out of their own files. The error
// wraps ErrNotDedicated and says why.
func Lockable(dir string) error {
	return lockable(dir, systemFolders())
}

// lockable is Lockable against the given well-known folders.
func lockable(dir string, f folders) error {
	if reason := pathReason(dir, f); reason != "" {
		return fmt.Errorf("%w: %s (%s)", ErrNotDedicated, dir, reason)
	}
	n := 0
	werr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if isReparse(d) {
			return fmt.Errorf("%w: %s (contains a link or junction: %s)", ErrNotDedicated, dir, path)
		}
		if n++; n > MaxEntries {
			return fmt.Errorf("%w: %s (more than %d files and folders)", ErrNotDedicated, dir, MaxEntries)
		}
		return nil
	})
	return werr
}

// folders are the well-known locations pathReason refuses.
type folders struct {
	exact []string // refused as such; subfolders are fine (Program Files\App)
	trees []string // refused together with everything below them
}

// systemFolders resolves the shared locations of this machine. Folders that
// cannot be resolved are left out.
func systemFolders() folders {
	known := func(id *windows.KNOWNFOLDERID) string {
		p, err := windows.KnownFolderPath(id, 0)
		if err != nil {
			return ""
		}
		return p
	}
	return folders{
		exact: []string{
			known(windows.FOLDERID_ProgramFiles),
			known(windows.FOLDERID_ProgramFilesX86),
			known(windows.FOLDERID_ProgramFilesX64),
			known(windows.FOLDERID_ProgramFilesCommon),
			known(windows.FOLDERID_ProgramFilesCommonX86),
			known(windows.FOLDERID_ProgramData),
		},
		trees: []string{
			known(windows.FOLDERID_Windows),      // also covers the SYSTEM profile and C:\Windows\Temp
			known(windows.FOLDERID_UserProfiles), // every user's Downloads, Desktop, AppData\Local\Temp …
		},
	}
}

// pathReason is the pure part of Lockable: why dir must not be tightened,
// or "" when it may. Comparison is case-insensitive on cleaned paths.
func pathReason(dir string, f folders) string {
	if !filepath.IsAbs(dir) {
		return "not an absolute path"
	}
	dir = filepath.Clean(dir)
	vol := filepath.VolumeName(dir)
	if vol == "" || len(dir) <= len(vol)+1 {
		return "drive or share root"
	}
	for _, t := range f.trees {
		if t != "" && within(dir, filepath.Clean(t)) {
			return "system or user-profile folder"
		}
	}
	for _, e := range f.exact {
		if e != "" && strings.EqualFold(dir, filepath.Clean(e)) {
			return "shared system folder"
		}
	}
	return ""
}

// within reports whether path equals dir or lies beneath it, ignoring case.
func within(path, dir string) bool {
	if strings.EqualFold(path, dir) {
		return true
	}
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

// Rights that let a holder change what the SYSTEM service runs or reads:
// create or overwrite files, delete them, or rewrite their permissions or
// owner. Attribute and extended-attribute writes are left out.
const writeRights = 0x0002 | // FILE_WRITE_DATA / FILE_ADD_FILE
	0x0004 | // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
	0x0040 | // FILE_DELETE_CHILD
	windows.DELETE |
	windows.WRITE_DAC |
	windows.WRITE_OWNER |
	windows.GENERIC_WRITE |
	windows.GENERIC_ALL

// nonAdminSIDs are the groups every ordinary logged-in user belongs to.
var nonAdminSIDs = map[string]bool{
	"S-1-5-32-545": true, // BUILTIN\Users
	"S-1-5-11":     true, // NT AUTHORITY\Authenticated Users
	"S-1-1-0":      true, // Everyone
	"S-1-5-4":      true, // NT AUTHORITY\INTERACTIVE
}

// NonAdminWritable reports whether the DACL of path lets every ordinary
// user write to it: an allow ACE for Users, Authenticated Users, Everyone or
// INTERACTIVE that applies to path itself (not inherit-only) and carries a
// write, delete or permission-change right. A missing or NULL DACL (FAT
// drives, for one) counts as writable. Deny ACEs are not weighed, so the
// answer errs on the side of "writable".
func NonAdminWritable(path string) (bool, error) {
	acl, _, err := readDACL(path)
	if err != nil {
		return false, err
	}
	if acl == nil {
		return true, nil
	}
	aces, err := aceList(acl)
	if err != nil {
		return false, err
	}
	return grantsNonAdminWrite(aces), nil
}

// grantsNonAdminWrite is the decision NonAdminWritable makes on aces.
func grantsNonAdminWrite(aces []ace) bool {
	for _, a := range aces {
		if a.typ != windows.ACCESS_ALLOWED_ACE_TYPE || a.flags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if nonAdminSIDs[a.sid] && a.mask&writeRights != 0 {
			return true
		}
	}
	return false
}
