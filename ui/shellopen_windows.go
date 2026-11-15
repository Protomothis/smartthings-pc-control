package ui

// Opening things with Windows: a web page, a file or folder, a file shown
// selected in Explorer, and the native Open dialog for picking a file (the
// preset editor's 찾아보기).

import (
	"errors"
	"os/exec"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// openURL opens url in the default browser.
func openURL(url string) error { return shellOpen(url) }

// openPath opens a file with its program, or a folder in Explorer.
func openPath(path string) error { return shellOpen(path) }

// revealPath opens Explorer with path selected.
func revealPath(path string) error {
	return exec.Command(systool.Explorer(), "/select,"+path).Start()
}

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const swShowNormal = 1
	return windows.ShellExecute(0, verb, file, nil, nil, swShowNormal)
}

// fileFilter is one entry of the Open dialog's type list: "스크립트" with
// patterns "*.ps1;*.bat;*.cmd".
type fileFilter struct {
	name, patterns string
}

// errPickCancelled is a closed Open dialog.
var errPickCancelled = errors.New("cancelled")

var (
	modComdlg32          = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileNameW = modComdlg32.NewProc("GetOpenFileNameW")
)

// openFileNameW is OPENFILENAMEW.
type openFileNameW struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

const (
	ofnFileMustExist = 0x00001000
	ofnPathMustExist = 0x00000800
	ofnNoChangeDir   = 0x00000008
	ofnExplorer      = 0x00080000
)

// pickFile shows the native Open dialog over the window and calls done
// with the chosen path, or errPickCancelled. The dialog blocks its own
// goroutine, not the window; done runs on the window goroutine (onWindow).
// The filters come first, then 모든 파일; dir is where it starts ("" for
// the last place used).
func (u *UI) pickFile(title, dir string, filters []fileFilter, done func(path string, err error)) {
	owner := u.hwnd.Load()
	all := u.t("ui.pick.all")
	go func() {
		path, err := openFileDialog(owner, title, dir, append(filters, fileFilter{all, "*.*"}))
		u.onWindow(func() { done(path, err) })
	}()
}

// filterSpec is OPENFILENAMEW's lpstrFilter: name NUL patterns NUL … then
// one more NUL. (syscall.UTF16FromString refuses inner NULs.)
func filterSpec(filters []fileFilter) []uint16 {
	var out []uint16
	for _, f := range filters {
		out = append(out, utf16.Encode([]rune(f.name))...)
		out = append(out, 0)
		out = append(out, utf16.Encode([]rune(f.patterns))...)
		out = append(out, 0)
	}
	return append(out, 0)
}

// openFileDialog runs GetOpenFileNameW on the calling goroutine.
func openFileDialog(owner uintptr, title, dir string, filters []fileFilter) (string, error) {
	filter := filterSpec(filters)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	ofn := openFileNameW{
		owner:       owner,
		filter:      &filter[0],
		filterIndex: 1,
		file:        &buf[0],
		maxFile:     uint32(len(buf)),
		flags:       ofnFileMustExist | ofnPathMustExist | ofnNoChangeDir | ofnExplorer,
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	if title != "" {
		ofn.title, _ = windows.UTF16PtrFromString(title)
	}
	if dir != "" {
		ofn.initialDir, _ = windows.UTF16PtrFromString(dir)
	}
	if r, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return "", errPickCancelled
	}
	return windows.UTF16ToString(buf), nil
}
