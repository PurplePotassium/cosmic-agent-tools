package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// Goal reads .hal/GOAL.md ("" if absent).
func (a *App) Goal() string {
	data, err := os.ReadFile(config.GoalFile(a.RepoDir))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(string(data), "\uFEFF")
}

// SetGoal writes .hal/GOAL.md.
func (a *App) SetGoal(content string) error {
	path := config.GoalFile(a.RepoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return statedir.WriteFileAtomic(path, []byte(content))
}

var fragmentRe = regexp.MustCompile(`^(project|base|types/[a-z0-9_-]+|pipelines/[a-z0-9_-]+)$`)

// Fragment reads a prompt fragment by its logical name ("project",
// "types/art", ...).
func (a *App) Fragment(name string) (string, error) {
	if !fragmentRe.MatchString(name) {
		return "", fmt.Errorf("invalid fragment name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(config.PromptsDir(a.RepoDir), filepath.FromSlash(name)+".md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SetFragment writes a prompt fragment.
func (a *App) SetFragment(name, content string) error {
	if !fragmentRe.MatchString(name) {
		return fmt.Errorf("invalid fragment name %q", name)
	}
	path := filepath.Join(config.PromptsDir(a.RepoDir), filepath.FromSlash(name)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return statedir.WriteFileAtomic(path, []byte(content))
}

var imageDataURLRe = regexp.MustCompile(`^data:image/(png|jpe?g|gif|webp);base64,(.+)$`)

// attachmentNameRe strips everything but a conservative filename charset —
// the value comes straight from a browser File object, so treat it as
// untrusted path input.
var attachmentNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SaveAttachment decodes a pasted/attached image (a data: URL, as produced by
// FileReader.readAsDataURL) and writes it under <StateDir>/attachments with
// a random-prefixed name. It returns the absolute path so a task's detail
// can reference it directly — the next pass's Read tool can open an image
// by path, no separate image-serving route needed.
func (a *App) SaveAttachment(name, dataURL string) (string, error) {
	m := imageDataURLRe.FindStringSubmatch(dataURL)
	if m == nil {
		return "", fmt.Errorf("attachment must be a base64 image data URL (png/jpeg/gif/webp)")
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return "", fmt.Errorf("invalid base64 attachment data: %w", err)
	}
	dir := filepath.Join(a.StateDir, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := attachmentNameRe.ReplaceAllString(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), "-")
	if base == "" {
		base = "image"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return "", err
	}
	ext := strings.ToLower(m[1])
	if ext == "jpg" {
		ext = "jpeg"
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.%s", hex.EncodeToString(prefix), base, ext))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// attachmentRefRe matches the markdown image lines SaveAttachment's callers
// write into a task's detail, e.g. ![name](<StateDir>/attachments/xxx.png).
var attachmentRefRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// attachmentPaths extracts the attachment file paths referenced by detail
// that live under <StateDir>/attachments (SaveAttachment's directory);
// references to anything else are ignored.
func (a *App) attachmentPaths(detail string) map[string]bool {
	dir := filepath.Join(a.StateDir, "attachments")
	paths := make(map[string]bool)
	for _, m := range attachmentRefRe.FindAllStringSubmatch(detail, -1) {
		path := filepath.Clean(m[1])
		if filepath.Dir(path) == dir {
			paths[path] = true
		}
	}
	return paths
}

// DeleteTask removes a task and best-effort unlinks any attachment files its
// detail text references. An attachment's only owner is that markdown line —
// once the task is gone the file would otherwise sit under
// <StateDir>/attachments forever, so failures to remove it are logged and
// swallowed rather than failing the delete.
func (a *App) DeleteTask(ctx context.Context, id string) error {
	t, err := a.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteTask(ctx, id); err != nil {
		return err
	}
	for path := range a.attachmentPaths(t.Detail) {
		_ = os.Remove(path)
	}
	return nil
}

// UpdateTask patches a task and best-effort unlinks any attachment files that
// were referenced by its old detail but are no longer referenced by the new
// one — the same leak DeleteTask guards against, but triggered by an edit
// (e.g. a dashboard PATCH) that drops or replaces an image reference instead
// of removing the whole task.
func (a *App) UpdateTask(ctx context.Context, id string, p store.TaskPatch) (*domain.Task, error) {
	var before map[string]bool
	if p.Detail != nil {
		if old, err := a.Store.GetTask(ctx, id); err == nil {
			before = a.attachmentPaths(old.Detail)
		}
	}
	t, err := a.Store.UpdateTask(ctx, id, p)
	if err != nil {
		return nil, err
	}
	if p.Detail != nil {
		after := a.attachmentPaths(*p.Detail)
		for path := range before {
			if !after[path] {
				_ = os.Remove(path)
			}
		}
	}
	return t, nil
}

// ConfigView is the effective config plus provenance for the UI/CLI.
type ConfigView struct {
	Effective  config.Config     `json:"effective"`
	Provenance map[string]string `json:"provenance"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// ConfigSnapshot assembles the config view.
func (a *App) ConfigSnapshot() ConfigView {
	return ConfigView{
		Effective:  a.Res().Config,
		Provenance: a.Res().Provenance,
		Warnings:   a.Res().Warnings,
	}
}

// maxPassLogTail caps how much of a pass log PassLog returns. Agent passes can
// write multi-hundred-MB logs; slurping one whole on a single dashboard click
// would balloon the process. The dashboard only renders the tail anyway, so a
// large log is truncated to its last maxPassLogTail bytes.
const maxPassLogTail = 2 << 20 // 2 MiB

// PassLog returns the captured log of a pass (header-only for blind drivers),
// capped to the last maxPassLogTail bytes so a giant log can't blow up memory.
func (a *App) PassLog(ctx context.Context, id int64) (string, error) {
	p, err := a.Store.GetPass(ctx, id)
	if err != nil {
		return "", err
	}
	if p.LogPath == "" {
		return "", nil
	}
	f, err := os.Open(p.LogPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() <= maxPassLogTail {
		data, err := io.ReadAll(f)
		return string(data), err
	}
	if _, err := f.Seek(info.Size()-maxPassLogTail, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPassLogTail))
	if err != nil {
		return "", err
	}
	// Drop the leading partial line so the tail starts on a clean line
	// boundary — that also guarantees we never split a multi-byte UTF-8 rune.
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[i+1:]
	} else {
		// No newline in the whole tail (one giant line): the seek point can
		// still land mid-rune, so drop leading continuation bytes (≤3).
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
	}
	banner := fmt.Sprintf("… log truncated — showing the last %.1f MB of %.1f MB total …\n\n",
		float64(maxPassLogTail)/(1<<20), float64(info.Size())/(1<<20))
	return banner + string(data), nil
}

var _ = domain.MainBacklog
