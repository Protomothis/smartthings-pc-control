// Package sapi reads text aloud and lists the installed voices through the
// SAPI 5 automation object SAPI.SpVoice (#106, docs/design/media-notify.md
// §2). Both the user-action subcommand (speaking a PC notification) and the
// desktop app (the voice select of the 미디어·알림 section) use it; each call
// runs in the calling user's session, never in the service's session 0.
//
// Every call initialises COM on its own locked OS thread and releases it
// again, so callers need no COM setup of their own and may call from any
// goroutine.
package sapi

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// sFalse is the HRESULT CoInitializeEx returns when COM is already
// initialised on the thread; go-ole reports it as an error.
const sFalse = 1

// withCOM runs f on a locked OS thread inside a single-threaded apartment.
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != sFalse {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()
	return f()
}

// newVoice creates an SpVoice. The caller releases it.
func newVoice() (*ole.IDispatch, error) {
	unk, err := oleutil.CreateObject("SAPI.SpVoice")
	if err != nil {
		return nil, fmt.Errorf("SAPI.SpVoice is not available: %w", err)
	}
	defer unk.Release()
	disp, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, fmt.Errorf("SAPI.SpVoice: %w", err)
	}
	return disp, nil
}

// voiceToken is one installed voice: its token object (held until release)
// and its description, e.g. "Microsoft Heami Desktop - Korean".
type voiceToken struct {
	disp *ole.IDispatch
	name string
}

// listTokens returns every voice token of v. The caller releases them.
func listTokens(v *ole.IDispatch) ([]voiceToken, error) {
	res, err := oleutil.CallMethod(v, "GetVoices")
	if err != nil {
		return nil, fmt.Errorf("GetVoices: %w", err)
	}
	tokens := res.ToIDispatch()
	if tokens == nil {
		return nil, errors.New("GetVoices returned no collection")
	}
	defer tokens.Release()
	countV, err := oleutil.GetProperty(tokens, "Count")
	if err != nil {
		return nil, fmt.Errorf("voices Count: %w", err)
	}
	count, _ := countV.Value().(int32)
	var out []voiceToken
	for i := int32(0); i < count; i++ {
		itemV, err := oleutil.CallMethod(tokens, "Item", i)
		if err != nil {
			continue
		}
		item := itemV.ToIDispatch()
		if item == nil {
			continue
		}
		descV, err := oleutil.CallMethod(item, "GetDescription")
		if err != nil {
			item.Release()
			continue
		}
		out = append(out, voiceToken{disp: item, name: descV.ToString()})
		descV.Clear()
	}
	return out, nil
}

func releaseTokens(ts []voiceToken) {
	for _, t := range ts {
		t.disp.Release()
	}
}

// currentVoiceName is the description of the voice v speaks with now — the
// system default until a voice is set.
func currentVoiceName(v *ole.IDispatch) string {
	res, err := oleutil.GetProperty(v, "Voice")
	if err != nil {
		return ""
	}
	tok := res.ToIDispatch()
	if tok == nil {
		return ""
	}
	defer tok.Release()
	desc, err := oleutil.CallMethod(tok, "GetDescription")
	if err != nil {
		return ""
	}
	defer desc.Clear()
	return desc.ToString()
}

// MatchVoice picks the voice for want from names: a case-insensitive exact
// match first, else the first name containing want (so "Heami" finds
// "Microsoft Heami Desktop - Korean"). -1 when want is blank or nothing
// matches, which callers treat as "the system default".
func MatchVoice(names []string, want string) int {
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "" {
		return -1
	}
	for i, n := range names {
		if strings.ToLower(n) == w {
			return i
		}
	}
	for i, n := range names {
		if strings.Contains(strings.ToLower(n), w) {
			return i
		}
	}
	return -1
}

// Voices lists the installed voice descriptions in SAPI's order (the first
// is usually the system default).
func Voices() ([]string, error) {
	var names []string
	err := withCOM(func() error {
		v, err := newVoice()
		if err != nil {
			return err
		}
		defer v.Release()
		ts, err := listTokens(v)
		if err != nil {
			return err
		}
		defer releaseTokens(ts)
		for _, t := range ts {
			names = append(names, t.name)
		}
		return nil
	})
	return names, err
}

// Resolve reports which voice Speak(text, want) would use: the matching
// installed voice, or the system default (found false) when want is blank
// or matches nothing.
func Resolve(want string) (used string, found bool, err error) {
	err = withCOM(func() error {
		v, err := newVoice()
		if err != nil {
			return err
		}
		defer v.Release()
		used, found, err = pick(v, want)
		return err
	})
	return used, found, err
}

// pick selects want on v (when it matches) and returns the voice in force.
func pick(v *ole.IDispatch, want string) (string, bool, error) {
	if strings.TrimSpace(want) == "" {
		return currentVoiceName(v), false, nil
	}
	ts, err := listTokens(v)
	if err != nil {
		return "", false, err
	}
	defer releaseTokens(ts)
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.name
	}
	i := MatchVoice(names, want)
	if i < 0 {
		return currentVoiceName(v), false, nil
	}
	if _, err := oleutil.PutPropertyRef(v, "Voice", ts[i].disp); err != nil {
		return "", false, fmt.Errorf("set voice %q: %w", names[i], err)
	}
	return names[i], true, nil
}

// svsfIsNotXML is SpeechVoiceSpeakFlags.SVSFIsNotXML: the text is read as
// plain text, so markup in a notification ("<silence msec=…/>", "<volume>")
// is spoken literally instead of steering the engine.
const svsfIsNotXML = 16

// Speak reads text aloud with the voice want (the system default when it
// is blank or not installed) and returns when the engine is done. It
// blocks for as long as the text takes to say.
func Speak(text, want string) (used string, err error) {
	err = withCOM(func() error {
		v, err := newVoice()
		if err != nil {
			return err
		}
		defer v.Release()
		used, _, err = pick(v, want)
		if err != nil {
			return err
		}
		res, err := oleutil.CallMethod(v, "Speak", text, int32(svsfIsNotXML))
		if err != nil {
			return fmt.Errorf("Speak: %w", err)
		}
		res.Clear()
		return nil
	})
	return used, err
}
