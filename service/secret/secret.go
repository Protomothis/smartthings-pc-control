// Package secret stores small secrets (currently the Telegram bot token) in
// config.json without leaving them in plaintext on disk.
//
// Protect encrypts a value with Windows DPAPI (CryptProtectData) and returns
// "dpapi:" + base64(ciphertext); Unprotect reverses it. Values without the
// prefix are treated as legacy plaintext and passed through unchanged so the
// caller can re-save them encrypted on the next write.
//
// Machine scope (CRYPTPROTECT_LOCAL_MACHINE) is used deliberately: the
// service runs as LocalSystem and the tray GUI runs as the logged-in user, and
// both must be able to read the same config.json. User scope would tie the
// ciphertext to whichever account wrote it and break the other side.
//
// Trade-off: with machine scope any process that can run on this machine and
// read config.json can also decrypt the token. The goal is narrower than that
// threat model. It keeps the token out of plaintext on disk and out of file
// backups or copied config files, and it is at least as protected as the
// existing "secret" API key field in the same file. The ciphertext is bound to
// this machine's DPAPI master key, so a config.json copied to another PC will
// fail to decrypt there and the token must be entered again.
package secret

// Prefix marks a value produced by Protect.
const Prefix = "dpapi:"

// IsProtected reports whether stored carries the DPAPI prefix with a payload.
func IsProtected(stored string) bool {
	return len(stored) > len(Prefix) && stored[:len(Prefix)] == Prefix
}

// Mask returns a display form that hides the value: "****" followed by the
// last 4 characters, or just "****" when the value is shorter than 8
// characters. Empty input yields "".
func Mask(plain string) string {
	if plain == "" {
		return ""
	}
	r := []rune(plain)
	if len(r) < 8 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}
