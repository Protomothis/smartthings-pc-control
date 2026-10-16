package privacy

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// OpenUserHive opens HKU\<sid> for reading. It fails when the user's hive
// is not loaded (nobody of that SID logged in), which the Monitor reports
// as unknown. Nothing here ever writes to the registry.
func OpenUserHive(sid string) (Hive, error) {
	k, err := registry.OpenKey(registry.USERS, sid, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	return regHive{root: k}, nil
}

// regHive is a loaded user hive.
type regHive struct{ root registry.Key }

func notFound(err error) error {
	if errors.Is(err, registry.ErrNotExist) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		return ErrNotFound
	}
	return err
}

func (h regHive) SubKeys(path string) ([]string, error) {
	k, err := registry.OpenKey(h.root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return nil, notFound(err)
	}
	defer k.Close()
	return k.ReadSubKeyNames(-1)
}

func (h regHive) Times(path string) (start, stop uint64, err error) {
	k, err := registry.OpenKey(h.root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, 0, notFound(err)
	}
	defer k.Close()
	start, err = qword(k, "LastUsedTimeStart")
	if err != nil {
		return 0, 0, err
	}
	stop, err = qword(k, "LastUsedTimeStop")
	return start, stop, err
}

// qword reads a QWORD value; a missing one is 0.
func qword(k registry.Key, name string) (uint64, error) {
	v, _, err := k.GetIntegerValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, nil
	}
	return v, err
}

func (h regHive) Close() error { return h.root.Close() }
