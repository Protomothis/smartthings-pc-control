package gui

import (
	"errors"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// loginState guards the login dialog (#98). Before it, connTick fired
// initialLoad every 5 s while disconnected, and every 401 opened another
// dialog on top of the one the user was typing into — the new, empty entry
// took focus, so typed and pasted text seemed to vanish, and each stacked
// dialog could post a wrong secret until the service's rate limit (5
// failures, 60 s lock) rejected even the right one.
//
// Written on the UI thread only; pollLoop reads it (hence atomic).
type loginState int32

const (
	// loginIdle: no dialog open, no attempt in flight. A 401 from
	// initialLoad opens the dialog.
	loginIdle loginState = iota
	// loginPrompting: the dialog or its error follow-up is showing, or a
	// Login call is in flight. Nothing opens a second dialog and connTick
	// leaves initialLoad alone.
	loginPrompting
	// loginDeferred: the user cancelled, or the service refused the attempt
	// (rate limit, unreachable). No automatic prompt; the status-bar Login
	// button starts the next attempt.
	loginDeferred
)

// shouldAutoLoad reports whether connTick may retry initialLoad. Retrying
// while deferred is fine (initialLoad never prompts then, and GetConfig is
// not a login attempt); while prompting it would only race the dialog.
func shouldAutoLoad(connected bool, st loginState) bool {
	return !connected && st != loginPrompting
}

// shouldPromptLogin reports whether a 401 from initialLoad may open the
// login dialog: only when none is open and the user has not cancelled.
func shouldPromptLogin(st loginState) bool {
	return st == loginIdle
}

// retryPromptAfter reports whether a failed attempt re-opens the dialog
// (a mistyped secret) or waits for the Login button (rate limit, service
// gone — re-prompting would only fail again).
func retryPromptAfter(err error) bool {
	return errors.Is(err, errLoginInvalid)
}

// shouldReloginAfterSave reports whether saving the settings changed the
// secret to a non-empty value. Going from no secret to one leaves this
// client without a valid session, so it logs in with the new secret right
// away instead of waiting for the next 401 to prompt.
func shouldReloginAfterSave(oldSecret, newSecret string) bool {
	return newSecret != "" && newSecret != oldSecret
}

// loginErrorMessage is the text shown after a failed login attempt. The
// "60초" in login.limited is service/webui_wiring.go loginLockDuration.
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

func (u *ui) loginState() loginState { return loginState(u.login.Load()) }

// setLoginState records s and shows the status-bar Login button only while
// deferred. UI thread only.
func (u *ui) setLoginState(s loginState) {
	u.login.Store(int32(s))
	if u.loginBtn == nil {
		return
	}
	if s == loginDeferred {
		u.loginBtn.Show()
	} else {
		u.loginBtn.Hide()
	}
}

// deferLogin stops automatic prompting until the user presses Login.
func (u *ui) deferLogin() {
	u.setLoginState(loginDeferred)
	u.setStatus(u.t("login.required"))
	u.setConn(connPending)
}

// promptLogin shows the password dialog and calls onSuccess after a valid
// login. A no-op while a dialog is already open or an attempt is in flight.
// UI thread only.
func (u *ui) promptLogin(onSuccess func()) {
	if u.loginState() == loginPrompting {
		return
	}
	u.setLoginState(loginPrompting)

	entry := widget.NewPasswordEntry()
	// A validator keeps the confirm button disabled while the entry is
	// empty, so an empty secret is never posted (it would only count as a
	// failed attempt towards the lockout).
	entry.Validator = func(s string) error {
		if s == "" {
			return errors.New(u.t("login.empty"))
		}
		return nil
	}
	items := []*widget.FormItem{widget.NewFormItem(u.t("login.secret"), entry)}
	d := dialog.NewForm(u.t("login.title"), u.t("login.ok"), u.t("login.cancel"), items, func(ok bool) {
		if !ok {
			u.deferLogin()
			return
		}
		secret := entry.Text
		if secret == "" { // unreachable with the validator; never post it
			u.setLoginState(loginIdle)
			u.promptLogin(onSuccess)
			return
		}
		runAsyncErr(nil, func() error { return u.client.Login(secret) },
			func(err error) { u.finishLogin(err, onSuccess) })
	}, u.win)
	entry.OnSubmitted = func(string) { d.Submit() } // Enter; ignored while invalid
	d.Resize(fyne.NewSize(360, 0))
	d.Show()
	u.win.Canvas().Focus(entry)
}

// finishLogin handles the result of a dialog login attempt. The state stays
// loginPrompting while the error dialog is up, so no timer can open a login
// dialog behind it.
func (u *ui) finishLogin(err error, onSuccess func()) {
	if err == nil {
		u.setLoginState(loginIdle)
		onSuccess()
		return
	}
	e := dialog.NewError(errors.New(loginErrorMessage(err, u.lang)), u.win)
	e.SetOnClosed(func() {
		if retryPromptAfter(err) {
			u.setLoginState(loginIdle)
			u.promptLogin(onSuccess)
			return
		}
		u.deferLogin()
	})
	e.Show()
}
