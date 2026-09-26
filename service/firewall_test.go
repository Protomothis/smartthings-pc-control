package service

// Tests for the firewall rules (#76). The netsh runner is replaced
// throughout: nothing here executes netsh, installs or uninstalls
// anything.

import (
	"errors"
	"slices"
	"strings"
	"testing"
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
	prev := runNetsh
	runNetsh = f.run
	t.Cleanup(func() { runNetsh = prev })
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

// ---- argument construction -------------------------------------------------

func TestEnsureFirewallRuleAddsInboundRule(t *testing.T) {
	f := newFakeNetsh()
	withFakeNetsh(t, f)

	if err := ensureFirewallRule("Test Rule", firewallProtoUDP, 1900); err != nil {
		t.Fatalf("ensureFirewallRule: %v", err)
	}
	if got := f.verbs(); len(got) != 2 || got[0] != "show" || got[1] != "add" {
		t.Fatalf("calls = %v, want show then add", got)
	}
	add := f.calls[1]
	if add[0] != "advfirewall" || add[1] != "firewall" || add[3] != "rule" {
		t.Fatalf("add prefix = %v", add[:4])
	}
	if !hasArgs(add, "name=Test Rule", "dir=in", "action=allow", "protocol=udp", "localport=1900") {
		t.Fatalf("add rule args = %v", add)
	}
}

func TestEnsureFirewallRuleTCPPortIsFormatted(t *testing.T) {
	f := newFakeNetsh()
	withFakeNetsh(t, f)

	if err := ensureFirewallRule(firewallRuleName, firewallProtoTCP, 5001); err != nil {
		t.Fatalf("ensureFirewallRule: %v", err)
	}
	if !hasArgs(f.calls[1], "protocol=tcp", "localport=5001", "name="+firewallRuleName) {
		t.Fatalf("add rule args = %v", f.calls[1])
	}
}

func TestSSDPRuleUsesUDP1900AndTheDocumentedName(t *testing.T) {
	f := newFakeNetsh()
	withFakeNetsh(t, f)

	if err := ensureSSDPFirewallRule(); err != nil {
		t.Fatalf("ensureSSDPFirewallRule: %v", err)
	}
	if ssdpFirewallRuleName != "SmartThings PC Control SSDP" {
		t.Fatalf("rule name = %q", ssdpFirewallRuleName)
	}
	if !hasArgs(f.calls[1], "name="+ssdpFirewallRuleName, "dir=in", "action=allow",
		"protocol=udp", "localport=1900") {
		t.Fatalf("add rule args = %v", f.calls[1])
	}
	// The port comes from the responder's own constant, not a literal.
	if ssdpPort != 1900 {
		t.Fatalf("ssdpPort = %d", ssdpPort)
	}
}

func TestDeleteFirewallRuleArgs(t *testing.T) {
	f := newFakeNetsh(ssdpFirewallRuleName)
	withFakeNetsh(t, f)

	if err := removeSSDPFirewallRule(); err != nil {
		t.Fatalf("removeSSDPFirewallRule: %v", err)
	}
	del := f.calls[0]
	if verbOf(del) != "delete" || del[3] != "rule" || !hasArgs(del, "name="+ssdpFirewallRuleName) {
		t.Fatalf("delete args = %v", del)
	}
	if f.existing[ssdpFirewallRuleName] {
		t.Fatal("rule still present after delete")
	}
}

// ---- idempotence -----------------------------------------------------------

func TestEnsureFirewallRuleIsIdempotent(t *testing.T) {
	f := newFakeNetsh(ssdpFirewallRuleName)
	withFakeNetsh(t, f)

	if err := ensureSSDPFirewallRule(); err != nil {
		t.Fatalf("ensureSSDPFirewallRule: %v", err)
	}
	if got := f.verbs(); len(got) != 1 || got[0] != "show" {
		t.Fatalf("calls = %v, want a single show and no add", got)
	}
}

func TestEnsureFirewallRuleAddsOnlyOnceAcrossRestarts(t *testing.T) {
	f := newFakeNetsh()
	withFakeNetsh(t, f)

	for i := 0; i < 3; i++ {
		if err := ensureSSDPFirewallRule(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	adds := 0
	for _, v := range f.verbs() {
		if v == "add" {
			adds++
		}
	}
	if adds != 1 {
		t.Fatalf("add rule ran %d times, want 1", adds)
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

// ---- the command-port rule still replaces a stale port ---------------------

func TestAddFirewallRuleDeletesThenAdds(t *testing.T) {
	f := newFakeNetsh(firewallRuleName) // a rule for the old port exists
	withFakeNetsh(t, f)

	if err := addFirewallRule(5005); err != nil {
		t.Fatalf("addFirewallRule: %v", err)
	}
	got := f.verbs()
	if len(got) != 3 || got[0] != "delete" || got[1] != "show" || got[2] != "add" {
		t.Fatalf("calls = %v, want delete, show, add", got)
	}
	if !hasArgs(f.calls[2], "localport=5005", "protocol=tcp") {
		t.Fatalf("add rule args = %v", f.calls[2])
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
