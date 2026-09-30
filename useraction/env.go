package useraction

import (
	"os"

	"golang.org/x/sys/windows"
)

// userEnviron is the logged-in user's own environment block (APPDATA,
// TEMP, USERPROFILE, PATH with the user's entries…).
//
// The service starts this subcommand with CreateProcessAsUser under the
// user's token but hands it the service's environment, which is SYSTEM's:
// APPDATA points into C:\Windows\system32\config\systemprofile and TEMP at
// C:\Windows\TEMP. Anything a preset launches would inherit that and, say,
// keep its settings in the wrong profile. CreateEnvironmentBlock for the
// process token builds the block the user's shell would have. On failure
// the inherited environment is used as before.
func userEnviron() []string {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		return os.Environ()
	}
	defer tok.Close()
	env, err := tok.Environ(false)
	if err != nil || len(env) == 0 {
		return os.Environ()
	}
	return env
}

// adoptUserEnviron replaces this process's environment with userEnviron,
// for the launches that inherit it implicitly (ShellExecute).
func adoptUserEnviron() {
	env := userEnviron()
	os.Clearenv()
	for _, kv := range env {
		// "=C:=C:\\" style per-drive entries start with '='; skip them.
		for i := 1; i < len(kv); i++ {
			if kv[i] == '=' {
				os.Setenv(kv[:i], kv[i+1:])
				break
			}
		}
	}
}
