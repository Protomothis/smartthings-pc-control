package main

import (
	"bytes"
	"encoding/binary"
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
	manifest, err := os.ReadFile("smartthings-pc-control.manifest")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(syso, bytes.ReplaceAll(manifest, []byte("\r\n"), []byte("\n"))) && !bytes.Contains(syso, manifest) {
		t.Error("smartthings-pc-control.manifest is not in rsrc_windows_amd64.syso; regenerate it (CONTRIBUTING.md)")
	}
	// windres rewrites the .ico directory but keeps each image's data.
	ico, err := os.ReadFile("icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if len(ico) < 6 {
		t.Fatal("icon.ico is too short")
	}
	n := int(le.Uint16(ico[4:]))
	if n == 0 || len(ico) < 6+16*n {
		t.Fatalf("icon.ico: bad directory (%d images)", n)
	}
	for i := 0; i < n; i++ {
		e := ico[6+16*i:]
		size, off := int(le.Uint32(e[8:])), int(le.Uint32(e[12:]))
		if off+size > len(ico) {
			t.Fatalf("icon.ico: image %d runs past the end", i)
		}
		if !bytes.Contains(syso, ico[off:off+size]) {
			t.Errorf("icon.ico image %d is not in rsrc_windows_amd64.syso; regenerate it (CONTRIBUTING.md)", i)
		}
	}
}
