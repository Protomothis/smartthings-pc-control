package session

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// waitDelay bounds how long a waited-for user-session command may keep its
// output pipes open after it exited or was killed — a grandchild that
// inherited them must not keep CombinedOutput blocked.
const waitDelay = 500 * time.Millisecond

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

// Output executes a command in the target user's desktop session (Session
// 0 isolation keeps the service off the interactive desktop), waits for it
// and returns the combined output. The child is killed when ctx is done.
// The output is returned even when the command fails, since a failing
// child may still have said why on stdout — user-action does exactly that.
func (w WTS) Output(ctx context.Context, target Target, name string, args ...string) ([]byte, error) {
	cmd, token, err := w.Command(ctx, target, name, args...)
	if err != nil {
		return nil, err
	}
	defer token.Close()
	cmd.WaitDelay = waitDelay

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("exec error: %w - output: %s", err, string(output))
	}
	return output, nil
}

// Start launches a command in the active user's desktop session without
// waiting for it. Used for long-lived processes such as the tray app, where
// waiting would pin a goroutine for the app's lifetime.
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
