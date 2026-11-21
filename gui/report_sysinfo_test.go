package gui

import (
	"errors"
	"testing"
	"time"
)

// fakeSys answers from maps; a missing value is an error, as in the
// registry.
func fakeSys(strs map[string]string, ints map[string]uint64) sysSource {
	return sysSource{
		regString: func(path, name string) (string, error) {
			if v, ok := strs[path+`\`+name]; ok {
				return v, nil
			}
			return "", errors.New("not found")
		},
		regInt: func(path, name string) (uint64, error) {
			if v, ok := ints[path+`\`+name]; ok {
				return v, nil
			}
			return 0, errors.New("not found")
		},
		memory: func() (uint64, uint64, error) { return 16 << 30, 4 << 30, nil },
		uptime: func() time.Duration { return 3 * time.Hour },
		cores:  func() int { return 8 },
		env:    func(k string) string { return map[string]string{"COMPUTERNAME": "DESK-1", "USERNAME": "jdoe"}[k] },
	}
}

func TestReadSystemInfo(t *testing.T) {
	v := regWinVersion + `\`
	src := fakeSys(map[string]string{
		v + "ProductName":                "Windows 10 Pro",
		v + "CurrentBuildNumber":         "22631",
		v + "DisplayVersion":             "23H2",
		regCPU0 + `\ProcessorNameString`: "  Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz   ",
	}, map[string]uint64{v + "UBR": 4460})
	info := readSystemInfo(src)
	if info.Windows != "Windows 11 Pro 23H2 (build 22631.4460)" {
		t.Errorf("windows %q", info.Windows)
	}
	if info.CPU != "Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz" || info.Cores != 8 {
		t.Errorf("cpu %q %d", info.CPU, info.Cores)
	}
	if info.MemTotal != 16<<30 || info.MemFree != 4<<30 || info.Uptime != 3*time.Hour {
		t.Errorf("mem %d/%d up %v", info.MemTotal, info.MemFree, info.Uptime)
	}
	if info.PCName != "DESK-1" || info.UserName != "jdoe" {
		t.Errorf("names %q %q", info.PCName, info.UserName)
	}
}

func TestWindowsName(t *testing.T) {
	v := regWinVersion + `\`
	for _, c := range []struct {
		strs map[string]string
		ubr  bool
		want string
	}{
		{map[string]string{v + "ProductName": "Windows 10 Home", v + "CurrentBuildNumber": "19045", v + "DisplayVersion": "22H2"}, true, "Windows 10 Home 22H2 (build 19045.99)"},
		{map[string]string{v + "ProductName": "Windows 10 Pro", v + "CurrentBuildNumber": "22000"}, false, "Windows 11 Pro (build 22000)"},
		{map[string]string{}, false, "Windows"},
	} {
		ints := map[string]uint64{}
		if c.ubr {
			ints[v+"UBR"] = 99
		}
		if got := windowsName(fakeSys(c.strs, ints)); got != c.want {
			t.Errorf("%v: %q, want %q", c.strs, got, c.want)
		}
	}
}

// The real source reads this PC: no values asserted, only that it answers.
func TestReadSystemInfoReal(t *testing.T) {
	info := ReadSystemInfo()
	if info.Windows == "" || info.Cores < 1 || info.MemTotal == 0 || info.Uptime <= 0 {
		t.Errorf("%+v", info)
	}
}
