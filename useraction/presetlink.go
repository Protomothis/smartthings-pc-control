package useraction

import (
	"errors"
	"net/url"
	"strings"
)

// The rule for a url preset (#137): a web address (http/https with a host)
// or a Windows app link — any registered URL protocol such as
// steam://rungameid/570, spotify:track:…, ms-settings:display. ShellExecute
// opens both the same way. The schemes below are refused: they open local or
// remote files, run script, or are known ways to start something other than
// an app's own "open this" handler.
//
// One rule for the subcommand (Parse), config.ValidatePreset (through Parse)
// and the GUI editor, which maps the two errors to its own messages.

var (
	// ErrPresetURLInvalid: not http(s) with a host, nor "scheme:something".
	ErrPresetURLInvalid = errors.New("preset: url must be http(s):// or an app link such as steam://")
	// ErrPresetURLBlocked: a well-formed link whose scheme is refused.
	ErrPresetURLBlocked = errors.New("preset: this kind of link cannot be a preset (file:, javascript:, search-ms: …)")
)

// blockedPresetSchemes are refused whatever follows them.
var blockedPresetSchemes = map[string]bool{
	// files, script and in-page content
	"file": true, "javascript": true, "vbscript": true, "data": true, "about": true,
	"shell": true, "res": true, "mk": true, "its": true, "ms-its": true, "mhtml": true,
	// known Windows abuse paths: help, diagnostics, search, installers,
	// Office's launcher and the Windows setup host
	"hcp": true, "ms-msdt": true, "search-ms": true, "search": true,
	"ms-officecmd": true, "ms-appinstaller": true, "ms-cxh": true, "ms-cxh-full": true,
	// Office document handlers: they open a document from any address
	"ms-word": true, "ms-excel": true, "ms-powerpoint": true, "ms-access": true,
	"ms-visio": true, "ms-project": true, "ms-publisher": true, "ms-infopath": true,
	"ms-spd": true, "onenote": true,
}

// PresetURLScheme returns the lower-case scheme of a url preset's path, or
// ErrPresetURLInvalid / ErrPresetURLBlocked.
func PresetURLScheme(raw string) (string, error) {
	i := strings.IndexByte(raw, ':')
	// One letter would be a drive (C:\…), not a protocol.
	if i < 2 || !validScheme(raw[:i]) {
		return "", ErrPresetURLInvalid
	}
	scheme := strings.ToLower(raw[:i])
	if scheme == "http" || scheme == "https" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", ErrPresetURLInvalid
		}
		return scheme, nil
	}
	if blockedPresetSchemes[scheme] {
		return "", ErrPresetURLBlocked
	}
	if strings.Trim(raw[i+1:], "/") == "" {
		return "", ErrPresetURLInvalid
	}
	return scheme, nil
}

// validScheme is RFC 3986's ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func validScheme(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return s != ""
}
