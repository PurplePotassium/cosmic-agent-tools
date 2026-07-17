package main

import (
	"fmt"
	"os"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
)

// cmdAgyRun is the agy passthrough: it spawns agy with args verbatim in the
// current directory, inside a hidden console (agy silently drops output — and
// can hang — when its stdio is piped, so a shell-spawned `agy` is never
// safe), waits, and exits with agy's own exit code. Art passes' claude
// orchestrator invokes agy exclusively through this command; anything agy
// "prints" must be read from its op log (--log-file) or its artifacts.
func cmdAgyRun(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(`usage: workshop agy-run [agy args...]

Runs agy with the given args verbatim in the current directory, inside a
hidden console (agy drops output / hangs on piped stdio), and exits with
agy's exit code. Nothing is captured: pass --log-file to read agy's
operational log afterwards.
`)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	ctx, cancel := interruptCtx()
	defer cancel()
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agy-run:", err)
		return 2
	}
	code, err := driver.AgyExec(ctx, wd, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agy-run:", err)
		if code < 0 {
			return 2
		}
	}
	if code < 0 {
		return 1
	}
	return code
}
