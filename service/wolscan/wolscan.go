// Package wolscan reads this PC's network adapters and their Wake-on-LAN
// state (Go net for the adapters, PowerShell for WoL only), and the public
// IP power.started reports. The /st/v1 wol block (service/stapi) and GET
// /api/wol-status (service/webui) are built from Scan.
package wolscan

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/internal/systool"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// Scan queries all network adapters for WoL capability using Go net + PowerShell for WoL only
func Scan() status.WoLStatus {
	result := status.WoLStatus{}

	// Get network interfaces using Go standard library (no encoding issues)
	ifaces, err := net.Interfaces()
	if err != nil {
		return status.WoLStatus{Error: fmt.Sprintf("failed to list interfaces: %v", err)}
	}

	// Filter to real adapters (has MAC, not loopback, not point-to-point)
	type ifaceInfo struct {
		Name string
		MAC  string
		IPs  []string
		Up   bool
	}
	var realAdapters []ifaceInfo

	for _, iface := range ifaces {
		// Skip loopback, no-MAC (virtual), and point-to-point interfaces
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac == "" {
			continue
		}

		info := ifaceInfo{
			Name: iface.Name,
			MAC:  formatMAC(mac),
			Up:   iface.Flags&net.FlagUp != 0,
		}

		// Get IP addresses
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, _ := net.ParseCIDR(addr.String())
			if ip != nil && !ip.IsLinkLocalUnicast() {
				info.IPs = append(info.IPs, ip.String())
			}
		}

		// Only include adapters that have IPs or are up (skip internal virtual ones)
		if len(info.IPs) > 0 || info.Up {
			realAdapters = append(realAdapters, info)
		}
	}

	if len(realAdapters) == 0 {
		return status.WoLStatus{Error: "no network adapters found"}
	}

	// Get WoL status via PowerShell (by MAC matching)
	// WakeOnMagicPacket: 0=Unsupported, 1=Disabled, 2=Enabled
	//
	// Per adapter, not one pipeline: Get-NetAdapterPowerManagement throws
	// "A device attached to the system is not functioning" on some Realtek
	// drivers, which used to fail the whole query and report WoL as off even
	// though the adapter's advanced property "*WakeOnMagicPacket" is enabled.
	// That property is the fallback (registry value 1 = enabled).
	wolScript := `Get-NetAdapter -Physical | ForEach-Object {
  $n = $_.Name; $v = 0
  try { $pm = Get-NetAdapterPowerManagement -Name $n -ErrorAction Stop; $v = [int]$pm.WakeOnMagicPacket } catch { $v = -1 }
  if ($v -ne 2) {
    $p = Get-NetAdapterAdvancedProperty -Name $n -RegistryKeyword '*WakeOnMagicPacket' -ErrorAction SilentlyContinue
    if ($p) { if ([int]($p.RegistryValue | Select-Object -First 1) -eq 1) { $v = 2 } elseif ($v -lt 1) { $v = 1 } }
    elseif ($v -lt 0) { $v = 0 }
  }
  [pscustomobject]@{ MAC = $_.MacAddress; WakeOnMagicPacket = $v }
} | ConvertTo-Json -Compress`
	wolCmd := systool.Command(systool.PowerShell, "-NoProfile", "-Command", wolScript)
	wolOutput, wolErr := wolCmd.CombinedOutput()
	if wolErr != nil {
		logx.Printf("WoL PowerShell query failed: %v", wolErr)
		result.Warning = "WoL status unavailable (requires admin privileges)"
	}

	type wolInfo struct {
		MAC               string `json:"MAC"`
		WakeOnMagicPacket int    `json:"WakeOnMagicPacket"`
	}
	wolMap := make(map[string]wolInfo)
	wolTrimmed := strings.TrimSpace(string(wolOutput))
	if len(wolTrimmed) > 0 {
		var wolList []wolInfo
		if wolTrimmed[0] == '[' {
			json.Unmarshal([]byte(wolTrimmed), &wolList)
		} else {
			var single wolInfo
			if json.Unmarshal([]byte(wolTrimmed), &single) == nil {
				wolList = []wolInfo{single}
			}
		}
		for _, w := range wolList {
			wolMap[strings.ToUpper(w.MAC)] = w
		}
	}

	for _, a := range realAdapters {
		wa := status.NetAdapter{
			Name:       a.Name,
			MacAddress: a.MAC,
			IPs:        a.IPs,
			Status:     "Down",
		}
		if a.Up {
			wa.Status = "Up"
		}

		// Match by MAC (normalize to XX-XX-XX format)
		macKey := strings.ToUpper(strings.ReplaceAll(a.MAC, ":", "-"))
		if wol, ok := wolMap[macKey]; ok {
			wa.WoLCapable = wol.WakeOnMagicPacket != 0
			wa.WoLEnabled = wol.WakeOnMagicPacket == 2
		}
		result.Adapters = append(result.Adapters, wa)

		if a.Up && wa.WoLEnabled {
			result.Ready = true
		}
	}

	// Get external IP (best-effort, non-blocking with timeout)
	result.ExternalIP = ExternalIP()

	return result
}

// formatMAC converts "aa:bb:cc:dd:ee:ff" to "AA-BB-CC-DD-EE-FF"
func formatMAC(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
}

const externalIPCacheTTL = 10 * time.Minute

// ExternalIP queries an external service for the public IP (cached 10min)
func ExternalIP() string {
	externalIPMu.Lock()
	defer externalIPMu.Unlock()

	if cachedExternalIP != "" && time.Since(externalIPCachedAt) < externalIPCacheTTL {
		return cachedExternalIP
	}

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return cachedExternalIP
	}
	resp, err := client.Do(req)
	if err != nil {
		// Return stale cache if available
		return cachedExternalIP
	}
	defer resp.Body.Close()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	ip := strings.TrimSpace(string(body[:n]))
	if net.ParseIP(ip) != nil {
		cachedExternalIP = ip
		externalIPCachedAt = time.Now()
		return ip
	}
	return cachedExternalIP
}

// External IP cache
var (
	cachedExternalIP   string
	externalIPCachedAt time.Time
	externalIPMu       sync.Mutex
)
