package gui

import (
	"embed"
	"encoding/json"
	"strings"

	"fyne.io/fyne/v2/lang"
)

type Lang string

const (
	LangKo Lang = "ko"
	LangEn Lang = "en"
)

// languages are the shipped translations, each locales/<lang>.json.
var languages = []Lang{LangKo, LangEn}

// systemLang returns the OS display language, mapped to a supported Lang.
func systemLang() Lang {
	if strings.HasPrefix(strings.ToLower(lang.SystemLocale().String()), "ko") {
		return LangKo
	}
	return LangEn
}

// The texts live in locales/ko.json and locales/en.json, one flat object
// of key → text each, embedded in the exe. Both files carry the same keys
// with the same fmt verbs in the same order (i18n_test.go checks it).
//
//go:embed locales/*.json
var localeFiles embed.FS

// messages is lang → key → text, read from the embedded files at start.
var messages = loadMessages()

func loadMessages() map[Lang]map[string]string {
	out := make(map[Lang]map[string]string, len(languages))
	for _, l := range languages {
		data, err := localeFiles.ReadFile("locales/" + string(l) + ".json")
		if err != nil {
			panic("gui: missing locale " + string(l) + ": " + err.Error())
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			panic("gui: bad locale " + string(l) + ": " + err.Error())
		}
		out[l] = m
	}
	return out
}

// T returns the message for key in the given language, or the key itself
// when there is none.
func T(l Lang, key string) string {
	if s, ok := messages[l][key]; ok {
		return s
	}
	return key
}
