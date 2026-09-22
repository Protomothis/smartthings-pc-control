package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ---- local shutdown reason, System/User32 event 1074 (#87) ----------------

// sampleEvent1074 is what `wevtutil qe System /q:... /c:1 /rd:true /f:xml`
// prints: one bare <Event> element, no document root, with the shutdown
// type in the fifth insertion string.
func sampleEvent1074(at time.Time, shutdownType string) []byte {
	return []byte(fmt.Sprintf(`<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'>`+
		`<System><Provider Name='User32'/><EventID Qualifiers='32768'>1074</EventID>`+
		`<Level>4</Level><Task>0</Task><Keywords>0x80000000000000</Keywords>`+
		`<TimeCreated SystemTime='%s'/><EventRecordID>91234</EventRecordID>`+
		`<Channel>System</Channel><Computer>DESKTOP-TEST</Computer>`+
		`<Security UserID='S-1-5-21-1-2-3-1001'/></System>`+
		`<EventData><Data Name='param1'>Explorer.EXE</Data>`+
		`<Data Name='param2'>DESKTOP-TEST</Data>`+
		`<Data Name='param3'>No title for this reason could be found</Data>`+
		`<Data Name='param4'>0x500ff</Data>`+
		`<Data Name='param5'>%s</Data>`+
		`<Data Name='param6'></Data>`+
		`<Data Name='param7'>DESKTOP-TEST\kim</Data></EventData></Event>`,
		at.UTC().Format("2006-01-02T15:04:05.0000000Z"), shutdownType))
}

func TestParseShutdownReasonMapsTheShutdownType(t *testing.T) {
	now := time.Now()
	// The English strings user32 writes into the raw EventData, plus the
	// Korean ones in case a build does localise them.
	for _, tc := range []struct{ typ, want string }{
		{"restart", "restart"},
		{"power off", "shutdown"},
		{"shutdown", "shutdown"},
		{"다시 시작", "restart"},
		{"재시작", "restart"},
		{"종료", "shutdown"},
		{"전원 끄기", "shutdown"},
		{"hibernate", "hibernate"},
		{"sleep", "suspend"},
	} {
		got, ok := parseShutdownReason(sampleEvent1074(now, tc.typ), now)
		if !ok || got != tc.want {
			t.Errorf("shutdown type %q → (%q, %v), want (%q, true)", tc.typ, got, ok, tc.want)
		}
	}
}

func TestParseShutdownReasonIgnoresAStaleRecord(t *testing.T) {
	// The last restart was yesterday; this stop is something else, and
	// "restart" would leave the tile lying about a PC that is off.
	now := time.Now()
	if _, ok := parseShutdownReason(sampleEvent1074(now.Add(-25*time.Hour), "restart"), now); ok {
		t.Error("a day-old 1074 record must not explain this stop")
	}
	// Right at the edge of the window it still counts.
	if got, ok := parseShutdownReason(sampleEvent1074(now.Add(-90*time.Second), "restart"), now); !ok || got != "restart" {
		t.Errorf("a 90s-old record → (%q, %v), want (restart, true)", got, ok)
	}
}

func TestParseShutdownReasonRejectsWhatItCannotRead(t *testing.T) {
	now := time.Now()
	for name, out := range map[string]string{
		"empty":        "",
		"not xml":      "The channel was not found.",
		"truncated":    "<Event><System><TimeCreated SystemTime='",
		"no eventdata": "<Event><System><Provider Name='User32'/></System></Event>",
		"unknown type": string(sampleEvent1074(now, "quantum collapse")),
	} {
		if got, ok := parseShutdownReason([]byte(out), now); ok {
			t.Errorf("%s: parsed as %q, want no answer", name, got)
		}
	}
}

func TestParseShutdownReasonFallsBackToAnotherInsertionString(t *testing.T) {
	// If a future Windows renumbers the insertion strings, a readable
	// answer somewhere in the record still beats guessing "shutdown".
	out := `<Event><System><Provider Name='User32'/></System><EventData>` +
		`<Data Name='param9'>restart</Data></EventData></Event>`
	if got, ok := parseShutdownReason([]byte(out), time.Now()); !ok || got != "restart" {
		t.Errorf("param9 fallback → (%q, %v), want (restart, true)", got, ok)
	}
}

func TestParseShutdownReasonWithoutATimestamp(t *testing.T) {
	// The query already filtered on TimeCreated, so a record whose
	// timestamp cannot be read is trusted rather than thrown away.
	out := `<Event><System><Provider Name='User32'/></System><EventData>` +
		`<Data Name='param5'>power off</Data></EventData></Event>`
	if got, ok := parseShutdownReason([]byte(out), time.Now()); !ok || got != "shutdown" {
		t.Errorf("record without TimeCreated → (%q, %v), want (shutdown, true)", got, ok)
	}
}

func TestLocalShutdownReasonUsesTheRunner(t *testing.T) {
	orig := localShutdownRunner
	t.Cleanup(func() { localShutdownRunner = orig })

	localShutdownRunner = func(context.Context) ([]byte, error) {
		return sampleEvent1074(time.Now(), "restart"), nil
	}
	if got, ok := localShutdownReason(); !ok || got != "restart" {
		t.Errorf("localShutdownReason() = (%q, %v), want (restart, true)", got, ok)
	}

	// wevtutil missing, the query timing out, an empty System log: all of
	// them leave the caller on its own fallback.
	localShutdownRunner = func(context.Context) ([]byte, error) {
		return nil, errors.New("exec: wevtutil: executable file not found in %PATH%")
	}
	if got, ok := localShutdownReason(); ok {
		t.Errorf("a failed query returned %q, want no answer", got)
	}
}

func TestLocalShutdownReasonHonoursItsDeadline(t *testing.T) {
	orig := localShutdownRunner
	t.Cleanup(func() { localShutdownRunner = orig })

	var deadline time.Time
	localShutdownRunner = func(ctx context.Context) ([]byte, error) {
		deadline, _ = ctx.Deadline()
		return nil, ctx.Err()
	}
	localShutdownReason()
	if deadline.IsZero() {
		t.Fatal("the runner was called without a deadline; a hung wevtutil would hold the shutdown")
	}
	if budget := time.Until(deadline); budget > localShutdownTimeout+time.Second {
		t.Errorf("deadline is %s away, want ≤ %s: the stop path has a budget", budget, localShutdownTimeout)
	}
}

// ---- the whole ladder (§6.2) ---------------------------------------------

func TestStopReasonPrefersTheCommandHint(t *testing.T) {
	orig := localShutdownRunner
	t.Cleanup(func() { localShutdownRunner = orig; resetPowerCommandHint() })

	// The event log says restart; the command this service ran says
	// suspend. Only the command knows about suspend at all, and 1074 is
	// not even written for one, so the hint wins.
	localShutdownRunner = func(context.Context) ([]byte, error) {
		return sampleEvent1074(time.Now(), "restart"), nil
	}
	resetPowerCommandHint()
	notePowerCommand("suspend")
	if got := stopReason(true); got != "suspend" {
		t.Errorf("stopReason(shutdown) = %q, want suspend (the command hint)", got)
	}
}

func TestStopReasonReadsTheEventLogWithoutAHint(t *testing.T) {
	orig := localShutdownRunner
	t.Cleanup(func() { localShutdownRunner = orig; resetPowerCommandHint() })
	resetPowerCommandHint()

	localShutdownRunner = func(context.Context) ([]byte, error) {
		return sampleEvent1074(time.Now(), "restart"), nil
	}
	if got := stopReason(true); got != "restart" {
		t.Errorf("stopReason(shutdown) = %q, want restart (from event 1074)", got)
	}

	localShutdownRunner = func(context.Context) ([]byte, error) {
		return sampleEvent1074(time.Now(), "power off"), nil
	}
	if got := stopReason(true); got != "shutdown" {
		t.Errorf("stopReason(shutdown) = %q, want shutdown (from event 1074)", got)
	}
}

func TestStopReasonFallsBackWhenTheLogSaysNothing(t *testing.T) {
	orig := localShutdownRunner
	t.Cleanup(func() { localShutdownRunner = orig; resetPowerCommandHint() })
	resetPowerCommandHint()

	called := 0
	localShutdownRunner = func(context.Context) ([]byte, error) {
		called++
		return nil, errors.New("no events")
	}
	if got := stopReason(true); got != "shutdown" {
		t.Errorf("stopReason(shutdown) = %q, want shutdown", got)
	}

	// A plain SERVICE_CONTROL_STOP is not a system shutdown: the PC stays
	// on, so the reason stays "unknown" and the event log is not read -
	// the newest 1074 would be about the last real shutdown.
	before := called
	if got := stopReason(false); got != "unknown" {
		t.Errorf("stopReason(stop) = %q, want unknown", got)
	}
	if called != before {
		t.Error("a plain stop must not query the event log")
	}
}
