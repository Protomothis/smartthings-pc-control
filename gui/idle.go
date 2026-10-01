package gui

// Session heartbeat (#77, #104, #117). The service runs in session 0 and
// cannot measure how long the user has been away — WTSINFOEXW.LastInputTime
// is pinned near logon on Windows 10/11 console sessions — nor see the
// volume the user just changed with the keyboard, nor what is playing. This
// app does run in the interactive session, so every 30s it samples
// GetLastInputInfo, the default playback device and the system media
// session and posts them to the service, which publishes them in GET
// /st/v1/status. Between those posts it looks at the audio and the media
// session every 3s and posts just the part that changed, so a new track or
// a volume key reaches the hub and the media card within seconds.

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// idleHeartbeatInterval is how often the samples are taken and posted.
// The service treats an idle sample as stale after 90s, so two posts may
// be lost (a suspended machine, a restarting service) before the hub sees
// null.
const idleHeartbeatInterval = 30 * time.Second

// mediaWatchInterval is how often the audio and the media session are
// checked for a change between heartbeats (#117).
const mediaWatchInterval = 3 * time.Second

var (
	modUser32            = windows.NewLazySystemDLL("user32.dll")
	modKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = modUser32.NewProc("GetLastInputInfo")
	procGetTickCount     = modKernel32.NewProc("GetTickCount")
)

// lastInputInfo is LASTINPUTINFO: { UINT cbSize; DWORD dwTime; }, where
// dwTime is the GetTickCount value of the last input event.
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// idleSecondsFrom converts a last-input tick and the current tick into
// seconds. Both are 32-bit millisecond counters that wrap every ~49.7 days;
// subtracting them as uint32 wraps the same way, so a wrap between the two
// samples still yields the real elapsed time instead of ~49 days.
func idleSecondsFrom(lastInput, now uint32) int64 {
	return int64((now - lastInput) / 1000)
}

// systemIdleSeconds reports how long the interactive session has had no
// keyboard or mouse input; ok is false when the call fails (which happens
// on the lock screen under some policies).
func systemIdleSeconds() (int64, bool) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r1, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r1 == 0 {
		return 0, false
	}
	tick, _, _ := procGetTickCount.Call()
	return idleSecondsFrom(info.dwTime, uint32(tick)), true
}

// heartbeatSources are the samplers and switches behind buildHeartbeat,
// replaced by the tests.
var (
	heartbeatExposeSession = localExposeSession
	heartbeatMediaEnabled  = localMediaEnabled
	heartbeatIdle          = systemIdleSeconds
	// heartbeatAudio is the same getter `user-action audio get` uses, run
	// in-process: this app already is in the user's session.
	heartbeatAudio = useraction.ReadAudio
	// heartbeatNowPlaying reads the system media session in-process
	// (#117), and heartbeatShareNowPlaying is the media.now_playing opt-in.
	heartbeatNowPlaying      = useraction.ReadNowPlaying
	heartbeatShareNowPlaying = localNowPlaying
	heartbeatNow             = time.Now
)

// buildHeartbeat samples what the switches allow; ok is false when there
// is nothing to send.
//
// The switches are re-read from config.json on every tick — the same file
// localSecret() uses, rewritten by the service whenever a setting changes
// — so turning smartthings.expose_session or media.enabled off stops that
// part of the post within one interval, without a restart or an API round
// trip. The two are independent: the audio block serves the volume
// commands whether or not the session block is published.
func buildHeartbeat() (Heartbeat, bool) {
	var hb Heartbeat
	if heartbeatExposeSession() {
		if idle, ok := heartbeatIdle(); ok {
			hb.IdleSeconds = &idle
		}
	}
	hb.Audio, hb.Media = sampleMediaBlocks()
	return hb, hb.IdleSeconds != nil || hb.Audio != nil || hb.Media != nil
}

// sampleMediaBlocks reads the audio and media blocks media.enabled allows.
// A PC without a playback device (or a Core Audio hiccup) simply sends no
// audio block, and one without the media session API no media block; the
// service keeps its last value.
func sampleMediaBlocks() (*HeartbeatAudio, *HeartbeatMedia) {
	if !heartbeatMediaEnabled() {
		return nil, nil
	}
	at := heartbeatNow().Format(time.RFC3339Nano)
	var audio *HeartbeatAudio
	if a, err := heartbeatAudio(); err == nil {
		audio = &HeartbeatAudio{Volume: a.Volume, Muted: a.Muted, Device: a.Device, SampledAt: at}
	}
	var media *HeartbeatMedia
	if np, err := heartbeatNowPlaying(); err == nil {
		media = heartbeatMediaBlock(np, heartbeatShareNowPlaying(), at)
	}
	return audio, media
}

// heartbeatMediaBlock is the media block for np: the status always (it is
// what the play/pause buttons follow), the track and the app only with the
// media.now_playing opt-in. Nothing else — no file path, URL or artwork —
// is ever read, let alone sent.
func heartbeatMediaBlock(np useraction.NowPlaying, share bool, sampledAt string) *HeartbeatMedia {
	if !share {
		np = np.StatusOnly()
	}
	return &HeartbeatMedia{Status: np.Status, Title: np.Title, Artist: np.Artist, Album: np.Album, App: np.App, SampledAt: sampledAt}
}

// heartbeatSent remembers the audio and media blocks last delivered, so the
// 3s check posts only what changed. Both tickers update it.
type heartbeatSent struct {
	mu    sync.Mutex
	audio *HeartbeatAudio
	media *HeartbeatMedia
}

// changes returns the blocks that differ from the ones last sent (the
// sample time aside); ok is false when nothing changed.
func (s *heartbeatSent) changes(audio *HeartbeatAudio, media *HeartbeatMedia) (Heartbeat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hb Heartbeat
	if audio != nil && !sameAudioBlock(audio, s.audio) {
		hb.Audio = audio
	}
	if media != nil && !sameMediaBlock(media, s.media) {
		hb.Media = media
	}
	return hb, hb.Audio != nil || hb.Media != nil
}

// delivered records what a successful post carried.
func (s *heartbeatSent) delivered(hb Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hb.Audio != nil {
		s.audio = hb.Audio
	}
	if hb.Media != nil {
		s.media = hb.Media
	}
}

func sameAudioBlock(a, b *HeartbeatAudio) bool {
	return b != nil && a.Volume == b.Volume && a.Muted == b.Muted && a.Device == b.Device
}

func sameMediaBlock(a, b *HeartbeatMedia) bool {
	if b == nil {
		return false
	}
	x, y := *a, *b
	x.SampledAt, y.SampledAt = "", ""
	return x == y
}

// sendIdleHeartbeat posts one heartbeat, or does nothing at all.
//
// Every failure is silent: the service being down, mid-restart or simply
// not listening yet is the normal state of affairs for a tray app, and the
// next tick retries anyway.
func (u *ui) sendIdleHeartbeat() {
	hb, ok := buildHeartbeat()
	if !ok {
		return
	}
	u.postHeartbeat(hb)
}

// watchMediaChanges is the 3s check (#117): sample the audio and the media
// session and post only the blocks that changed since the last delivery.
func (u *ui) watchMediaChanges() {
	hb, ok := u.hbSent.changes(sampleMediaBlocks())
	if !ok {
		return
	}
	u.postHeartbeat(hb)
}

// postHeartbeat delivers hb and records what arrived.
func (u *ui) postHeartbeat(hb Heartbeat) {
	hb.SessionID = heartbeatSessionID()
	ignored, err := u.client.SessionHeartbeat(hb)
	if errors.Is(err, errUnauthorized) {
		// A secret is configured and this client has no session yet (the
		// user never opened the window, or the service restarted).
		// config.json holds the secret, so log in the way the toast handler
		// does and retry once.
		if secret := localSecret(); secret != "" && u.client.Login(secret) == nil {
			ignored, err = u.client.SessionHeartbeat(hb)
		}
	}
	if err == nil {
		// An ignored post counts as delivered too: the 3s check then posts
		// only on a change rather than every 3s, and the 30s heartbeat
		// sends everything anyway once this session becomes the target.
		u.hbSent.delivered(hb)
		noteHeartbeatReply(ignored)
	}
}

var (
	// heartbeatSessionID is the Windows session this process runs in,
	// read once: it never changes for a process.
	heartbeatSessionID = sync.OnceValue(currentSessionID)
	// heartbeatLog writes gui.log; replaced by the tests.
	heartbeatLog = func(format string, args ...any) { guiLog("heartbeat", format, args...) }
	// heartbeatIgnored is whether the last delivered post was ignored.
	heartbeatIgnored atomic.Bool
)

// currentSessionID is ProcessIdToSessionId for this process, 0 when it
// fails — the field is then left out and the service believes the post.
func currentSessionID() uint32 {
	var id uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id) != nil {
		return 0
	}
	return id
}

// noteHeartbeatReply logs when the service starts or stops ignoring this
// app's heartbeats: it acts on another session (a locked console next to
// this RDP session, say), so the volume, media and idle time it reports
// come from there. One line per change, not one per post.
func noteHeartbeatReply(ignored bool) {
	if heartbeatIgnored.Swap(ignored) == ignored {
		return
	}
	if ignored {
		heartbeatLog("service ignores heartbeats from session %d: its commands act on another session", heartbeatSessionID())
	} else {
		heartbeatLog("service accepts heartbeats from session %d again", heartbeatSessionID())
	}
}
