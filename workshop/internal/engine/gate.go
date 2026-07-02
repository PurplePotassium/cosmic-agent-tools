package engine

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/proc"
)

const defaultGateTimeout = 15 * time.Minute

// runGate executes the project's verify/gate command in dir. An empty
// command is vacuously green. The gate is killable: a hung build must not
// wedge the integrator or a resolution pass.
func runGate(ctx context.Context, dir, command string, timeout time.Duration) (green bool, output string) {
	if strings.TrimSpace(command) == "" {
		return true, ""
	}
	if timeout <= 0 {
		timeout = defaultGateTimeout
	}
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(gctx, "cmd", "/d", "/c", command)
	} else {
		cmd = exec.CommandContext(gctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	proc.Configure(cmd, proc.Piped)

	out, err := cmd.CombinedOutput()
	tail := string(out)
	if len(tail) > 4000 {
		tail = "...\n" + tail[len(tail)-4000:]
	}
	if gctx.Err() == context.DeadlineExceeded {
		return false, tail + "\n(gate timed out)"
	}
	return err == nil, tail
}
