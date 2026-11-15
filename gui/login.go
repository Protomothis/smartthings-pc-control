package gui

import (
	"errors"
)

// loginErrorMessage is the text shown after a failed login with the
// secret (the home screen's login card). The "60초" in login.limited is
// service/webui_wiring.go loginLockDuration.
func loginErrorMessage(err error, l Lang) string {
	switch {
	case errors.Is(err, errLoginLimited):
		return T(l, "login.limited")
	case errors.Is(err, errLoginInvalid):
		return T(l, "login.invalid")
	default:
		return err.Error()
	}
}

// LoginErrorText is err from Client.Login in the user's words: a wrong
// secret, the service's rate limit, or the error as is.
func LoginErrorText(l Lang, err error) string { return loginErrorMessage(err, l) }
