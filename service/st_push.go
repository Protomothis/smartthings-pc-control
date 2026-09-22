package service

// Hub push subscriptions, /st/v1/subscribe (docs/design/edge-driver.md
// §4.5, issue #68). The Edge driver opens a listener on the hub, subscribes
// to it, and renews at 80% of the TTL; the service posts every device-state
// event to that callback with the full §4.2 status attached, so the driver
// updates without polling and without diffing.
//
//	POST   /st/v1/subscribe       {callback, ttl_seconds, driver_version}
//	DELETE /st/v1/subscribe/{id}
//
// Both go through stAuth like the rest of /st/v1. Subscriptions live in
// memory only: after a service restart the driver's next poll fails and it
// subscribes again.
//
// Events arrive on a raw tap of the notification bus (notify.Bus.Tap), not
// as an ordinary Sink, because §4.5 is explicit that the notification
// category filter and quiet hours must not apply — device state is not a
// notification. Delivery is handed to a worker goroutine so an emitting
// path never waits on the network; power.stopping is the exception and is
// delivered inline, see stPushTap.

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

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

const (
	// TTL bounds and default from §4.5.
	stSubMinTTL     = 60 * time.Second
	stSubMaxTTL     = 3600 * time.Second
	stSubDefaultTTL = 600 * time.Second
	// stSubSweepEvery is the background expiry sweep; every access sweeps
	// too, so this only matters while nothing happens.
	stSubSweepEvery = time.Minute
	// stPushTimeout bounds one callback POST (§4.5).
	stPushTimeout = 2 * time.Second
	// stPushStoppingDeadline bounds the whole synchronous power.stopping
	// delivery, retry included, before the stop proceeds (§4.5).
	stPushStoppingDeadline = 1500 * time.Millisecond
	// stPushMaxFailures removes a subscription after this many consecutive
	// failed deliveries (§4.5).
	stPushMaxFailures = 3
	// stPushQueueCap bounds the asynchronous delivery queue. Events are
	// dropped (and logged) rather than blocking the emitter when a hub is
	// slow enough to fill it.
	stPushQueueCap = 64
	// stMaxCallbackLen is a sanity bound on the callback URL.
	stMaxCallbackLen = 512
)

// ---- subscription store ----------------------------------------------------

// stSubscription is one hub listener. Callback is the key: a driver that
// subscribes again for the same URL renews rather than piling up (§4.5).
type stSubscription struct {
	ID            string
	Callback      string
	DriverVersion string
	ExpiresAt     time.Time
	// failures counts consecutive delivery failures; a success resets it.
	failures int
}

// stSubStore holds the live subscriptions. It is the only mutable push
// state and is reset between tests by stPushReset.
type stSubStore struct {
	mu     sync.Mutex
	byID   map[string]*stSubscription
	byCall map[string]*stSubscription
	nextID int
	// sweeper is started on the first subscribe.
	sweeper sync.Once
}

var stSubs = &stSubStore{byID: map[string]*stSubscription{}, byCall: map[string]*stSubscription{}}

// stPushNow is time.Now, replaced by tests that drive expiry.
var stPushNow = time.Now

// sweepLocked drops everything that has expired. The caller holds mu.
func (s *stSubStore) sweepLocked(now time.Time) {
	for id, sub := range s.byID {
		if !now.Before(sub.ExpiresAt) {
			delete(s.byID, id)
			delete(s.byCall, sub.Callback)
			logMsg("ST push: subscription %s (%s) expired", id, sub.Callback)
		}
	}
}

// subscribe registers or renews callback for ttl and reports whether this
// renewed an existing subscription.
func (s *stSubStore) subscribe(callback, driverVersion string, ttl time.Duration) (stSubscription, bool) {
	now := stPushNow()
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
	if host := stCallbackHost(callback); host != "" {
		for id, old := range s.byID {
			if old.Callback != callback && stCallbackHost(old.Callback) == host {
				delete(s.byID, id)
				delete(s.byCall, old.Callback)
				logMsg("ST push: %s (%s) replaced by a new subscription from %s", id, old.Callback, host)
			}
		}
	}
	s.nextID++
	sub := &stSubscription{
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
func (s *stSubStore) remove(id string) bool {
	now := stPushNow()
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
func (s *stSubStore) active() []stSubscription {
	now := stPushNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	out := make([]stSubscription, 0, len(s.byID))
	for _, sub := range s.byID {
		out = append(out, *sub)
	}
	return out
}

// noteResult records one delivery outcome and removes the subscription
// after stPushMaxFailures consecutive failures (§4.5).
func (s *stSubStore) noteResult(id string, err error) {
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
	drop := failures >= stPushMaxFailures
	if drop {
		delete(s.byID, id)
		delete(s.byCall, callback)
	}
	s.mu.Unlock()

	// Bodies are never logged, only the destination and the error (§8).
	if drop {
		logMsg("ST push: %s (%s) removed after %d consecutive failures: %v", id, callback, failures, err)
		return
	}
	logMsg("ST push: delivery to %s (%s) failed (%d/%d): %v", id, callback, failures, stPushMaxFailures, err)
}

// startSweeper launches the periodic expiry sweep once. Expiry is also
// applied on every access, so this only keeps a forgotten subscription
// from lingering in memory (and logs it at the right time).
func (s *stSubStore) startSweeper() {
	s.sweeper.Do(func() {
		go func() {
			for range time.Tick(stSubSweepEvery) {
				s.mu.Lock()
				s.sweepLocked(stPushNow())
				s.mu.Unlock()
			}
		}()
	})
}

// stPushReset clears every subscription (tests, and a config reload that
// changes the secret).
func stPushReset() {
	stSubs.mu.Lock()
	stSubs.byID = map[string]*stSubscription{}
	stSubs.byCall = map[string]*stSubscription{}
	stSubs.mu.Unlock()
}

// stCallbackHost returns the host (without port) of a callback URL, or "".
func stCallbackHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ---- callback validation (§4.5, §8) ----------------------------------------

// stValidateCallback checks that raw is an http:// URL whose host is the
// IP the request came from, and that the address is one the LAN can own.
// It returns the normalised URL.
func stValidateCallback(raw, from string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("callback is required")
	}
	if len(raw) > stMaxCallbackLen {
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
		// time; §4.5 compares the host against the source IP.
		return "", fmt.Errorf("callback host must be an IP address")
	}
	src := net.ParseIP(from)
	if src == nil || !ip.Equal(src) {
		return "", fmt.Errorf("callback host must be the request source IP")
	}
	if !stCallbackAddrAllowed(ip) {
		return "", fmt.Errorf("callback host must be a private address")
	}
	return u.String(), nil
}

// stCallbackAddrAllowed reports whether ip is an address a hub on this LAN
// can legitimately have: RFC1918/ULA private, link-local, or loopback.
// Loopback needs no extra guard — the host has to equal the request source,
// so a loopback callback can only come from a loopback request.
func stCallbackAddrAllowed(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}

// stClampTTL turns the requested ttl_seconds into a duration: 0 (absent)
// means the default, anything outside 60..3600 is rejected (§4.5).
func stClampTTL(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return stSubDefaultTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < stSubMinTTL || ttl > stSubMaxTTL {
		return 0, fmt.Errorf("ttl_seconds must be between %d and %d",
			int(stSubMinTTL/time.Second), int(stSubMaxTTL/time.Second))
	}
	return ttl, nil
}

// ---- handlers (§4.5) -------------------------------------------------------

type stSubscribeRequest struct {
	Callback      string `json:"callback"`
	TTLSeconds    int    `json:"ttl_seconds"`
	DriverVersion string `json:"driver_version"`
}

type stSubscribeResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
}

// handleSTSubscribe serves POST /st/v1/subscribe.
func handleSTSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body stSubscribeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, stMaxBody)).Decode(&body); err != nil {
		stError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	from := remoteHost(r.RemoteAddr)
	callback, err := stValidateCallback(body.Callback, from)
	if err != nil {
		logMsg("ST push: subscribe from %s rejected: %v", from, err)
		stError(w, http.StatusBadRequest, err.Error())
		return
	}
	ttl, err := stClampTTL(body.TTLSeconds)
	if err != nil {
		stError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, renewed := stSubs.subscribe(callback, truncate(strings.TrimSpace(body.DriverVersion), 32), ttl)
	verb := "subscribed"
	if renewed {
		verb = "renewed"
	}
	logMsg("ST push: %s %s → %s for %s (driver %s)", sub.ID, verb, sub.Callback, formatDelay(ttl), sub.DriverVersion)
	writeJSON(w, http.StatusOK, stSubscribeResponse{
		ID:        sub.ID,
		ExpiresAt: sub.ExpiresAt.Format(time.RFC3339),
	})
}

// handleSTUnsubscribe serves DELETE /st/v1/subscribe/{id}.
func handleSTUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		stError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/st/v1/subscribe/")
	if id == "" || strings.Contains(id, "/") {
		stError(w, http.StatusNotFound, "not found")
		return
	}
	removed := stSubs.remove(id)
	if removed {
		logMsg("ST push: %s removed by the hub", truncate(id, 32))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

// registerSTPushRoutes mounts the §4.5 subscription endpoints, wrapped in
// the same auth/rate limit as the rest of /st/v1. stHandler calls it.
func registerSTPushRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/st/v1/subscribe", stAuth(handleSTSubscribe))
	mux.HandleFunc("/st/v1/subscribe/", stAuth(handleSTUnsubscribe))
}

// ---- event → push mapping (§4.5) -------------------------------------------

// stPushEventType returns the "<category>.<kind>" the hub should be told
// about, or ok=false when the event is not one of them. session.* is
// additionally gated on smartthings.expose_session, so turning the option
// off stops the events as well as the status block.
func stPushEventType(ev notify.Event, cfg SmartThingsConfig) (string, bool) {
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
	case "display":
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

// stPushJob is one event waiting to go out. The status is built at
// delivery time, not here, so a queued event still carries fresh state.
type stPushJob struct {
	Type string
	At   time.Time
	Data map[string]string
}

var (
	stPushQueue     = make(chan stPushJob, stPushQueueCap)
	stPushWorkerOne sync.Once
	stPushClient    = &http.Client{Timeout: stPushTimeout}
)

// stPushTap is the notify.Bus raw tap: every event, before the category
// filter, aggregation, quiet hours and the throttle (§4.5).
//
// It runs on the emitting goroutine, so it hands the work to the delivery
// worker — except power.stopping, which is delivered inline. That event is
// emitted from the SCM stop handler and from the suspend broadcast, right
// before the PC stops answering, and blocking those paths for up to
// stPushStoppingDeadline is the only way the hub learns the difference
// between "shutting down" and "fell off the network". Both call sites are
// prepared to wait; the stop continues afterwards either way.
func stPushTap(ev notify.Event) {
	typ, ok := stPushEventType(ev, getConfig().SmartThings)
	if !ok {
		return
	}
	if !stPushAnySubscribers() {
		return
	}
	job := stPushJob{Type: typ, At: ev.At, Data: ev.Fields}
	if typ == "power.stopping" {
		ctx, cancel := context.WithTimeout(context.Background(), stPushStoppingDeadline)
		defer cancel()
		stPushDispatch(ctx, job)
		return
	}
	stPushWorkerOne.Do(func() { go stPushWorker() })
	select {
	case stPushQueue <- job:
	default:
		logMsg("ST push: queue full, dropping %s", job.Type)
	}
}

// stPushAnySubscribers reports whether anything is listening, so the tap
// costs one mutex on a service nobody subscribed to.
func stPushAnySubscribers() bool {
	return len(stSubs.active()) > 0
}

// stPushWorker delivers queued events one at a time, in emit order.
func stPushWorker() {
	for job := range stPushQueue {
		stPushDispatch(context.Background(), job)
	}
}

// stPushDispatch renders the body once and posts it to every live
// subscriber in parallel, waiting for all of them (ctx bounds the whole
// thing for power.stopping).
func stPushDispatch(ctx context.Context, job stPushJob) {
	subs := stSubs.active()
	if len(subs) == 0 {
		return
	}
	body, err := stPushBody(job)
	if err != nil {
		logMsg("ST push: cannot encode %s: %v", job.Type, err)
		return
	}
	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Add(1)
		go func(sub stSubscription) {
			defer wg.Done()
			stSubs.noteResult(sub.ID, stPushPost(ctx, sub.Callback, body))
		}(sub)
	}
	wg.Wait()
}

// stPushPayload is the §4.5 callback body. machine_id is repeated at the
// top level so a hub serving several PCs can route the event without
// parsing status.
type stPushPayload struct {
	Protocol  int               `json:"protocol"`
	MachineID string            `json:"machine_id"`
	Type      string            `json:"type"`
	At        string            `json:"at"`
	Data      map[string]string `json:"data"`
	Status    stStatusResponse  `json:"status"`
}

// stPushBody renders one event, with the full §4.2 status attached so the
// driver needs no diff. The secret never appears in it (§8): the status
// only reports whether one is set, and any event field that happens to
// carry it is dropped.
func stPushBody(job stPushJob) ([]byte, error) {
	cfg := getConfig()
	at := job.At
	if at.IsZero() {
		at = stPushNow()
	}
	return json.Marshal(stPushPayload{
		Protocol:  stProtocol,
		MachineID: machineID(),
		Type:      job.Type,
		At:        at.Format(time.RFC3339),
		Data:      stPushData(job.Data, cfg.Secret),
		Status:    buildSTStatus(cfg),
	})
}

// stPushData copies the event fields, dropping any value equal to the
// secret so it cannot leak through a notification field.
func stPushData(fields map[string]string, secret string) map[string]string {
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		if secret != "" && v == secret {
			continue
		}
		out[k] = v
	}
	return out
}

// stPushPost delivers body to callback: one POST with a 2s timeout and one
// retry (§4.5). ctx can cut both attempts short (power.stopping).
func stPushPost(ctx context.Context, callback string, body []byte) error {
	err := stPushPostOnce(ctx, callback, body)
	if err == nil || ctx.Err() != nil {
		return err
	}
	return stPushPostOnce(ctx, callback, body)
}

// stPushPostOnce is a single attempt.
func stPushPostOnce(ctx context.Context, callback string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callback, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "smartthings-pc-control/"+Version)
	resp, err := stPushClient.Do(req)
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
