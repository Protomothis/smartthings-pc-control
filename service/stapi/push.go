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
// Both go through auth like the rest of /st/v1. Subscriptions live in
// memory only: after a service restart the driver's next poll fails and it
// subscribes again.
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
	// delivery, retry included, before the stop proceeds (§3.5).
	PushStoppingDeadline = 1500 * time.Millisecond
	// PushMaxFailures removes a subscription after this many consecutive
	// failed deliveries (§3.5).
	PushMaxFailures = 3
	// pushQueueCap bounds the asynchronous delivery queue. Events are
	// dropped (and logged) rather than blocking the emitter when a hub is
	// slow enough to fill it.
	pushQueueCap = 64
	// maxCallbackLen is a sanity bound on the callback URL.
	maxCallbackLen = 512
)

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

// subStore holds the live subscriptions. It is the only mutable push
// state.
type subStore struct {
	mu     sync.Mutex
	now    func() time.Time
	byID   map[string]*subscription
	byCall map[string]*subscription
	nextID int
	// sweeper is started on the first subscribe.
	sweeper sync.Once
}

func newSubStore(now func() time.Time) *subStore {
	return &subStore{now: now, byID: map[string]*subscription{}, byCall: map[string]*subscription{}}
}

// sweepLocked drops everything that has expired. The caller holds mu.
func (s *subStore) sweepLocked(now time.Time) {
	for id, sub := range s.byID {
		if !now.Before(sub.ExpiresAt) {
			delete(s.byID, id)
			delete(s.byCall, sub.Callback)
			logx.Printf("ST push: subscription %s (%s) expired", id, sub.Callback)
		}
	}
}

// subscribe registers or renews callback for ttl and reports whether this
// renewed an existing subscription.
func (s *subStore) subscribe(callback, driverVersion string, ttl time.Duration) (subscription, bool) {
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
	// A hub whose driver restarted comes back on a new ephemeral port; the
	// listener behind its old callback is gone. Keeping that subscription
	// only buys 2s timeouts and retries on every event until it fails out,
	// so a new callback from the same host replaces the host's old ones.
	if host := callbackHost(callback); host != "" {
		for id, old := range s.byID {
			if old.Callback != callback && callbackHost(old.Callback) == host {
				delete(s.byID, id)
				delete(s.byCall, old.Callback)
				logx.Printf("ST push: %s (%s) replaced by a new subscription from %s", id, old.Callback, host)
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
func (s *subStore) remove(id string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	sub, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	delete(s.byCall, sub.Callback)
	return true
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
	}
	s.mu.Unlock()

	// Bodies are never logged, only the destination and the error (§8).
	if drop {
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
	s.nextID = 0
	s.mu.Unlock()
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
	case "display", "awake", "battery", "activity", "audio", "media":
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
// PushStoppingDeadline is the only way the hub learns the difference
// between "shutting down" and "fell off the network". Both call sites are
// prepared to wait; the stop continues afterwards either way.
func (s *Server) PushTap(ev notify.Event) {
	typ, ok := pushEventType(ev, s.d.Config().SmartThings)
	if !ok {
		return
	}
	if s.Subscriptions() == 0 {
		// The tap costs one mutex on a service nobody subscribed to.
		return
	}
	job := pushJob{Type: typ, At: ev.At, Data: ev.Fields}
	if typ == "power.stopping" {
		ctx, cancel := context.WithTimeout(context.Background(), PushStoppingDeadline)
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

// Deliver sends one push of type typ to every live subscriber now and
// waits for all of them (tests; PushTap does this for power.stopping).
func (s *Server) Deliver(ctx context.Context, typ string, at time.Time, data map[string]string) {
	s.dispatch(ctx, pushJob{Type: typ, At: at, Data: data})
}

// dispatch renders the body once and posts it to every live subscriber in
// parallel, waiting for all of them (ctx bounds the whole thing for
// power.stopping).
func (s *Server) dispatch(ctx context.Context, job pushJob) {
	subs := s.subs.active()
	if len(subs) == 0 {
		return
	}
	body, err := s.pushBody(job)
	if err != nil {
		logx.Printf("ST push: cannot encode %s: %v", job.Type, err)
		return
	}
	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Add(1)
		go func(sub subscription) {
			defer wg.Done()
			s.subs.noteResult(sub.ID, s.post(ctx, sub.Callback, body))
		}(sub)
	}
	wg.Wait()
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
	// activity.changed: see pushBody.
	Data   any    `json:"data"`
	Status Status `json:"status"`
}

// pushBody renders one event, with the full §3.2 status attached so the
// driver needs no diff. The secret never appears in it (§8): the status
// only reports whether one is set, and any event field that happens to
// carry it is dropped.
//
// activity.changed is the one event whose data is not its fields: it is
// the status activity block itself (§11, #123), taken from the same status
// document so the two can never disagree.
func (s *Server) pushBody(job pushJob) ([]byte, error) {
	cfg := s.d.Config()
	at := job.At
	if at.IsZero() {
		at = s.PushNow()
	}
	st := s.BuildStatus(cfg)
	var data any = pushData(job.Data, cfg.Secret)
	if job.Type == "activity.changed" {
		data = st.Activity
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
