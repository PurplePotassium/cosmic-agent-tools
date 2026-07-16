// Package export mirrors finished-pass evidence (pass logs, driver
// operational logs, archived agent transcripts) into an operator-chosen
// folder ([export] config) for auditing, optionally rendering transcripts
// as human-readable markdown.
package export

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
)

// Pass mirrors one pass's evidence files into dir: the pass log itself plus
// every "<stem>.*" sibling (driver op log, archived transcript, screened art
// intermediate). When humanReadable is set and an archived transcript exists,
// "<stem>.transcript.md" is rendered beside the copies. dir is created as
// needed. Copies are attempted for every file; the first error is returned.
func Pass(dir string, humanReadable bool, logPath string) error {
	logDir := filepath.Dir(logPath)
	stem := strings.TrimSuffix(filepath.Base(logPath), ".log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, stem+".") {
			continue
		}
		record(statedir.CopyFile(filepath.Join(logDir, name), filepath.Join(dir, name)))
	}
	if humanReadable {
		raw, err := os.ReadFile(filepath.Join(logDir, stem+".transcript.jsonl"))
		switch {
		case err == nil:
			record(statedir.WriteFileAtomic(filepath.Join(dir, stem+".transcript.md"),
				[]byte(RenderTranscript(stem, raw))))
		case !os.IsNotExist(err):
			record(err)
		}
	}
	return firstErr
}
