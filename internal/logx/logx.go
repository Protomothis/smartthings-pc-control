// Package logx is the service log: service.log next to the exe, appended
// to, rotated at maxSize into service.log.1 … .3. Until Init has opened the
// file every line is dropped, which is what the installer and the CLI
// commands want.
package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileName is the log file's name in the folder Init is given.
const FileName = "service.log"

const (
	maxSize    = 512 * 1024 // 512KB
	maxBackups = 3
)

var (
	mu     sync.Mutex
	logger *log.Logger
	file   *os.File
	path   string
)

// Init opens dir\service.log for appending. A second call while the log
// is open does nothing; a file that cannot be opened leaves logging off.
func Init(dir string) {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		return // Already initialized
	}
	if dir == "" {
		return
	}
	path = filepath.Join(dir, FileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	file = f
	logger = log.New(f, "", log.LstdFlags)
}

// Close closes the file; later lines are dropped until the next Init.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
		logger = nil
	}
}

// Path is the log file's path, "" before the first Init.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// rotate must be called with mu held.
func rotate() {
	if file == nil || path == "" {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Size() < maxSize {
		return
	}

	// Close current log
	file.Close()

	// Rotate: .3 삭제, .2→.3, .1→.2, current→.1
	for i := maxBackups; i >= 1; i-- {
		src := path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", path, i-1)
		}
		dst := fmt.Sprintf("%s.%d", path, i)
		os.Remove(dst)
		os.Rename(src, dst)
	}

	// Open new log file
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		file = nil
		logger = nil
		return
	}
	file = f
	logger = log.New(f, "", log.LstdFlags)
}

// Printf writes one line (log.Printf rules) and rotates when the file has
// grown past its limit.
func Printf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()

	if logger != nil {
		logger.Printf(format, args...)
		rotate()
	}
}

// ErrorLog is a logger for http.Server.ErrorLog that writes through
// Printf (#133): a panic in a handler, which net/http recovers, then shows
// up in service.log with its stack instead of on a stderr nobody reads.
func ErrorLog() *log.Logger { return log.New(printfWriter{}, "", 0) }

// printfWriter hands each write (one message) to Printf.
type printfWriter struct{}

func (printfWriter) Write(p []byte) (int, error) {
	Printf("%s", strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}

// Capture sends every line to w, without timestamps, until the returned
// function restores the previous logger (tests).
func Capture(w io.Writer) (restore func()) {
	mu.Lock()
	saved := logger
	logger = log.New(w, "", 0)
	mu.Unlock()
	return func() {
		mu.Lock()
		logger = saved
		mu.Unlock()
	}
}

// MaskSecret is how a secret appears in the log: "(none)", "***" for a
// short one, otherwise the first and last two characters.
func MaskSecret(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) <= 4 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}
