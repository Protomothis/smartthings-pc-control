package secureacl

// Private files (#131). The install DACL leaves every file readable by
// Users, which is right for the exe and the log but not for config.json:
// it holds the WebUI/API secret, so any local account could read it and
// drive the service. LockFile gives one file a protected DACL of its own —
// SYSTEM and Administrators only — and WritePrivateFile writes a file that
// way from the first byte. Apply leaves such a file alone (tighterThan), so
// the folder lockdown at every start does not undo it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateACEs is the DACL LockFile writes. No inheritance flags: it is set
// on files only. OWNER RIGHTS (OW) gets READ_CONTROL alone, which takes
// away the implicit WRITE_DAC a file's owner has otherwise — a config.json
// a standard user created before #126 is still owned by that user, who
// could grant themselves read access again.
//
//	0x1f01ff  FILE_ALL_ACCESS  SYSTEM, Administrators
//	0x020000  READ_CONTROL     OWNER RIGHTS
const privateACEs = "(A;;0x1f01ff;;;SY)(A;;0x1f01ff;;;BA)(A;;0x20000;;;OW)"

// ErrNotPrivate wraps a WritePrivateFile error that came after the data was
// written: the file is saved but its DACL could not be made private (a FAT
// drive, a reparse point). Callers log it and carry on, like the plaintext
// fallback for the bot token.
var ErrNotPrivate = errors.New("file not restricted to SYSTEM and Administrators")

// LockFile gives path the private DACL. It is idempotent: changed is false
// when path already had exactly that DACL. A missing file is fs.ErrNotExist;
// a reparse point (symlink) is refused, so the service never rewrites the
// permissions of whatever it points at.
func LockFile(path string) (changed bool, err error) {
	return lockFile(path, "")
}

// WritePrivateFile writes data to path the way os.WriteFile does, but the
// file is never readable by Users: an existing file is locked before it is
// truncated, and a new one is created with the private DACL. An error that
// wraps ErrNotPrivate means the data was written all the same.
func WritePrivateFile(path string, data []byte) error {
	return writePrivateFile(path, data, "")
}

// CreatePrivateFile creates a new file at path with the private DACL from
// the start and returns it open for writing: the service's crash records
// (#133), which may name paths and settings. An existing file is an error
// that matches fs.ErrExist.
func CreatePrivateFile(path string) (*os.File, error) {
	return createPrivateFile(path, "")
}

func createPrivateFile(path, extraACEs string) (*os.File, error) {
	sd, err := privateSD(extraACEs)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	// Shared like os.OpenFile's handles, so others may read it meanwhile.
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// privateSD builds the private security descriptor, with extra SDDL ACEs
// appended (the tests keep their own account able to delete the file).
func privateSD(extraACEs string) (*windows.SECURITY_DESCRIPTOR, error) {
	sd, err := windows.SecurityDescriptorFromString("D:P" + privateACEs + extraACEs)
	if err != nil {
		return nil, fmt.Errorf("build private DACL: %w", err)
	}
	return sd, nil
}

func lockFile(path, extraACEs string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || info.IsDir() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	sd, err := privateSD(extraACEs)
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("build private DACL: %w", err)
	}
	want, err := aceList(dacl)
	if err != nil {
		return false, err
	}
	if ok, err := rootMatches(path, want); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return false, fmt.Errorf("set private DACL on %s: %w", path, err)
	}
	return true, nil
}

func writePrivateFile(path string, data []byte, extraACEs string) error {
	// Lock what is there first, so the new content never sits in a file
	// Users can read. A file that cannot be locked is still written (the
	// error is reported afterwards); a missing one is created private.
	_, lockErr := lockFile(path, extraACEs)
	if errors.Is(lockErr, fs.ErrNotExist) {
		lockErr = nil
	}
	sd, err := privateSD(extraACEs)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	// CREATE_ALWAYS truncates an existing file and keeps its DACL (the
	// descriptor only applies to a file it creates).
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, sa,
		windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	if lockErr != nil {
		return fmt.Errorf("%w: %w", ErrNotPrivate, lockErr)
	}
	return nil
}

// tighterThan reports whether path is a file with a protected DACL that
// grants nobody more than want (the folder's own ACEs) does: allow ACEs
// only, each for a trustee in want and within that trustee's rights. A
// private file passes; one that adds a trustee, widens a right or carries
// a deny ACE (which could lock the service out) does not.
func tighterThan(path string, want []ace) (bool, error) {
	acl, protected, err := readDACL(path)
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
	for _, h := range have {
		if h.typ != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false, nil
		}
		ok := false
		for _, w := range want {
			if w.typ == windows.ACCESS_ALLOWED_ACE_TYPE && w.sid == h.sid && h.mask&^w.mask == 0 {
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
