package driver

// ClaudePath resolves the claude binary exactly as the claude driver would
// spawn it (HAL_CLAUDE_BIN override → PATH). For status surfaces
// (doctor, the dashboard's environment panel) that report where the agent
// lives without probing its capabilities.
func ClaudePath() (string, error) { return findClaude() }

// AgyPath resolves the agy binary exactly as the agy driver would spawn it
// (HAL_AGY_BIN → PATH → known install dirs). Same status-surface use as
// ClaudePath.
func AgyPath() (string, error) { return findAgy() }

// CodexPath resolves the Codex CLI exactly as the codex driver would spawn it
// (HAL_CODEX_BIN override → PATH). The ChatGPT/Codex desktop install exposes
// this executable, so status surfaces use it as the install signal.
func CodexPath() (string, error) { return findCodex() }
