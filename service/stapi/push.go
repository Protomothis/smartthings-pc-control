package stapi

// Hub push subscriptions, /st/v1/subscribe (docs/design/edge-driver.md
// §3.5, issue #68). The Edge driver opens a listener on the hub, subscribes
// to it, and renews at 80% of the TTL; the service posts every device-state
// event to that callback with the full §3.2 status attached, so the driver
// updates without polling and without diffing.
//
//	POST   /st/v1/subscribe       {callback, ttl_seconds, driver_version}
//	DELETE /st/v1/subscribe/{id}
//
// Both go through auth like the rest of /st/v1. Subscriptions are kept in
// memory and, when Deps.Subscriptions is set, written through to the
// service's state file on every change and renewal (#142): a service that
// restarts (an update, a reboot) takes up the live ones again, and its
// power.started also reaches the hubs whose subscription ran out while the
// PC was off, so the driver learns that the PC is back without waiting for
// its next poll. See wakeTargets.
//
// Events arrive on a raw tap of the notification bus (notify.Bus.Tap), not
// as an ordinary Sink, because §3.5 is explicit that the notification
// category filter and quiet hours must not apply — device state is not a
// notification. Delivery is handed to a worker goroutine so an emitting
// path never waits on the network; power.stopping is the exception and is
// delivered inline, see PushTap.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/power"
)

const (
	// TTL bounds and default from §3.5.
	subMinTTL     = 60 * time.Second
	subMaxTTL     = 3600 * time.Second
	subDefaultTTL = 600 * time.Second
	// subSweepEvery is the background expiry sweep; every access sweeps
	// too, so this only matters while nothing happens.
	subSweepEvery = time.Minute
	// pushTimeout bounds one callback POST (§3.5).
	pushTimeout = 2 * time.Second
	// PushStoppingDeadline bounds the whole synchronous power.stopping
	// delivery, retry included, before the stop proceeds (§3.5), when
	// Windows itself is going down (shutdown, restart, suspend,
	// hibernate): the OS gives services only a few seconds then.
	PushStoppingDeadline = 1500 * time.Millisecond
	// PushAppStopDeadline is the same bound for reason app_stop, a plain
	// service stop with the PC staying on (§3.5). Nothing presses the stop
	// then, and this push is the hub's only sign that the app went away —
	// a stopped service's port answers with silence, not a refusal
	// (edge-platform-notes "연결 거부와 시간 초과") — so it gets room for a
	// full attempt (pushTimeout) and most of the retry.
	PushAppStopDeadline = 4 * time.Second
	// PushMaxFailures removes a subscription after this many consecutive
	// failed deliveries (§3.5).
	PushMaxFailures = 3
	// pushQueueCap bounds the asynchronous delivery queue. Events are
	// dropped (and logged) rather than blocking the emitter when a hub is
	// slow enough to fill it.
	pushQueueCap = 64
	// maxCallbackLen is a sanity bound on the callback URL.
	maxCallbackLen = 512
	// subRecallWindow is how long a subscription that ran out (or failed
	// out) is still told about power.started and power.resumed (#142).
	// A PC switched off for the night or the weekend comes back to a hub
	// whose subscription expired long ago; one that stayed off for longer
	// than this is unlikely to find the same listener there.
	subRecallWindow = 7 * 24 * time.Hour
)

// wakeRetryDelays are the extra attempts of a power.started or
// power.resumed push that failed (#142), after the immediate one and its
// retry: at boot and right after a resume the PC's network is often not up
// yet when the event is emitted. A variable so the tests can shorten it.
var wakeRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second}

// isWakeEvent reports whether typ is a push that announces the PC coming
// up, which also goes to the hubs whose subscription lapsed (#142).
func isWakeEvent(typ string) bool {
	return typ == "power.started" || typ == "power.resumed"
}

// ---- subscription store ----------------------------------------------------

// subscription is one hub listener. Callback is the key: a driver that
// subscribes again for the same URL renews rather than piling up (§3.5).
type subscription struct {
	ID            string
	Callback      string
	DriverVersion string
	ExpiresAt     time.Time
	// failures counts consecutive delivery failures; a success resets it.
	failures int
}

// subStore holds the live subscriptions and the recently lapsed ones. It is
// the only mutable push state.
type subStore struct {
	mu     sync.Mutex
	now    func() time.Time
	byID   map[string]*subscription
	byCall map[string]*subscription
	// lapsed are the subscriptions that expired or failed out within
	// subRecallWindow, keyed by callback: never pushed to, except for the
	// wake events (wakeTargets, #142).
	lapsed map[string]*subscription
	nextID int
	// sweeper is started on the first subscribe.
	sweeper sync.Once

	// persist writes the store through (#142); nil keeps it in memory.
	persist func([]SavedSubscription)
	// persistMu keeps the snapshots in the order they are written.
	persistMu sync.Mutex
}

func newSubStore(now func() time.Time) *subStore {
	return &subStore{
		now:    now,
		byID:   map[string]*subscription{},
		byCall: map[string]*subscription{},
		lapsed: map[string]*subscription{},
	}
}

// sweepLocked moves everything that has expired to lapsed, and forgets
// what lapsed subRecallWindow ago or longer. The caller holds mu.
func (s *subStore) sweepLocked(now time.Time) {
	for id, sub := range s.byID {
		if !now.Before(sub.ExpiresAt) {
			delete(s.byID, id)
			delete(s.byCall, sub.Callback)
			s.lapsed[sub.Callback] = sub
			logx.Printf("ST push: subscription %s (%s) expired", id, sub.Callback)
		}
	}
	for callback, sub := range s.lapsed {
		if now.Sub(sub.ExpiresAt) >= subRecallWindow {
			delete(s.lapsed, callback)
		}
	}
}

// subscribe registers or renews callback for ttl and reports whether this
// renewed an existing subscription. Either way the store is written
// through: the renewed expiry is what decides, after a restart, whether
// the subscription is taken up again or only recalled (#142).
func (s *subStore) subscribe(callback, driverVersion string, ttl time.Duration) (subscription, bool) {
	sub, renewed := s.subscribeLocked(callback, driverVersion, ttl)
	s.save()
	return sub, renewed
}

func (s *subStore) subscribeLocked(callback, driverVersion string, ttl time.Duration) (subscription, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	if sub, ok := s.byCall[callback]; ok {
		sub.ExpiresAt = now.Add(ttl)
		sub.DriverVersion = driverVersion
		sub.failures = 0
		return *sub, true
	}
	// A hub coming back for a callback that lapsed gets a new id: its
	// driver dropped the old one when the renewal failed.
	delete(s.lapsed, callback)
	// A hub whose driver restarted comes back on a new ephemeral port; the
	// listener behind its old callback is gone. Keeping that subscription
	// only buys 2s timeouts and retries on every event until it fails out,
	// so a new callback from the same host replaces the host's old ones,
	// the lapsed ones too.
	if host := callbackHost(callback); host != "" {
		for id, old := range s.byID {
			if old.Callback != callback && callbackHost(old.Callback) == host {
				delete(s.byID, id)
				delete(s.byCall, old.Callback)
				logx.Printf("ST push: %s (%s) replaced by a new subscription from %s", id, old.Callback, host)
			}
		}
		for old := range s.lapsed {
			if callbackHost(old) == host {
				delete(s.lapsed, old)
			}
		}
	}
	s.nextID++
	sub := &subscription{
		ID:            "sub-" + strconv.Itoa(s.nextID),
		Callback:      callback,
		DriverVersion: driverVersion,
		ExpiresAt:     now.Add(ttl),
	}
	s.byID[sub.ID] = sub
	s.byCall[callback] = sub
	s.startSweeper()
	return *sub, false
}

// remove drops the subscription with id and reports whether it was there.
// The hub said goodbye (its device was removed), so it is not recalled
// either.
func (s *subStore) remove(id string) bool {
	removed := s.removeLocked(id)
	if removed {
		s.save()
	}
	return removed
}

func (s *subStore) removeLocked(id string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if sub, ok := s.byID[id]; ok {
		delete(s.byID, id)
		delete(s.byCall, sub.Callback)
		return true
	}
	for callback, sub := range s.lapsed {
		if sub.ID == id {
			delete(s.lapsed, callback)
			return true
		}
	}
	return false
}

// active returns the unexpired subscriptions.
func (s *subStore) active() []subscription {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	out := make([]subscription, 0, len(s.byID))
	for _, sub := range s.byID {
		out = append(out, *sub)
	}
	return out
}

// wakeTargets returns the subscriptions a power.started or power.resumed
// goes to (#142): the unexpired ones and those that lapsed within
// subRecallWindow. The lapsed ones are why: a PC that was off or asleep
// for longer than the TTL could not be renewed against, and its hub would
// otherwise only find out at its next poll, up to a poll interval later.
// The driver routes a push by machine_id, not by subscription, so it takes
// one for a lapsed subscription too, and then subscribes again.
// lapsed is the number of lapsed ones, which come last.
func (s *subStore) wakeTargets() (subs []subscription, lapsed int) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	out := make([]subscription, 0, len(s.byID)+len(s.lapsed))
	for _, sub := range s.byID {
		out = append(out, *sub)
	}
	for _, sub := range s.lapsed {
		out = append(out, *sub)
	}
	return out, len(s.lapsed)
}

// anyLapsed reports whether a wake event has a lapsed subscription to go to.
func (s *subStore) anyLapsed() bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	return len(s.lapsed) > 0
}

// noteResult records one delivery outcome and removes the subscription
// after PushMaxFailures consecutive failures (§3.5).
func (s *subStore) noteResult(id string, err error) {
	s.mu.Lock()
	sub, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	if err == nil {
		sub.failures = 0
		s.mu.Unlock()
		return
	}
	sub.failures++
	failures, callback := sub.failures, sub.Callback
	drop := failures >= PushMaxFailures
	if drop {
		delete(s.byID, id)
		delete(s.byCall, callback)
		// Not forgotten: a hub that was out of reach for a while is still
		// told when the PC comes back up (#142).
		if now := s.now(); now.Before(sub.ExpiresAt) {
			sub.ExpiresAt = now
		}
		sub.failures = 0
		s.lapsed[callback] = sub
	}
	s.mu.Unlock()

	// Bodies are never logged, only the destination and the error (§8).
	if drop {
		s.save()
		logx.Printf("ST push: %s (%s) removed after %d consecutive failures: %v", id, callback, failures, err)
		return
	}
	logx.Printf("ST push: delivery to %s (%s) failed (%d/%d): %v", id, callback, failures, PushMaxFailures, err)
}

// startSweeper launches the periodic expiry sweep once. Expiry is also
// applied on every access, so this only keeps a forgotten subscription
// from lingering in memory (and logs it at the right time).
func (s *subStore) startSweeper() {
	s.sweeper.Do(func() {
		go func() {
			for range time.Tick(subSweepEvery) {
				s.mu.Lock()
				s.sweepLocked(s.now())
				s.mu.Unlock()
			}
		}()
	})
}

// reset clears every subscription and counts the ids from sub-1 again.
func (s *subStore) reset() {
	s.mu.Lock()
	s.byID = map[string]*subscription{}
	s.byCall = map[string]*subscription{}
	s.lapsed = map[string]*subscription{}
	s.nextID = 0
	s.mu.Unlock()
}

// ---- persistence (#142) ----------------------------------------------------

// SavedSubscription is one subscription as the service keeps it across
// restarts: what the hub registered and until when. The failure count is
// not kept.
type SavedSubscription struct {
	ID            string    `json:"id"`
	Callback      string    `json:"callback"`
	DriverVersion string    `json:"driver_version,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// save writes the store through persist: the live subscriptions and the
// lapsed ones still within subRecallWindow, ordered by callback.
func (s *subStore) save() {
	if s.persist == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	now := s.now()
	s.mu.Lock()
	s.sweepLocked(now)
	out := make([]SavedSubscription, 0, len(s.byCall)+len(s.lapsed))
	for _, group := range []map[string]*subscription{s.byCall, s.lapsed} {
		for _, sub := range group {
			out = append(out, SavedSubscription{
				ID:            sub.ID,
				Callback:      sub.Callback,
				DriverVersion: sub.DriverVersion,
				ExpiresAt:     sub.ExpiresAt,
			})
		}
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b SavedSubscription) int { return strings.Compare(a.Callback, b.Callback) })
	s.persist(out)
}

// restore takes up saved subscriptions: an unexpired one is live again
// under its old id (the driver holds that id, and renews by callback), one
// that lapsed within subRecallWindow is recalled for the wake events, and
// anything older, or with a callback subscribe would not accept, is
// dropped. A callback already subscribed in this run wins. Reports how many
// of each it took.
func (s *subStore) restore(saved []SavedSubscription) (live, lapsed int) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var renumber []*subscription
	for _, rec := range saved {
		if !savedCallbackOK(rec.Callback) {
			continue
		}
		if _, ok := s.byCall[rec.Callback]; ok {
			continue
		}
		if _, ok := s.lapsed[rec.Callback]; ok {
			continue
		}
		if n, ok := subNumber(rec.ID); ok && n > s.nextID {
			s.nextID = n
		}
		sub := &subscription{
			ID:            rec.ID,
			Callback:      rec.Callback,
			DriverVersion: httpx.Truncate(rec.DriverVersion, 32),
			ExpiresAt:     rec.ExpiresAt,
		}
		switch {
		case now.Before(rec.ExpiresAt):
			s.byCall[sub.Callback] = sub
			if _, taken := s.byID[sub.ID]; taken || sub.ID == "" {
				renumber = append(renumber, sub)
				continue
			}
			s.byID[sub.ID] = sub
			live++
		case now.Sub(rec.ExpiresAt) < subRecallWindow:
			s.lapsed[sub.Callback] = sub
			lapsed++
		}
	}
	// An id that was missing or already taken (a hand-edited file) gets a
	// new one once the highest saved number is known.
	for _, sub := range renumber {
		s.nextID++
		sub.ID = "sub-" + strconv.Itoa(s.nextID)
		s.byID[sub.ID] = sub
		live++
	}
	if live > 0 {
		s.startSweeper()
	}
	return live, lapsed
}

// subNumber is n of "sub-n".
func subNumber(id string) (int, bool) {
	digits, ok := strings.CutPrefix(id, "sub-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// savedCallbackOK applies what validateCallback checks, except the source
// IP, to a callback read back from disk.
func savedCallbackOK(raw string) bool {
	if raw == "" || len(raw) > maxCallbackLen {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && callbackAddrAllowed(ip)
}

// RestoreSubscriptions takes up what Deps.Subscriptions saved (#142). The
// root calls it once at start, before the HTTP server and power.started.
func (s *Server) RestoreSubscriptions() {
	if s.d.Subscriptions == nil {
		return
	}
	live, lapsed := s.subs.restore(s.d.Subscriptions.LoadSubscriptions())
	if live+lapsed > 0 {
		logx.Printf("ST push: %d subscription(s) restored, %d lapsed one(s) kept for power.started", live, lapsed)
	}
}

// RestartSubscriptionIDs clears every subscription and counts the ids from
// sub-1 again (tests of the assembled service, which share one Server: ids
// otherwise count up for the life of the process).
func (s *Server) RestartSubscriptionIDs() { s.subs.reset() }

// Subscriptions is how many unexpired subscriptions there are.
func (s *Server) Subscriptions() int { return len(s.subs.active()) }

// callbackHost returns the host (without port) of a callback URL, or "".
func callbackHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ---- callback validation (§3.5, §8) ----------------------------------------

// validateCallback checks that raw is an http:// URL whose host is the IP
// the request came from, and that the address is one the LAN can own. It
// returns the normalised URL.
func validateCallback(raw, from string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("callback is required")
	}
	if len(raw) > maxCallbackLen {
		return "", fmt.Errorf("callback is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("callback is not a URL")
	}
	// Plain http only: the hub listener has no certificate, and https
	// would only invite a callback pointing somewhere else entirely.
	if u.Scheme != "http" {
		return "", fmt.Errorf("callback must be http://")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		// A name could resolve anywhere, and to something different next
		// time; §3.5 compares the host against the source IP.
		return "", fmt.Errorf("callback host must be an IP address")
	}
	src := net.ParseIP(from)
	if src == nil || !ip.Equal(src) {
		return "", fmt.Errorf("callback host must be the request source IP")
	}
	if !callbackAddrAllowed(ip) {
		return "", fmt.Errorf("callback host must be a private address")
	}
	return u.String(), nil
}

// callbackAddrAllowed reports whether ip is an address a hub on this LAN
// can legitimately have: RFC1918/ULA private, link-local, or loopback.
// Loopback needs no extra guard — the host has to equal the request source,
// so a loopback callback can only come from a loopback request.
func callbackAddrAllowed(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}

// clampTTL turns the requested ttl_seconds into a duration: 0 (absent)
// means the default, anything outside 60..3600 is rejected (§3.5).
func clampTTL(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return subDefaultTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < subMinTTL || ttl > subMaxTTL {
		return 0, fmt.Errorf("ttl_seconds must be between %d and %d",
			int(subMinTTL/time.Second), int(subMaxTTL/time.Second))
	}
	return ttl, nil
}

// ---- handlers (§3.5) -------------------------------------------------------

// SubscribeRequest is the POST /st/v1/subscribe body.
type SubscribeRequest struct {
	Callback      string `json:"callback"`
	TTLSeconds    int    `json:"ttl_seconds"`
	DriverVersion string `json:"driver_version"`
}

type subscribeResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
}

// handleSubscribe serves POST /st/v1/subscribe.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body SubscribeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := httpx.RemoteHost(r.RemoteAddr)
	callback, err := validateCallback(body.Callback, from)
	if err != nil {
		logx.Printf("ST push: subscribe from %s rejected: %v", from, err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ttl, err := clampTTL(body.TTLSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, renewed := s.subs.subscribe(callback, httpx.Truncate(strings.TrimSpace(body.DriverVersion), 32), ttl)
	verb := "subscribed"
	if renewed {
		verb = "renewed"
	}
	logx.Printf("ST push: %s %s → %s for %s (driver %s)", sub.ID, verb, sub.Callback, power.FormatDelay(ttl), sub.DriverVersion)
	httpx.WriteJSON(w, http.StatusOK, subscribeResponse{
		ID:        sub.ID,
		ExpiresAt: sub.ExpiresAt.Format(time.RFC3339),
	})
}

// handleUnsubscribe serves DELETE /st/v1/subscribe/{id}.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/st/v1/subscribe/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	removed := s.subs.remove(id)
	if removed {
		logx.Printf("ST push: %s removed by the hub", httpx.Truncate(id, 32))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

// ---- event → push mapping (§3.5) -------------------------------------------

// pushEventType returns the "<category>.<kind>" the hub should be told
// about, or ok=false when the event is not one of them. session.* is
// additionally gated on smartthings.expose_session, so turning the option
// off stops the events as well as the status block.
func pushEventType(ev notify.Event, cfg config.SmartThingsConfig) (string, bool) {
	switch ev.Category {
	case "power":
		switch ev.Kind {
		case "stopping", "started", "resumed":
			return ev.Key(), true
		}
	case "schedule", "remote":
		return ev.Key(), true
	case "system":
		switch ev.Kind {
		case "updated", "update_available":
			return ev.Key(), true
		}
	// activity.changed (#110, #123) is not gated on activity.enabled:
	// switching it off is itself a change the hub has to hear about, and
	// the block names watched programs only, never anything else running.
	case "display", "awake", "battery", "activity", "audio", "media", "privacy":
		if ev.Kind == "changed" {
			return ev.Key(), true
		}
	case "session":
		switch ev.Kind {
		case "locked", "unlocked":
			return ev.Key(), cfg.ExposeSession
		}
	}
	return "", false
}

// ---- delivery --------------------------------------------------------------

// pushJob is one event waiting to go out. The status is built at delivery
// time, not here, so a queued event still carries fresh state.
type pushJob struct {
	Type string
	At   time.Time
	Data map[string]string
	// fromCache builds the status from memory only (StoppingStatus).
	fromCache bool
	// wake goes to the lapsed subscriptions too, and is tried again later
	// when it fails (power.started, power.resumed; #142).
	wake bool
	// flushed marks a no-op job: the worker closes it once every job
	// queued before it has been delivered (see FlushPush).
	flushed chan struct{}
}

// pusher is the delivery queue and its worker.
type pusher struct {
	queue     chan pushJob
	workerOne sync.Once
	client    *http.Client
}

func (p *pusher) init() {
	p.queue = make(chan pushJob, pushQueueCap)
	p.client = &http.Client{Timeout: pushTimeout}
}

// PushTap is the notify.Bus raw tap: every event, before the category
// filter, aggregation, quiet hours and the throttle (§3.5).
//
// It runs on the emitting goroutine, so it hands the work to the delivery
// worker — except power.stopping, which is delivered inline. That event is
// emitted from the SCM stop handler and from the suspend broadcast, right
// before the PC stops answering, and blocking those paths for up to
// StoppingDeadline(reason) is the only way the hub learns the difference
// between "shutting down" and "fell off the network". Both call sites are
// prepared to wait; the stop continues afterwards either way. Its body is
// built from memory only (StoppingStatus), so the deadline is spent on the
// network and not on status lookups.
//
// The connection itself cannot be kept warm: the driver answers every push
// with "Connection: close" (edge/src/push.lua), so each push dials anew —
// a LAN connect, a few milliseconds.
func (s *Server) PushTap(ev notify.Event) {
	typ, ok := pushEventType(ev, s.d.Config().SmartThings)
	if !ok {
		return
	}
	wake := isWakeEvent(typ)
	if s.Subscriptions() == 0 && (!wake || !s.subs.anyLapsed()) {
		// The tap costs one mutex on a service nobody subscribed to.
		return
	}
	job := pushJob{Type: typ, At: ev.At, Data: ev.Fields, wake: wake}
	if typ == "power.stopping" {
		// The body comes from memory only (StoppingStatus): a status
		// build that had to rescan the adapters spent a second of the
		// deadline in PowerShell before the POST started, and the push
		// never reached the hub (2026-10-03).
		job.fromCache = true
		ctx, cancel := context.WithTimeout(context.Background(), StoppingDeadline(ev.Fields["reason"]))
		defer cancel()
		s.dispatch(ctx, job)
		return
	}
	s.push.workerOne.Do(func() { go s.pushWorker() })
	select {
	case s.push.queue <- job:
	default:
		logx.Printf("ST push: queue full, dropping %s", job.Type)
	}
}

// StoppingDeadline is how long the synchronous power.stopping delivery for
// reason may hold the stop (§3.5): PushAppStopDeadline for app_stop,
// PushStoppingDeadline for everything Windows itself is doing.
func StoppingDeadline(reason string) time.Duration {
	if reason == "app_stop" {
		return PushAppStopDeadline
	}
	return PushStoppingDeadline
}

// pushWorker delivers queued events one at a time, in emit order.
func (s *Server) pushWorker() {
	for job := range s.push.queue {
		if job.flushed != nil {
			close(job.flushed)
			continue
		}
		s.dispatch(context.Background(), job)
	}
}

// FlushPush waits until the delivery worker has sent everything queued so
// far, or until timeout; false when it did not drain in time (tests: a
// delivery left over from one test must not read state the next one is
// rewriting).
func (s *Server) FlushPush(timeout time.Duration) bool {
	s.push.workerOne.Do(func() { go s.pushWorker() })
	done := make(chan struct{})
	select {
	case s.push.queue <- pushJob{flushed: done}:
	case <-time.After(timeout):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Deliver sends one push of type typ to every live subscriber now (and,
// for power.started and power.resumed, to the lapsed ones) and waits for
// the first attempt at all of them (tests; PushTap does this for
// power.stopping).
func (s *Server) Deliver(ctx context.Context, typ string, at time.Time, data map[string]string) {
	s.dispatch(ctx, pushJob{Type: typ, At: at, Data: data, wake: isWakeEvent(typ)})
}

// dispatch renders the body once and posts it to every live subscriber in
// parallel, waiting for all of them (ctx bounds the whole thing for
// power.stopping). A wake event goes to the lapsed subscriptions too, and
// whoever it did not reach is tried again in the background
// (wakeRetryDelays) without holding up the events behind it.
func (s *Server) dispatch(ctx context.Context, job pushJob) {
	subs, lapsed := s.subs.active(), 0
	if job.wake {
		subs, lapsed = s.subs.wakeTargets()
	}
	if len(subs) == 0 {
		return
	}
	body, err := s.pushBody(job)
	if err != nil {
		logx.Printf("ST push: cannot encode %s: %v", job.Type, err)
		return
	}
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Add(1)
		go func(sub subscription, recalled bool) {
			defer wg.Done()
			err := s.post(ctx, sub.Callback, body)
			s.subs.noteResult(sub.ID, err)
			if recalled {
				// The only record that the hub was told: noteResult keeps
				// no count for a lapsed subscription.
				logx.Printf("ST push: %s to lapsed %s (%s): %s", job.Type, sub.ID, sub.Callback, outcome(err))
			}
			if err != nil && job.wake {
				s.retryWake(job.Type, sub, body)
			}
		}(sub, i >= len(subs)-lapsed)
	}
	wg.Wait()
}

// retryWake tries a wake event that did not reach sub again after each of
// wakeRetryDelays, in the background, until one attempt gets through. A
// failed retry is not counted against the subscription (the first attempt
// was); a success resets its count.
func (s *Server) retryWake(typ string, sub subscription, body []byte) {
	delays := wakeRetryDelays
	go func() {
		start := time.Now()
		for _, d := range delays {
			time.Sleep(d)
			if err := s.post(context.Background(), sub.Callback, body); err == nil {
				s.subs.noteResult(sub.ID, nil)
				logx.Printf("ST push: %s to %s (%s) delivered %s late", typ, sub.ID, sub.Callback,
					time.Since(start).Round(time.Second))
				return
			}
		}
		logx.Printf("ST push: %s to %s (%s) gave up after %d more attempt(s)", typ, sub.ID, sub.Callback, len(delays))
	}()
}

// outcome is "ok" or the error, for the log.
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// pushPayload is the §3.5 callback body. machine_id is repeated at the top
// level so a hub serving several PCs can route the event without parsing
// status.
type pushPayload struct {
	Protocol  int    `json:"protocol"`
	MachineID string `json:"machine_id"`
	Type      string `json:"type"`
	At        string `json:"at"`
	// Data is the event's fields (map[string]string), except for
	// activity.changed and privacy.changed: see pushBody.
	Data   any    `json:"data"`
	Status Status `json:"status"`
}

// pushBody renders one event, with the full §3.2 status attached so the
// driver needs no diff. The secret never appears in it (§8): the status
// only reports whether one is set, and any event field that happens to
// carry it is dropped.
//
// activity.changed is an event whose data is not its fields: it is the
// status activity block itself (§11, #123), taken from the same status
// document so the two can never disagree. privacy.changed (#143) carries
// the status privacy block the same way.
func (s *Server) pushBody(job pushJob) ([]byte, error) {
	cfg := s.d.Config()
	at := job.At
	if at.IsZero() {
		at = s.PushNow()
	}
	st := s.buildStatus(cfg, job.fromCache)
	var data any = pushData(job.Data, cfg.Secret)
	switch job.Type {
	case "activity.changed":
		data = st.Activity
	case "privacy.changed":
		// Likewise the status privacy block (#143); null once the
		// option is turned off.
		data = st.Privacy
	}
	return json.Marshal(pushPayload{
		Protocol:  Protocol,
		MachineID: s.d.Status.MachineID(),
		Type:      job.Type,
		At:        at.Format(time.RFC3339),
		Data:      data,
		Status:    st,
	})
}

// pushData copies the event fields, dropping any value equal to the secret
// so it cannot leak through a notification field.
func pushData(fields map[string]string, secret string) map[string]string {
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		if secret != "" && v == secret {
			continue
		}
		out[k] = v
	}
	return out
}

// post delivers body to callback: one POST with a 2s timeout and one retry
// (§3.5). ctx can cut both attempts short (power.stopping).
func (s *Server) post(ctx context.Context, callback string, body []byte) error {
	err := s.postOnce(ctx, callback, body)
	if err == nil || ctx.Err() != nil {
		return err
	}
	return s.postOnce(ctx, callback, body)
}

// postOnce is a single attempt.
func (s *Server) postOnce(ctx context.Context, callback string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callback, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "smartthings-pc-control/"+s.d.Version())
	resp, err := s.push.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback answered %s", resp.Status)
	}
	return nil
}
