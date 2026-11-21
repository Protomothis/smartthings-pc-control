package gui

// Masking for the problem report (#175). Everything that leaves the PC in
// a report — the logs, the issue body, report.md — goes through a Masker
// first. The report is posted publicly on GitHub, so the rules lean
// towards hiding too much: a log line that loses a harmless number is a
// smaller loss than one that leaks a chat id.
//
// What is hidden, in the order it is applied:
//
//   - now-playing text: everything after "now playing" on a line, and the
//     value of a title/artist/album key, so what someone listens to never
//     shows up;
//   - the Telegram "ignored message from unknown chat" line's chat, user
//     name and message text;
//   - the given secrets (the service secret, a plaintext bot token)
//     wherever they occur, and anything shaped like a bot token;
//   - e-mail addresses;
//   - the given names (the PC name, the Windows user name, the Telegram
//     PC name) as whole words, in any case;
//   - the user folder in a path: C:\Users\<name>\ → C:\Users\<user>\;
//   - MAC addresses but their first three bytes (the vendor);
//   - IPv6 addresses (but the loopback);
//   - IPv4 addresses: a public one entirely, a private one but its last
//     number (the loopback and 0.0.0.0 stay);
//   - the configured chat ids, and any long number written after "chat".

import (
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Placeholders the masked parts are replaced by. None of them matches a
// rule again, so masking twice changes nothing.
const (
	maskSecret   = "<secret>"
	maskBotToken = "<bot-token>" //nolint:gosec // the placeholder that replaces a token, not one
	maskChatID   = "<chat-id>"
	maskEmail    = "<email>"
	maskName     = "<name>"
	maskUser     = "<user>"
	maskMedia    = "<media>"
	maskText     = "<text>"
	maskPublicIP = "<public-ip>"
	maskIPv6     = "<ipv6>"
)

// MaskInput is what a Masker hides by value.
type MaskInput struct {
	// Secrets are replaced wherever they occur: the service secret, a bot
	// token known in plain text. Values shorter than 4 characters are
	// ignored (they would match half the log).
	Secrets []string
	// ChatIDs are the configured Telegram chat ids.
	ChatIDs []string
	// Names are replaced as whole words: the PC name, the Windows user
	// name, the PC name set for Telegram. Values shorter than 2 characters
	// are ignored.
	Names []string
}

// Masker hides personal data in report text. Safe for concurrent use.
type Masker struct {
	secrets []*regexp.Regexp
	chatIDs []*regexp.Regexp
	names   []*regexp.Regexp
}

var (
	// maskNowPlayingRe is a "now playing" phrase: what follows it on the
	// line is a title.
	maskNowPlayingRe = regexp.MustCompile(`(?i)\bnow[ _-]?playing\b`)
	// maskMediaKeyRe is a title/artist/album key with its value, as JSON
	// ("title":"…") or key=value / key: value.
	maskMediaKeyRe = regexp.MustCompile(`(?i)(\b(?:title|artist|album|track|song)"?\s*[=:]\s*)("[^"]*"|'[^']*'|[^,;|)\]}\r\n]*)`)
	// maskUnknownChatRe is service/tgcontrol's "ignored message from
	// unknown chat <id> (<user>): <text>".
	maskUnknownChatRe = regexp.MustCompile(`(?i)(unknown chat )\S+ \([^)]*\): .*`)
	// maskBotTokenRe is a Telegram bot token: the bot id, a colon and the
	// key.
	maskBotTokenRe = regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{30,}`)
	maskEmailRe    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// maskUserDirRe is the folder under C:\Users in a path, with either
	// slash and doubled backslashes (JSON, %q) alike. A name with spaces
	// ("John Doe") counts only when the path goes on after it; otherwise the
	// name ends at the first space, so the words after a bare
	// "C:\Users\jdoe" in a sentence are not swallowed (#177).
	maskUserDirRe = regexp.MustCompile(`(?i)(\b[a-z]:(?:\\\\|\\|/)+users(?:\\\\|\\|/)+)(?:[^\\/:*?"<>|\r\n]+(\\|/)|[^\\/:*?"<>|\r\n\s]+)`)
	// maskMACRe is six hex bytes split by colons or dashes.
	maskMACRe = regexp.MustCompile(`(?i)\b([0-9a-f]{2}[:-][0-9a-f]{2}[:-][0-9a-f]{2})([:-])[0-9a-f]{2}[:-][0-9a-f]{2}[:-][0-9a-f]{2}\b`)
	// maskIPv6Re is an IPv6 address with "::" or at least five groups —
	// never a clock time ("15:04:05") or a MAC (already masked).
	maskIPv6Re = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){4,7}\b|(?:\b[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*\b)?)(?:%[0-9a-z]+)?`)
	maskIPv4Re = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	// maskChatRe is a long number after the word chat ("chat 123456789",
	// "chat_id=-100123…", "chat id: …").
	maskChatRe = regexp.MustCompile(`(?i)(\bchat(?:[ _]?ids?)?["']?\s*[:=(]?\s*["']?)(-?\d{5,})`)
)

// NewMasker prepares a Masker for in.
func NewMasker(in MaskInput) *Masker {
	m := &Masker{}
	for _, v := range keepLonger(in.Secrets, 4) {
		m.secrets = append(m.secrets, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(v)))
	}
	for _, v := range keepLonger(in.ChatIDs, 3) {
		m.chatIDs = append(m.chatIDs, wordRe(v))
	}
	for _, v := range keepLonger(in.Names, 2) {
		m.names = append(m.names, wordRe(v))
	}
	return m
}

// wordRe matches v as a whole word in any case, with the character before
// and after it as groups 1 and 2: the user "kim" must not turn "skimming"
// into garbage, nor the chat id 12345 a longer number. Angle brackets
// count as part of a word, so a placeholder ("<name>") is never matched
// again by a value that happens to spell it.
func wordRe(v string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}<>])` + regexp.QuoteMeta(v) + `($|[^\p{L}\p{N}<>])`)
}

// replaceWord replaces every whole-word match of re by repl. A match takes
// the character on either side, so two matches one character apart need
// a second pass.
func replaceWord(s string, re *regexp.Regexp, repl string) string {
	for range 2 {
		s = re.ReplaceAllString(s, "${1}"+repl+"${2}")
	}
	return s
}

// maskMediaValue hides a title/artist/album value, keeping its quotes.
func maskMediaValue(match string) string {
	sm := maskMediaKeyRe.FindStringSubmatch(match)
	v := sm[2]
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		return sm[1] + v[:1] + maskMedia + v[:1]
	}
	return sm[1] + maskMedia
}

// keepLonger is the distinct trimmed values of at least n characters,
// longest first, so a value that contains another is replaced first.
func keepLonger(vals []string, n int) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len([]rune(v)) < n || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Text masks every line of s.
func (m *Masker) Text(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = m.Line(l)
	}
	return strings.Join(lines, "\n")
}

// Lines masks each of lines into a new slice.
func (m *Masker) Lines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = m.Line(l)
	}
	return out
}

// Line masks one line (see the top of this file for the rules).
func (m *Masker) Line(s string) string {
	if loc := maskNowPlayingRe.FindStringIndex(s); loc != nil {
		s = s[:loc[1]] + " " + maskMedia
	}
	s = maskMediaKeyRe.ReplaceAllStringFunc(s, maskMediaValue)
	s = maskUnknownChatRe.ReplaceAllString(s, "${1}"+maskChatID+" ("+maskUser+"): "+maskText)
	for _, re := range m.secrets {
		s = re.ReplaceAllLiteralString(s, maskSecret)
	}
	s = maskBotTokenRe.ReplaceAllString(s, maskBotToken)
	s = maskEmailRe.ReplaceAllString(s, maskEmail)
	// The user folder before the names, so a path always reads
	// C:\Users\<user> even when the folder is the user name.
	s = maskUserDirRe.ReplaceAllString(s, "${1}"+maskUser+"${2}")
	for _, re := range m.names {
		s = replaceWord(s, re, maskName)
	}
	s = maskMACRe.ReplaceAllString(s, "${1}${2}XX${2}XX${2}XX")
	s = maskIPv6Re.ReplaceAllStringFunc(s, maskIPv6Addr)
	s = maskIPv4Re.ReplaceAllStringFunc(s, maskIPv4Addr)
	for _, re := range m.chatIDs {
		s = replaceWord(s, re, maskChatID)
	}
	return maskChatRe.ReplaceAllString(s, "${1}"+maskChatID)
}

// maskIPv6Addr hides an IPv6 address; the loopback and a lone "::" (not an
// address in a log line, most likely) stay.
func maskIPv6Addr(s string) string {
	if s == "::" || s == "::1" {
		return s
	}
	if _, err := netip.ParseAddr(s); err != nil {
		return s
	}
	return maskIPv6
}

// maskIPv4Addr hides a public IPv4 address and the last number of a
// private one. What does not parse (a version "1.2.3.400") stays.
func maskIPv4Addr(s string) string {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return s
	}
	if a.IsLoopback() || a.IsUnspecified() {
		return s
	}
	if a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a) {
		return s[:strings.LastIndexByte(s, '.')] + ".x"
	}
	return maskPublicIP
}

// cgnat is 100.64.0.0/10, the shared address space Tailscale also uses: a
// home network address, not a public one.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")
