package gui

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fmtVerb matches one fmt verb ("%d", "%s", "%02d", "%%").
var fmtVerb = regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

// Both locale files carry the same keys, and every text the same fmt verbs
// in the same order — a missing or extra %d would garble the message or
// print %!d(MISSING) in one language only.
func TestLocalesMatch(t *testing.T) {
	ko, en := messages[LangKo], messages[LangEn]
	if len(ko) == 0 || len(en) == 0 {
		t.Fatalf("empty locale: ko %d, en %d keys", len(ko), len(en))
	}
	for key := range ko {
		if _, ok := en[key]; !ok {
			t.Errorf("%s: in ko.json, not in en.json", key)
		}
	}
	for key := range en {
		if _, ok := ko[key]; !ok {
			t.Errorf("%s: in en.json, not in ko.json", key)
		}
	}
	for key, k := range ko {
		e, ok := en[key]
		if !ok {
			continue
		}
		if kv, ev := fmtVerb.FindAllString(k, -1), fmtVerb.FindAllString(e, -1); !slices.Equal(kv, ev) {
			t.Errorf("%s: fmt verbs ko %v, en %v", key, kv, ev)
		}
		if strings.TrimSpace(k) == "" || strings.TrimSpace(e) == "" {
			t.Errorf("%s: empty text", key)
		}
	}
}

// literalKey finds the keys the code looks up by literal: u.t("…") and
// T(lang, "…"). Keys built at run time ("media." + status) are left out.
var literalKey = regexp.MustCompile(`(?:\bu\.t\(|\bT\([A-Za-z.]+, )"([a-z0-9_.]+)"\)`)

// Every key the code asks for by literal exists; T would otherwise show
// the key itself.
func TestLocaleKeysUsedExist(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range literalKey.FindAllStringSubmatch(string(src), -1) {
			seen++
			if _, ok := messages[LangKo][m[1]]; !ok {
				t.Errorf("%s: %q is not in the locales", f, m[1])
			}
		}
	}
	if seen < 100 {
		t.Errorf("found only %d literal lookups; is the pattern stale?", seen)
	}
}

// The keys built at run time, which TestLocaleKeysUsedExist cannot see:
// each family exists in both languages, with the verb the code fills in.
func TestRuntimeLocaleKeysExist(t *testing.T) {
	want := map[string]string{} // key -> a verb it must carry, or ""
	for _, s := range []string{"playing", "paused", "stopped"} {
		want["media."+s] = "" // media_card.go: "media." + status
	}
	for _, k := range []string{"st.rel.now", "st.search.fw.ok", "st.search.fw.missing"} {
		want[k] = ""
	}
	for _, k := range []string{"st.rel.sec", "st.rel.min", "st.rel.hour", "st.rel.day"} {
		want[k] = "%d"
	}
	for _, k := range []string{"slot", "name", "namelong", "type", "path", "quote", "args", "url", "urlargs", "abs", "exe", "script", "dup"} {
		want["presets.err."+k] = "%d" // the row's slot
	}
	for _, code := range []string{"no_user_session", "notify_disabled", "rate_limited", "no_such_preset", "timeout", "unsupported", "service_too_old"} {
		key := actionErrorKey(&actionError{Code: code})
		if key == "" {
			t.Errorf("action error %s has no text", code)
			continue
		}
		want[key] = ""
	}
	for key, verb := range want {
		for _, l := range []Lang{LangKo, LangEn} {
			s, ok := messages[l][key]
			if !ok || !strings.Contains(s, verb) {
				t.Errorf("%s (%s) = %q: missing, or without %s", key, l, s, verb)
			}
		}
	}
}
