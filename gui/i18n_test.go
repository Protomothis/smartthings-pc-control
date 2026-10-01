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
