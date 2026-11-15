package session

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// Command builds an exec.Cmd that runs under the token of the target
// session's user, i.e. inside their interactive desktop session. The
// caller must Close the returned token once the process has been started.
// The command is killed when ctx is done (exec.CommandContext), and the
// error wraps ErrNoUserSession (ErrNoConsoleSession for Console) when
// nobody is logged in there.
func (w WTS) Command(ctx context.Context, target Target, name string, args ...string) (*exec.Cmd, syscall.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	_, token, err := w.Find(target)
	if err != nil {
		return nil, 0, fmt.Errorf("get session: %w", err)
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token: token,
	}
	return cmd, token, nil
}

// Start launches a command in the active user's desktop session without
// waiting for it: the tray app, for the grace-period toast. It is the
// service's only process start in a user session; the user-session
// actions go to the running tray app instead (Broker, #164).
func (w WTS) Start(name string, args ...string) error {
	cmd, token, err := w.Command(context.Background(), ActiveUser, name, args...)
	if err != nil {
		return err
	}
	defer token.Close()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start error: %v", err)
	}
	// Detach: the child outlives this call and nobody will Wait on it.
	return cmd.Process.Release()
}
