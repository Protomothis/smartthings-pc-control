package service

// Windows Firewall rules (#76).
//
// The service opens two inbound ports: the command port over TCP (and the
// WebUI port when webui_remote is on) and UDP 1900 for the SSDP responder
// (#69). Without the UDP rule the Edge driver's M-SEARCH never reaches us
// on a default Windows firewall, so discovery silently finds nothing.
//
// Every rule goes through the same netsh runner, which tests replace: no
// test in this package ever shells out to the real netsh.

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync/atomic"
)

// netshRunner runs one netsh invocation and returns its combined output.
// A non-nil error means netsh exited non-zero (netsh exits 1 when
// "show rule" matches nothing, which is how rule existence is detected).
type netshRunner func(args ...string) ([]byte, error)

// runNetsh is the live runner; tests swap it out.
var runNetsh netshRunner = func(args ...string) ([]byte, error) {
	return exec.Command("netsh", args...).CombinedOutput()
}

const (
	firewallProtoTCP = "tcp"
	firewallProtoUDP = "udp"
)

// ssdpFirewallRuleName is the inbound UDP 1900 rule the SSDP responder
// needs. It is added at install and removed at uninstall; the service also
// re-adds it at start so an install made before #69 gains it.
const ssdpFirewallRuleName = "SmartThings PC Control SSDP"

// firewallRuleExists reports whether a rule of that name is present.
// netsh exits non-zero with "No rules match the specified criteria" when it
// is not, so the exit status carries the answer in any UI language.
func firewallRuleExists(name string) bool {
	_, err := runNetsh("advfirewall", "firewall", "show", "rule", "name="+name)
	return err == nil
}

// ensureFirewallRule adds an inbound allow rule for proto/port unless a
// rule of that name already exists, so calling it repeatedly is harmless.
// A caller that needs the rule to match a *changed* port deletes it first
// (see addFirewallRule) — the name is all this check looks at.
func ensureFirewallRule(name, proto string, port int) error {
	if firewallRuleExists(name) {
		return nil
	}
	output, err := runNetsh("advfirewall", "firewall", "add", "rule",
		"name="+name,
		"dir=in", "action=allow", "protocol="+proto,
		"localport="+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("netsh add rule %q failed: %v - output: %s", name, err, string(output))
	}
	return nil
}

// deleteFirewallRule removes every rule of that name.
func deleteFirewallRule(name string) error {
	output, err := runNetsh("advfirewall", "firewall", "delete", "rule",
		"name="+name)
	if err != nil {
		return fmt.Errorf("netsh delete rule %q failed: %v - output: %s", name, err, string(output))
	}
	return nil
}

// ssdpFirewallOK caches the last answer about ssdpFirewallRuleName. The
// app polls GET /api/st/hub every few seconds for the search-status line
// and must not make each poll shell out to netsh, so the rule is queried
// at service start (and whenever it is ensured) and the answer kept here.
// False before the first check, which is also what a PC with no rule
// looks like — the honest reading either way is "not confirmed".
var ssdpFirewallOK atomic.Bool

// ssdpFirewallRuleOK reports the cached answer (#95).
func ssdpFirewallRuleOK() bool { return ssdpFirewallOK.Load() }

// checkSSDPFirewallRule asks the firewall whether the rule is there and
// refreshes the cache.
func checkSSDPFirewallRule() bool {
	ok := firewallRuleExists(ssdpFirewallRuleName)
	ssdpFirewallOK.Store(ok)
	return ok
}

// ensureSSDPFirewallRule opens inbound UDP 1900 for the discovery
// responder. A nil error means the rule is in place afterwards, whether it
// was added now or already there.
func ensureSSDPFirewallRule() error {
	err := ensureFirewallRule(ssdpFirewallRuleName, firewallProtoUDP, ssdpPort)
	ssdpFirewallOK.Store(err == nil)
	return err
}

// removeSSDPFirewallRule drops the UDP 1900 rule (uninstall only).
func removeSSDPFirewallRule() error {
	return deleteFirewallRule(ssdpFirewallRuleName)
}

// ensureSSDPFirewallRuleAtStart is the upgrade path: a PC installed before
// #69 has no UDP rule, and reinstalling to get one is a poor answer. The
// service re-checks at every start, unconditionally (#95) — there is no
// setting that could turn discovery off any more. Only uninstall removes
// the rule.
//
// Best effort throughout — a failure (no admin rights, firewall service
// disabled) is logged and startup continues; the responder still works on
// networks where nothing blocks it. Either way the outcome is cached for
// the app's search-status line.
func ensureSSDPFirewallRuleAtStart() {
	if checkSSDPFirewallRule() {
		return
	}
	if err := ensureSSDPFirewallRule(); err != nil {
		logMsg("SSDP: could not ensure firewall rule %q (discovery may be blocked): %v",
			ssdpFirewallRuleName, err)
		return
	}
	logMsg("SSDP: firewall rule %q added (inbound UDP %d)", ssdpFirewallRuleName, ssdpPort)
}
