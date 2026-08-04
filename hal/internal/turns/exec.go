// Package turns runs one agent process per conversational turn and settles
// its outcome. Exec is the battle-tested spawn plumbing extracted from the
// engine's pass loop (process groups / Job Objects, KillTree on cancel,
// merged stdout+stderr drain with a bounded scanner, log capture); Runner
// layers the interactive stream-json turn protocol on top of it.
package turns

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
)

// ExecSpec is one process execution request.
type ExecSpec struct {
	Plan     driver.ExecPlan
	Prompt   string        // piped to stdin when Plan.StdinPrompt
	Dir      string        // working directory
	ExtraEnv []string      // appended to os.Environ()
	Timeout  time.Duration // 0 = none; measured over the whole process
	LogFile  *os.File      // raw output lines, verbatim (nil = discard)
	// OnLine observes every captured output line, called from the drain
	// goroutine. It may keep running up to the drain grace period after Exec
	// returns on a wedged pipe — implementations must be safe for that.
	OnLine func(line string)
}

// ExecResult is the raw process outcome.
type ExecResult struct {
	ExitCode int
	Tail     []string // last 400 captured lines (nil for consoled/blind runs)
	TimedOut bool     // the Timeout elapsed (parent ctx still live)
}

// Exec runs the planned process to completion. Cancellation (parent ctx or
// Timeout) kills the whole process tree. The error covers spawn-level
// failures only; a non-zero agent exit is reported via ExitCode.
func Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	execCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(execCtx, spec.Plan.Exe, spec.Plan.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.ExtraEnv...)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	if spec.Plan.StdinPrompt {
		cmd.Stdin = strings.NewReader(spec.Prompt)
	}
	proc.Configure(cmd, spec.Plan.Mode)

	drained := make(chan struct{})
	var drainedTail []string // owned by the drain goroutine until `drained` closes
	var pr, pw *os.File
	if spec.Plan.Mode == proc.Piped {
		var err error
		pr, pw, err = os.Pipe()
		if err != nil {
			return ExecResult{ExitCode: -1}, err
		}
		cmd.Stdout = pw
		cmd.Stderr = pw
		go func() {
			defer close(drained)
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if spec.LogFile != nil {
					fmt.Fprintln(spec.LogFile, line)
				}
				drainedTail = appendTail(drainedTail, line)
				if spec.OnLine != nil {
					spec.OnLine(line)
				}
			}
			if err := sc.Err(); err != nil {
				// An over-long line (> the 1MB buffer cap) ends the scan
				// early — the rest of the run goes blind. Say so instead of
				// silently presenting a truncated log/tail.
				marker := fmt.Sprintf("(hal: output capture truncated: %v)", err)
				if spec.LogFile != nil {
					fmt.Fprintln(spec.LogFile, marker)
				}
				drainedTail = appendTail(drainedTail, marker)
				if spec.OnLine != nil {
					spec.OnLine(marker)
				}
			}
		}()
	} else {
		close(drained)
		if spec.LogFile != nil {
			fmt.Fprintln(spec.LogFile, "(driver output not capturable — see the agent's own log and progress.json)")
		}
	}

	if err := cmd.Start(); err != nil {
		if pw != nil {
			pw.Close()
			pr.Close()
		}
		return ExecResult{ExitCode: -1}, err
	}
	proc.Adopt(cmd.Process.Pid)
	defer proc.Finished(cmd.Process.Pid)
	if pw != nil {
		pw.Close() // parent's write end; child holds its own dup
	}
	waitErr := cmd.Wait()
	var tail []string
	if pr != nil {
		// Drain what the child wrote; a lingering grandchild holding the
		// pipe must not hang the turn (KillTree usually prevents this).
		// The tail is only safe to read once the goroutine is done — on
		// timeout it stays nil rather than racing.
		select {
		case <-drained:
			tail = drainedTail
		case <-time.After(5 * time.Second):
		}
		pr.Close()
	}

	res := ExecResult{
		ExitCode: cmd.ProcessState.ExitCode(),
		Tail:     tail,
		TimedOut: execCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil,
	}
	if waitErr != nil && res.ExitCode == 0 {
		res.ExitCode = -1
	}
	return res, nil
}

func appendTail(tail []string, line string) []string {
	const keep = 400
	tail = append(tail, line)
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	return tail
}
