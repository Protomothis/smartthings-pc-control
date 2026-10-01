package secret

import (
	"crypto/sha256"
	"crypto/subtle"
)

// Equal compares a presented secret (or session token) with the expected
// one in constant time (#120). Both sides are hashed first, so not even the
// length of the expected value leaks through timing. Every password-like
// check of the service goes through it: the legacy URL secret, the
// X-PC-Secret header, the WebUI login and its session cookies.
func Equal(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
