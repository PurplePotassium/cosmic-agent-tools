package config

import (
	"errors"
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
)

// WriteOverridePipelines rewrites the "pipelines" table of the runtime
// overrides file at path with the full desired pipeline list, preserving
// every other key already there. mergeMaps replaces arrays wholesale rather
// than merging per-entry (see load.go), so callers must always pass the
// complete pipeline list — not just the one being added or removed.
func WriteOverridePipelines(path string, pipelines []PipelineConfig) error {
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("config: parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// first override ever written for this repo
	default:
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	doc["pipelines"] = pipelines
	buf, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("config: encode %s: %w", path, err)
	}
	return statedir.WriteFileAtomic(path, buf)
}
