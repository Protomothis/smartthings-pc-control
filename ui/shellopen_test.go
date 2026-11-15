package ui

import (
	"slices"
	"testing"
	"unicode/utf16"
)

// The Open dialog's filter list is name NUL patterns NUL … NUL NUL.
func TestFilterSpec(t *testing.T) {
	got := filterSpec([]fileFilter{{"스크립트", "*.ps1;*.bat"}, {"모든 파일", "*.*"}})
	want := utf16.Encode([]rune("스크립트\x00*.ps1;*.bat\x00모든 파일\x00*.*\x00\x00"))
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// The shell helpers the phase-2b screens use (logs: open the file or the
// folder; presets: browse) — kept compiling and in the lint's sight until
// a screen calls them.
var _ = []any{openPath, revealPath, (*UI).pickFile}
