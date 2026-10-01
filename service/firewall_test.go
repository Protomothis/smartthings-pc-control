package service

// Tests for the firewall rules (#76). The netsh runner is replaced
// throughout: nothing here executes netsh, installs or uninstalls
// anything.

import (
	"errors"
	"slices"
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
}

func newFakeNetsh(existing ...string) *fakeNetsh {
	f := &fakeNetsh{existing: map[string]bool{}}
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
		return []byte("Rule Name: " + name), nil
	case "add":
		if f.addErr != nil {
			return []byte("The requested operation requires elevation."), f.addErr
		}
		f.existing[name] = true
	case "delete":
		if !f.existing[name] {
			return []byte("No rules match the specified criteria."), errors.New("exit status 1")
		}
		delete(f.existing, name)
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
