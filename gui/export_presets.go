package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

// The toolkit-free part of the preset editor, for the Gio screen
// (ui/pages_presets.go, #165). The model itself is presets_model.go; this
// file only names it for package ui.

// PresetRow is one editor row as typed: Args is the single-line entry.
type PresetRow = presetRow

// PresetMaxSlots is the number of preset slots (1 … 10).
const PresetMaxSlots = presetMaxSlots

// PresetTypes are the preset kinds' wire values, in display order.
func PresetTypes() []string { return append([]string(nil), presetTypes...) }

// PresetRows are cfg's presets as editor rows, in slot order.
func PresetRows(cfg Config) []PresetRow { return presetsStateFromConfig(cfg).Rows }

// PresetRowProblem names what is wrong with r as an i18n key taking the
// slot (%d); "" when it looks fine.
func PresetRowProblem(r PresetRow) string { return rowProblem(r) }

// PresetRowsProblem checks every row, duplicate slots and duplicate names:
// the i18n key and the slot it is about, "" when all is well.
func PresetRowsProblem(rows []PresetRow) (string, int) { return rowsProblem(rows) }

// PresetDuplicateNames marks, by index, every row whose name another row
// also has (case-insensitive, trimmed).
func PresetDuplicateNames(rows []PresetRow) []bool { return duplicateNames(rows) }

// PresetFreeSlot is the lowest slot no row uses, 0 when all are taken.
func PresetFreeSlot(rows []PresetRow) int { return freeSlot(rows) }

// PresetOf is r in the wire form (trimmed, arguments split).
func PresetOf(r PresetRow) (Preset, error) { return r.preset() }

// PresetsDirty reports whether saving rows would change base's presets.
func PresetsDirty(rows []PresetRow, base Config) bool {
	return presetsFormState{Rows: rows}.dirty(base)
}

// ApplyPresets writes rows over cfg's presets (a new slice) and checks
// them; the error is the first problem in l's words.
func ApplyPresets(l Lang, rows []PresetRow, cfg *Config) error {
	s := presetsFormState{Rows: rows}
	if next, err := s.applyTo(*cfg); err == nil {
		*cfg = next
	}
	if key, slot := rowsProblem(rows); key != "" {
		return fmt.Errorf(T(l, key), slot)
	}
	return nil
}

// PresetSlotWarnings are the service's warnings about slot, one per
// paragraph; "" when it has none.
func PresetSlotWarnings(l Lang, ws []PresetWarning, slot int) string {
	return presetSlotWarnings(l, ws, slot)
}

// PresetWarningText is one service warning in l's words, its path on the
// next line.
func PresetWarningText(l Lang, w PresetWarning) string { return presetWarningText(l, w) }

// PresetLinkNote is the line under a URL row's path (#137) as an i18n key
// and the app it names: which app opens an app link, or that none on this
// PC does. "" for web addresses and other kinds.
func PresetLinkNote(r PresetRow) (key, app string) { return linkNote(r, protocolApp) }

// ActionErrorText is err from a preset run or test in l's words when the
// app knows its code, the service's message otherwise.
func ActionErrorText(l Lang, err error) string {
	if key := actionErrorKey(err); key != "" {
		return T(l, key)
	}
	return err.Error()
}

// PresetPathPlaceholder is the example path for a preset kind. The script
// one sits in the user's own Documents folder: a script in a folder other
// users can write to is one they could change into anything (C6).
func PresetPathPlaceholder(l Lang, typ string) string {
	switch typ {
	case "":
		return ""
	case "script":
		return fmt.Sprintf(T(l, "presets.path.placeholder.script"), documentsDir())
	}
	return T(l, "presets.path.placeholder."+typ)
}

// documentsDir is the user's Documents folder (wherever it was moved),
// %USERPROFILE%\Documents when Windows does not say.
var documentsDir = sync.OnceValue(func() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil && p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Documents")
	}
	return `%USERPROFILE%\Documents`
})
