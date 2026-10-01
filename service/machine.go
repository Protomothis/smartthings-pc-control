package service

// What identifies this PC to the hub and the app: the machine id, the
// hostname and the newest release this service knows about.

import (
	"os"
	"sync"

	"github.com/Protomothis/smartthings-pc-control/internal/release"

	"golang.org/x/sys/windows/registry"
)

// machineID is HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid, the stable
// per-install identifier the driver keys its device on. It never changes
// while Windows is installed, so it is read once.
var (
	machineIDOnce  sync.Once
	machineIDValue string
)

func machineID() string {
	machineIDOnce.Do(func() {
		guid, err := readMachineGUID()
		if err != nil || guid == "" {
			logMsg("ST API: MachineGuid unreadable (%v); falling back to the hostname", err)
			machineIDValue = hostname()
			return
		}
		machineIDValue = guid
	})
	return machineIDValue
}

// readMachineGUID reads the Cryptography\MachineGuid value. WOW64_64KEY
// keeps a 32-bit build looking at the same key as a 64-bit one.
func readMachineGUID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	return guid, nil
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "PC"
}

// stUpdateInfo reports the newest release this service knows about. The
// periodic checker caches every lookup (#68), so once it has run "latest"
// is the real newest tag even when it is not newer than us; before the
// first check the only thing on record is the tag state.json says was
// announced with system.update_available (#60).
func stUpdateInfo() stUpdate {
	if tag := latestReleaseTag(); tag != "" {
		return stUpdate{Available: release.IsNewer(Version, tag), Latest: tag}
	}
	stateMu.Lock()
	st := loadState(statePath())
	stateMu.Unlock()
	if tag := st.LastNotifiedTag; tag != "" && release.IsNewer(Version, tag) {
		return stUpdate{Available: true, Latest: tag}
	}
	return stUpdate{Available: false, Latest: ""}
}
