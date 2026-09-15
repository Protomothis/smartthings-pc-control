//go:build windows

package secret

import (
	"encoding/base64"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiFlags: machine scope so LocalSystem (service) and the user (GUI) share
// the ciphertext; UI forbidden because neither caller has an interactive
// desktop to show a prompt on.
const dpapiFlags = windows.CRYPTPROTECT_LOCAL_MACHINE | windows.CRYPTPROTECT_UI_FORBIDDEN

// Protect encrypts plain with DPAPI in machine scope and returns
// "dpapi:" + base64(ciphertext). Empty input returns "" with no error.
func Protect(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	ct, err := dpapiCall([]byte(plain), windows.CryptProtectData)
	if err != nil {
		return "", fmt.Errorf("secret: DPAPI protect failed: %w", err)
	}
	return Prefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Unprotect decrypts a value produced by Protect. Values without the "dpapi:"
// prefix are returned unchanged (legacy plaintext); empty input returns "".
// The returned error never contains the stored value.
func Unprotect(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !IsProtected(stored) {
		return stored, nil
	}
	ct, err := base64.StdEncoding.DecodeString(stored[len(Prefix):])
	if err != nil {
		return "", errors.New("secret: invalid DPAPI payload encoding")
	}
	plain, err := dpapiCall(ct, unprotect)
	if err != nil {
		return "", fmt.Errorf("secret: DPAPI unprotect failed: %w", err)
	}
	return string(plain), nil
}

// unprotect adapts CryptUnprotectData to the CryptProtectData signature
// (the only difference is the description parameter, which is never used).
func unprotect(in *windows.DataBlob, _ *uint16, entropy *windows.DataBlob, reserved uintptr, prompt *windows.CryptProtectPromptStruct, flags uint32, out *windows.DataBlob) error {
	return windows.CryptUnprotectData(in, nil, entropy, reserved, prompt, flags, out)
}

type dpapiFunc func(*windows.DataBlob, *uint16, *windows.DataBlob, uintptr, *windows.CryptProtectPromptStruct, uint32, *windows.DataBlob) error

// dpapiCall runs one DPAPI transform on in and returns a Go-owned copy of the
// output, freeing the LocalAlloc buffer the API hands back.
func dpapiCall(in []byte, fn dpapiFunc) ([]byte, error) {
	if len(in) == 0 {
		return nil, errors.New("empty input")
	}
	inBlob := windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	var outBlob windows.DataBlob
	if err := fn(&inBlob, nil, nil, 0, nil, dpapiFlags, &outBlob); err != nil {
		return nil, err
	}
	if outBlob.Data == nil {
		return nil, errors.New("DPAPI returned no data")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	out := make([]byte, outBlob.Size)
	copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	return out, nil
}
