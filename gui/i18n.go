package gui

import (
	"embed"
	"encoding/json"
	"io/fs"
)

type Lang string

const (
	LangKo Lang = "ko"
	LangEn Lang = "en"
)

// languages are the shipped translations, each locales/<lang>.json.
var languages = []Lang{LangKo, LangEn}

// The texts live in locales/ko.json and locales/en.json, one flat object
// of key → text each, embedded in the exe. Both languages carry the same
// keys with the same fmt verbs in the same order (i18n_test.go checks it).
//
// A screen of the Gio window (package ui, #165) keeps its new texts in a
// file of its own, locales/<lang>/<page>.json, keys starting "ui.<page>.",
// so screens written side by side never edit the same file. Those files are
// merged into the language's set; a key defined twice is a startup panic.
//
//go:embed locales
var localeFiles embed.FS

// messages is lang → key → text, read from the embedded files at start.
var messages = loadMessages(localeFiles)

func loadMessages(files fs.FS) map[Lang]map[string]string {
	out := make(map[Lang]map[string]string, len(languages))
	for _, l := range languages {
		paths, _ := fs.Glob(files, "locales/"+string(l)+"/*.json")
		paths = append([]string{"locales/" + string(l) + ".json"}, paths...)
		m := map[string]string{}
		for _, p := range paths {
			data, err := fs.ReadFile(files, p)
			if err != nil {
				panic("gui: missing locale " + p + ": " + err.Error())
			}
			part := map[string]string{}
			if err := json.Unmarshal(data, &part); err != nil {
				panic("gui: bad locale " + p + ": " + err.Error())
			}
			for k, v := range part {
				if _, dup := m[k]; dup {
					panic("gui: locale key " + k + " defined twice (" + p + ")")
				}
				m[k] = v
			}
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
