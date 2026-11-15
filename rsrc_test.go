package main

import (
	"bytes"
	"os"
	"testing"
)

// rsrc_windows_amd64.syso carries the icon and the application manifest
// (asInvoker, the supported Windows versions). Without cgo nothing else
// adds a manifest, so a .syso rebuilt from an old recipe or not rebuilt
// after an edit would ship an exe without one (#165).
func TestResourcesCurrent(t *testing.T) {
	syso, err := os.ReadFile("rsrc_windows_amd64.syso")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"smartthings-pc-control.manifest", "icon.ico"} {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if f == "icon.ico" {
			want = want[22:] // the .ico header and directory are rewritten; the image data is kept
		}
		if !bytes.Contains(syso, bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) && !bytes.Contains(syso, want) {
			t.Errorf("%s is not in rsrc_windows_amd64.syso; regenerate it (CONTRIBUTING.md)", f)
		}
	}
}
