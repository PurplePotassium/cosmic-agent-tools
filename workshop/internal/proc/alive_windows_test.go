//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
)

func isAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"`+strconv.Itoa(pid)+`"`)
}
