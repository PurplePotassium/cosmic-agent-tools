// Package turns runs one agent process per conversational turn and settles
// its outcome. Exec is the battle-tested spawn plumbing extracted from the
// engine's pass loop (process groups / Job Objects, KillTree on cancel,
// merged stdout+stderr drain with a bounded scanner, log capture); Runner
// layers the interactive stream-json turn protocol on top of it.
package turns

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
			emit := func(line string) {
				if spec.LogFile != nil {
					fmt.Fprintln(spec.LogFile, line)
				}
				drainedTail = appendTail(drainedTail, line)
				if spec.OnLine != nil {
					spec.OnLine(line)
				}
			}
			if err := drainOutputLines(pr, emit); err != nil {
				emit(fmt.Sprintf("(hal: output capture stopped: %v)", err))
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

const maxOutputLine = 1024 * 1024

// drainOutputLines reads a child pipe without ever letting an oversized line
// stop the drain. Agent output is line-oriented NDJSON, but a tool result can
// put arbitrarily large content on one line. Retaining that whole line would
// make capture memory unbounded; stopping at a Scanner token limit would leave
// the pipe unread and wedge the child. Keep at most maxOutputLine bytes,
// discard the rest through the newline, emit one marker, then resume with the
// next line so a later authoritative result event can still settle the turn.
func drainOutputLines(r io.Reader, emit func(string)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	line := make([]byte, 0, 64*1024)
	oversized := false

	for {
		fragment, isPrefix, err := br.ReadLine()
		if !oversized {
			if len(line)+len(fragment) <= maxOutputLine {
				line = append(line, fragment...)
			} else {
				line = line[:0]
				oversized = true
			}
		}

		if !isPrefix {
			switch {
			case oversized:
				emit("(hal: output line exceeded 1 MiB and was discarded)")
			case len(line) > 0 || err == nil:
				// err == nil distinguishes a real empty line from EOF with no
				// remaining bytes, matching bufio.Scanner's line semantics.
				emit(string(line))
			}
			line = line[:0]
			oversized = false
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func appendTail(tail []string, line string) []string {
	const keep = 400
	tail = append(tail, line)
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	return tail
}
