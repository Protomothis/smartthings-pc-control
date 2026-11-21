package gui

// The PC facts a problem report carries (#175): Windows edition and build,
// the CPU, memory and uptime. Read from the registry and kernel32 through
// sysSource so the tests can hand in their own values.

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// SystemInfo is what ReadSystemInfo found; a field it could not read is
// left zero.
type SystemInfo struct {
	// Windows is "Windows 11 Pro 23H2 (build 22631.4460)".
	Windows string
	CPU     string
	Cores   int
	// MemTotal and MemFree are bytes of physical memory.
	MemTotal, MemFree uint64
	Uptime            time.Duration
	// PCName and UserName are %COMPUTERNAME% and %USERNAME%: never shown,
	// only masked out of the logs.
	PCName, UserName string
}

// sysSource is where ReadSystemInfo reads from; the tests replace it.
type sysSource struct {
	regString func(path, name string) (string, error) // HKLM
	regInt    func(path, name string) (uint64, error) // HKLM
	memory    func() (total, free uint64, err error)
	uptime    func() time.Duration
	cores     func() int
	env       func(string) string
}

const (
	regWinVersion = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	regCPU0       = `HARDWARE\DESCRIPTION\System\CentralProcessor\0`
)

// ReadSystemInfo reads this PC's facts. It takes a few milliseconds of
// registry reads; call it off the window goroutine all the same.
func ReadSystemInfo() SystemInfo { return readSystemInfo(realSysSource()) }

func readSystemInfo(src sysSource) SystemInfo {
	var info SystemInfo
	info.Windows = windowsName(src)
	if cpu, err := src.regString(regCPU0, "ProcessorNameString"); err == nil {
		// Some CPUs pad the name with spaces on either side.
		info.CPU = strings.Join(strings.Fields(cpu), " ")
	}
	info.Cores = src.cores()
	if total, free, err := src.memory(); err == nil {
		info.MemTotal, info.MemFree = total, free
	}
	info.Uptime = src.uptime()
	info.PCName, info.UserName = src.env("COMPUTERNAME"), src.env("USERNAME")
	return info
}

// windowsName is the edition, the feature update and the build. Windows 11
// still says "Windows 10" in ProductName; the build number tells them
// apart (22000 was the first Windows 11 build).
func windowsName(src sysSource) string {
	product, err := src.regString(regWinVersion, "ProductName")
	if err != nil || product == "" {
		product = "Windows"
	}
	build, _ := src.regString(regWinVersion, "CurrentBuildNumber")
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	if dv, err := src.regString(regWinVersion, "DisplayVersion"); err == nil && dv != "" {
		product += " " + dv
	}
	if build != "" {
		if ubr, err := src.regInt(regWinVersion, "UBR"); err == nil {
			build = fmt.Sprintf("%s.%d", build, ubr)
		}
		product += " (build " + build + ")"
	}
	return product
}

func realSysSource() sysSource {
	return sysSource{
		regString: func(path, name string) (string, error) {
			k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
			if err != nil {
				return "", err
			}
			defer k.Close()
			s, _, err := k.GetStringValue(name)
			return s, err
		},
		regInt: func(path, name string) (uint64, error) {
			k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
			if err != nil {
				return 0, err
			}
			defer k.Close()
			v, _, err := k.GetIntegerValue(name)
			return v, err
		},
		memory: globalMemory,
		uptime: windows.DurationSinceBoot,
		cores:  runtime.NumCPU,
		env:    os.Getenv,
	}
}

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// procGlobalMemoryStatusEx is kernel32's GlobalMemoryStatusEx, which
// x/sys/windows does not wrap.
var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// globalMemory is the physical memory in total and available now.
func globalMemory() (total, free uint64, err error) {
	var m memoryStatusEx
	m.length = uint32(unsafe.Sizeof(m))
	if r, _, e := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0, e
	}
	return m.totalPhys, m.availPhys, nil
}
