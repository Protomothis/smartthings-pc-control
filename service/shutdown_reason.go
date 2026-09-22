package service

import (
	"context"
	"encoding/xml"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Local shutdown vs restart (#87).
//
// The SCM tells a service only that the system is going down
// (SERVICE_CONTROL_SHUTDOWN); it never says whether Windows is restarting
// or powering off. The Edge driver needs the difference, because
// power.stopping's reason decides what the tile shows (edge-driver doc
// §6.2), and a restart that reads as "off" leaves a switch stuck at off
// until the PC is back.
//
// Windows itself records the answer: user32 writes System event 1074 just
// before the shutdown starts, and its fifth insertion string is the
// shutdown type. Only the rendered message is localised — the raw
// EventData that /f:xml prints is written by user32 in English — so this
// reads the XML rather than parsing "Shutdown Type:" out of the text
// format, which on a Korean install says "종료 유형:" instead.

// localShutdownWindow is how old the newest 1074 record may be and still
// be about the shutdown now in progress. Windows logs it, then tells the
// services, so the gap is seconds; 2 minutes is slack, not a guess.
const localShutdownWindow = 120 * time.Second

// localShutdownTimeout caps the wevtutil call. It runs on the stop path,
// where the SmartThings push already spends up to 1.5s, and only when no
// remote command explains the stop — so the common case (a shutdown this
// service itself ran) pays nothing for it.
const localShutdownTimeout = 1500 * time.Millisecond

// localShutdownQuery selects the newest User32/1074 record written inside
// localShutdownWindow. timediff() takes milliseconds.
const localShutdownQuery = "*[System[Provider[@Name='User32'] and (EventID=1074) and " +
	"TimeCreated[timediff(@SystemTime) <= 120000]]]"

// shutdownTypeReason maps the shutdown type of event 1074 to a
// power.stopping reason (§6.2). The keys are matched as substrings of the
// lower-cased insertion string, so "power off" catches "power off" and
// Korean installs that did localise it are covered by their own rows.
var shutdownTypeReason = []struct {
	token, reason string
}{
	{"restart", "restart"},
	{"다시 시작", "restart"},
	{"재시작", "restart"},
	{"power off", "shutdown"},
	{"shutdown", "shutdown"},
	{"전원 끄기", "shutdown"},
	{"종료", "shutdown"},
	{"hibernate", "hibernate"},
	{"최대 절전", "hibernate"},
	{"sleep", "suspend"},
	{"절전", "suspend"},
}

// localShutdownRunner returns the raw XML of the newest matching record.
// It is a variable so tests can feed parseShutdownReason sample output
// without a Windows event log.
var localShutdownRunner = runLocalShutdownQuery

// runLocalShutdownQuery asks wevtutil for the one record. /rd:true reads
// newest-first, /c:1 stops after one, /f:xml prints the raw EventData
// (the rendered, localised message is not included).
func runLocalShutdownQuery(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "wevtutil", "qe", "System",
		"/q:"+localShutdownQuery, "/c:1", "/rd:true", "/f:xml")
	// A service has no console, but this must never flash one if the
	// binary is ever stopped from an interactive session.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Output()
}

// stopReason is the power.stopping reason for one SCM stop (§6.2).
// systemShutdown is true for SERVICE_CONTROL_SHUTDOWN, false for a plain
// SERVICE_CONTROL_STOP.
//
// The ladder, most trustworthy first:
//
//  1. a power command this service ran in the last two minutes — it is
//     the only source that can tell suspend and hibernate apart, and the
//     only one that is right when the stop is something this service
//     caused;
//  2. for a system shutdown, the newest User32/1074 record, which is how
//     restart is told from power off (#87);
//  3. "shutdown" for a system shutdown, "unknown" for a plain stop — the
//     service is going away but the PC is not.
func stopReason(systemShutdown bool) string {
	// "" means "no recent command hint"; stoppingReason returns whatever
	// it is given when there is one.
	if reason := stoppingReason(""); reason != "" {
		return reason
	}
	if !systemShutdown {
		return "unknown"
	}
	if reason, ok := localShutdownReason(); ok {
		return reason
	}
	return "shutdown"
}

// localShutdownReason reports the reason of the shutdown Windows is
// running right now, or false when the event log has nothing recent to
// say (no 1074 in the window, wevtutil missing, query timed out, a
// shutdown type this does not know). The caller falls back to "shutdown".
func localShutdownReason() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), localShutdownTimeout)
	defer cancel()
	out, err := localShutdownRunner(ctx)
	if err != nil {
		return "", false
	}
	return parseShutdownReason(out, time.Now())
}

// eventRecord is the part of an event-log XML record this needs: the
// insertion strings user32 filled in. The timestamp is an attribute of a
// nested empty element, which a struct field cannot reach, so
// eventSystemTime reads it separately.
type eventRecord struct {
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

// parseShutdownReason turns wevtutil's XML into a power.stopping reason.
//
// wevtutil prints bare <Event> elements with no document root, so this
// decodes the first one out of the stream. now is passed in so the age
// check is testable; a record whose TimeCreated cannot be read is trusted,
// because the query already filtered on it.
func parseShutdownReason(out []byte, now time.Time) (string, bool) {
	dec := xml.NewDecoder(strings.NewReader(string(out)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return "", false
		}
		if err != nil {
			return "", false
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Event" {
			continue
		}
		var rec eventRecord
		if err := dec.DecodeElement(&rec, &start); err != nil {
			return "", false
		}
		if at, ok := eventSystemTime(out); ok && now.Sub(at) > localShutdownWindow {
			return "", false
		}
		return shutdownTypeOf(rec)
	}
}

// eventSystemTime pulls the TimeCreated SystemTime attribute out of the
// raw record. encoding/xml cannot address an attribute of a nested empty
// element, and a second parse of one small record is cheaper than a
// hand-rolled System struct that would have to track every sibling.
func eventSystemTime(out []byte) (time.Time, bool) {
	dec := xml.NewDecoder(strings.NewReader(string(out)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return time.Time{}, false
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "TimeCreated" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local != "SystemTime" {
				continue
			}
			// Windows writes RFC3339 with up to 100ns precision.
			if at, err := time.Parse(time.RFC3339Nano, attr.Value); err == nil {
				return at, true
			}
			return time.Time{}, false
		}
		return time.Time{}, false
	}
}

// shutdownTypeOf maps a record's insertion strings to a reason. param5 is
// the shutdown type; if a future Windows renames it, every other string is
// tried as well, which costs nothing and beats returning "shutdown" for a
// restart.
func shutdownTypeOf(rec eventRecord) (string, bool) {
	var others []string
	for _, d := range rec.Data {
		if d.Name == "param5" {
			if reason, ok := matchShutdownType(d.Value); ok {
				return reason, true
			}
			continue
		}
		others = append(others, d.Value)
	}
	for _, v := range others {
		if reason, ok := matchShutdownType(v); ok {
			return reason, true
		}
	}
	return "", false
}

// matchShutdownType finds the first known shutdown type inside s.
func matchShutdownType(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", false
	}
	for _, m := range shutdownTypeReason {
		if strings.Contains(s, m.token) {
			return m.reason, true
		}
	}
	return "", false
}
