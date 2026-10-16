package service

// Tests for the firewall rules (#76). The netsh runner is replaced
// throughout: nothing here executes netsh, installs or uninstalls
// anything.

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/stapi"
)

// fakeNetsh records every invocation and answers "show rule" from a set of
// rule names it pretends the firewall holds.
type fakeNetsh struct {
	existing map[string]bool
	calls    [][]string
	// addErr, when set, fails every "add rule".
	addErr error
	// ports is each rule's localport, from its "add rule" or set by the
	// test; "show rule" lists it the way netsh does (#139).
	ports map[string]string
	// showOut, when set for a name, is "show rule"'s whole answer for it.
	showOut map[string]string
}

// netshShowRule is "show rule" output for one TCP rule, as an English
// Windows prints it.
func netshShowRule(name, port string) string {
	return "\r\nRule Name:                            " + name + "\r\n" +
		"----------------------------------------------------------------------\r\n" +
		"Enabled:                              Yes\r\n" +
		"Direction:                            In\r\n" +
		"Profiles:                             Domain,Private,Public\r\n" +
		"Grouping:                             \r\n" +
		"LocalIP:                              Any\r\n" +
		"RemoteIP:                             Any\r\n" +
		"Protocol:                             TCP\r\n" +
		"LocalPort:                            " + port + "\r\n" +
		"RemotePort:                           Any\r\n" +
		"Edge traversal:                       No\r\n" +
		"Action:                               Allow\r\n" +
		"Ok.\r\n"
}

func newFakeNetsh(existing ...string) *fakeNetsh {
	f := &fakeNetsh{existing: map[string]bool{}, ports: map[string]string{}, showOut: map[string]string{}}
	for _, name := range existing {
		f.existing[name] = true
	}
	return f
}

// run is the netshRunner installed by withFakeNetsh.
func (f *fakeNetsh) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	name := ruleNameArg(args)
	switch verbOf(args) {
	case "show":
		if !f.existing[name] {
			// netsh exits non-zero when nothing matches.
			return []byte("No rules match the specified criteria."), errors.New("exit status 1")
		}
		if out, ok := f.showOut[name]; ok {
			return []byte(out), nil
		}
		if port, ok := f.ports[name]; ok {
			return []byte(netshShowRule(name, port)), nil
		}
		return []byte("Rule Name: " + name), nil
	case "add":
		if f.addErr != nil {
			return []byte("The requested operation requires elevation."), f.addErr
		}
		f.existing[name] = true
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "localport="); ok {
				f.ports[name] = v
			}
		}
	case "delete":
		if !f.existing[name] {
			return []byte("No rules match the specified criteria."), errors.New("exit status 1")
		}
		delete(f.existing, name)
		delete(f.ports, name)
		delete(f.showOut, name)
	}
	return []byte("Ok."), nil
}

// verbOf returns the netsh sub-command ("add", "delete", "show").
func verbOf(args []string) string {
	if len(args) < 3 {
		return ""
	}
	return args[2]
}

// ruleNameArg returns the value of the name= argument.
func ruleNameArg(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "name="); ok {
			return v
		}
	}
	return ""
}

// verbs lists the sub-command of each recorded call, in order.
func (f *fakeNetsh) verbs() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, verbOf(c))
	}
	return out
}

// withFakeNetsh installs f for the duration of the test.
func withFakeNetsh(t *testing.T, f *fakeNetsh) {
	t.Helper()
	prev := sys.netsh
	sys.netsh = f.run
	t.Cleanup(func() { sys.netsh = prev })
}

// hasArgs reports whether call contains every want argument.
func hasArgs(call []string, want ...string) bool {
	have := map[string]bool{}
	for _, a := range call {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// TestFirewallRuleCalls: each entry point runs exactly these netsh calls
// against a firewall that does or does not hold the rule yet. A rule that
// exists is never added twice (a restart, an upgrade); the command-port
// rule is deleted first so a changed port replaces the stale one. The SSDP
// rule's name is the one the user documentation tells people to look for.
func TestFirewallRuleCalls(t *testing.T) {
	const ssdpName = "SmartThings PC Control SSDP"
	ssdpAdd := []string{"name=" + ssdpName, "dir=in", "action=allow", "protocol=udp", "localport=1900"}
	for _, tc := range []struct {
		name     string
		existing []string
		run      func() error
		verbs    string
		add      []string // the add rule call's arguments, when there is one
	}{
		{"SSDP rule, missing", nil, ensureSSDPFirewallRule, "show add", ssdpAdd},
		{"SSDP rule, present", []string{ssdpName}, ensureSSDPFirewallRule, "show", nil},
		{"SSDP rule, three restarts", nil, func() error {
			for range 3 {
				if err := ensureSSDPFirewallRule(); err != nil {
					return err
				}
			}
			return nil
		}, "show add show show", ssdpAdd},
		{"SSDP rule, uninstall", []string{ssdpName}, removeSSDPFirewallRule, "delete", nil},
		{"any rule, TCP", nil, func() error { return ensureFirewallRule("Test Rule", firewallProtoTCP, 5001) }, "show add",
			[]string{"name=Test Rule", "dir=in", "action=allow", "protocol=tcp", "localport=5001"}},
		{"command port, replacing the old port", []string{firewallRuleName}, func() error { return addFirewallRule(5005) }, "delete show add",
			[]string{"name=" + firewallRuleName, "protocol=tcp", "localport=5005"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNetsh(tc.existing...)
			withFakeNetsh(t, f)
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(f.verbs(), " "); got != tc.verbs {
				t.Fatalf("netsh calls = %q, want %q (%v)", got, tc.verbs, f.calls)
			}
			for _, c := range f.calls {
				if c[0] != "advfirewall" || c[1] != "firewall" || c[3] != "rule" {
					t.Errorf("call prefix = %v", c[:4])
				}
				if verbOf(c) == "add" && !hasArgs(c, tc.add...) {
					t.Errorf("add rule args = %v, want %v among them", c, tc.add)
				}
				if verbOf(c) == "delete" && ruleNameArg(c) == "" {
					t.Errorf("delete without a rule name: %v", c)
				}
			}
		})
	}
	if stapi.SSDPPort != 1900 || ssdpFirewallRuleName != ssdpName {
		t.Errorf("the SSDP rule follows stapi.SSDPPort %d and is named %q", stapi.SSDPPort, ssdpFirewallRuleName)
	}
}

func TestEnsureFirewallRuleReportsAddFailure(t *testing.T) {
	f := newFakeNetsh()
	f.addErr = errors.New("exit status 1")
	withFakeNetsh(t, f)

	err := ensureSSDPFirewallRule()
	if err == nil {
		t.Fatal("want an error when netsh add fails")
	}
	if !strings.Contains(err.Error(), ssdpFirewallRuleName) ||
		!strings.Contains(err.Error(), "elevation") {
		t.Fatalf("error should name the rule and carry netsh output: %v", err)
	}
}

// ---- the start-time upgrade path -------------------------------------------

// TestEnsureSSDPFirewallRuleAtStartIsUnconditional guards #95: there is no
// setting that could suppress the rule any more, and the outcome of every
// run is cached for the app's search-status line.
func TestEnsureSSDPFirewallRuleAtStartIsUnconditional(t *testing.T) {
	prev := getConfig()
	prevOK := ssdpFirewallRuleOK()
	t.Cleanup(func() {
		setConfig(prev)
		ssdpFirewallOK.Store(prevOK)
	})
	// An empty smartthings object is what a fresh config.json holds; it
	// must not keep the rule from being ensured.
	setConfig(Config{Port: 5001})

	t.Run("a missing rule is added", func(t *testing.T) {
		f := newFakeNetsh()
		withFakeNetsh(t, f)
		ssdpFirewallOK.Store(false)

		ensureSSDPFirewallRuleAtStart()
		add := slices.Index(f.verbs(), "add")
		if add < 0 {
			t.Fatalf("no add rule call: %v", f.calls)
		}
		if !hasArgs(f.calls[add], "name="+ssdpFirewallRuleName, "protocol=udp", "localport=1900") {
			t.Fatalf("add rule args = %v", f.calls[add])
		}
		if !ssdpFirewallRuleOK() {
			t.Error("the rule was added but the cached state says it is missing")
		}
	})

	t.Run("an existing rule is left alone", func(t *testing.T) {
		f := newFakeNetsh(ssdpFirewallRuleName)
		withFakeNetsh(t, f)
		ssdpFirewallOK.Store(false)

		ensureSSDPFirewallRuleAtStart()
		if got := f.verbs(); len(got) != 1 || got[0] != "show" {
			t.Fatalf("calls = %v, want a single show", got)
		}
		if !ssdpFirewallRuleOK() {
			t.Error("an existing rule was not reported as present")
		}
	})

	t.Run("a netsh failure never panics and is reported", func(t *testing.T) {
		f := newFakeNetsh()
		f.addErr = errors.New("exit status 1")
		withFakeNetsh(t, f)
		ssdpFirewallOK.Store(true)

		ensureSSDPFirewallRuleAtStart() // best effort: returns nothing, logs
		if ssdpFirewallRuleOK() {
			t.Error("a failed add still reports the rule as present")
		}
	})
}

// ---- the command-port rule follows the port (#139) -------------------------

// TestEnsureCommandFirewallRuleFollowsThePort: at every start the TCP rule
// is checked against the configured port. A rule already for that port is
// left alone; one for another port (the port was changed in the settings),
// one whose port could not be read, or none at all is replaced. The
// outcome is cached for /api/st/hub.
func TestEnsureCommandFirewallRuleFollowsThePort(t *testing.T) {
	prev := commandFirewall.Load()
	t.Cleanup(func() { commandFirewall.Store(prev) })

	// What a Korean Windows prints: the labels are translated, the values
	// are not. The IPv6 LocalIP must not pass for a port.
	korean := "\r\n규칙 이름:                             " + firewallRuleName + "\r\n" +
		"----------------------------------------------------------------------\r\n" +
		"사용:                                  예\r\n" +
		"방향:                                  들어오는 방향\r\n" +
		"로컬 IP:                               fe80::1\r\n" +
		"프로토콜:                              TCP\r\n" +
		"로컬 포트:                             5001\r\n" +
		"원격 포트:                             모두\r\n" +
		"작업:                                  허용\r\n" +
		"확인되었습니다.\r\n"

	for _, tc := range []struct {
		name    string
		port    string // the rule's port in the fake firewall, "" = no rule
		showOut string // a literal "show rule" answer instead
		want    int    // the configured port
		verbs   string // the netsh calls
		changed bool
	}{
		{"no rule yet", "", "", 5001, "show show add", true},
		{"same port, left alone", "5001", "", 5001, "show", false},
		{"port changed, replaced", "5001", "", 5005, "show delete show add", true},
		{"translated labels, same port", "", korean, 5001, "show", false},
		{"translated labels, port changed", "", korean, 5005, "show delete show add", true},
		{"unreadable output, replaced", "", "Rule Name: " + firewallRuleName, 5001, "show delete show add", true},
		{"two rules, one stale", "", netshShowRule(firewallRuleName, "5001") + netshShowRule(firewallRuleName, "5003"), 5001, "show delete show add", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNetsh()
			if tc.port != "" {
				f.existing[firewallRuleName] = true
				f.ports[firewallRuleName] = tc.port
			}
			if tc.showOut != "" {
				f.existing[firewallRuleName] = true
				f.showOut[firewallRuleName] = tc.showOut
			}
			withFakeNetsh(t, f)
			commandFirewall.Store(nil)

			changed, err := ensureCommandFirewallRule(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(f.verbs(), " "); got != tc.verbs {
				t.Fatalf("netsh calls = %q, want %q (%v)", got, tc.verbs, f.calls)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if add := slices.Index(f.verbs(), "add"); add >= 0 {
				want := strconv.Itoa(tc.want)
				if !hasArgs(f.calls[add], "name="+firewallRuleName, "dir=in", "action=allow", "protocol=tcp", "localport="+want) {
					t.Errorf("add rule args = %v", f.calls[add])
				}
			}
			st, checked := commandFirewallRuleState()
			if !checked || st.Port != tc.want || !st.OK {
				t.Errorf("cached state = %+v (checked %v), want port %d ok", st, checked, tc.want)
			}
		})
	}
}

// A failed add is reported, logged by the start path and cached as
// missing, so the app does not claim a rule that is not there.
func TestEnsureCommandFirewallRuleReportsFailure(t *testing.T) {
	prev := commandFirewall.Load()
	t.Cleanup(func() { commandFirewall.Store(prev) })

	f := newFakeNetsh(firewallRuleName)
	f.ports[firewallRuleName] = "5001"
	f.addErr = errors.New("exit status 1")
	withFakeNetsh(t, f)

	if _, err := ensureCommandFirewallRule(5005); err == nil {
		t.Fatal("want an error when netsh add fails")
	}
	if st, checked := commandFirewallRuleState(); !checked || st.OK || st.Port != 5005 {
		t.Errorf("cached state = %+v (checked %v), want port 5005 missing", st, checked)
	}

	commandFirewall.Store(nil)
	ensureCommandFirewallRuleAtStart(5005) // best effort: logs, never panics
	if st, _ := commandFirewallRuleState(); st.OK {
		t.Error("a failed add at start still reports the rule as present")
	}

	// Port 0 (no config yet) touches nothing.
	f.calls = nil
	ensureCommandFirewallRuleAtStart(0)
	if len(f.calls) != 0 {
		t.Errorf("port 0 ran netsh: %v", f.calls)
	}
}
