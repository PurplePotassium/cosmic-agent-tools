// Package backlog is the task-queue service over the store: claiming for
// pipelines, snapshotting the whole task space, and ingesting agent
// proposals with dedupe across every backlog.
package backlog

import (
	"context"
	"strings"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

// Service wraps the store with backlog semantics.
type Service struct {
	st *store.Store
}

// New builds the service.
func New(st *store.Store) *Service { return &Service{st: st} }

// Claim atomically claims the next task for a pipeline (own backlog first,
// then — if the pipeline drains main — the main backlog by type filter).
// Returns nil when nothing is eligible.
func (s *Service) Claim(ctx context.Context, p domain.Pipeline, passID int64) (*domain.Task, error) {
	return s.st.Claim(ctx, p.Name, p.DrainMain, p.TaskTypes, passID)
}

// Snapshot returns every open and claimed task across all backlogs, ordered.
func (s *Service) Snapshot(ctx context.Context) ([]*domain.Task, error) {
	return s.st.ListTasks(ctx, store.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed},
	})
}

// Ingest turns agent proposals into tasks: titles are deduped (normalized)
// against every open/claimed task in every backlog AND against each other; a
// proposal may target the shared backlog (default) or a known pipeline's
// backlog. At most maxAccept proposals are accepted. Returns the added tasks.
func (s *Service) Ingest(ctx context.Context, from string, proposals []domain.Proposal, knownPipelines map[string]bool, maxAccept int) ([]*domain.Task, error) {
	if len(proposals) == 0 {
		return nil, nil
	}
	existing, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range existing {
		seen[normTitle(t.Title)] = true
	}

	var added []*domain.Task
	for _, p := range proposals {
		if len(added) >= maxAccept {
			break
		}
		key := normTitle(p.Title)
		if key == "" || seen[key] {
			continue
		}
		backlog := domain.MainBacklog
		if p.Backlog != "" && p.Backlog != statedirSharedLabel {
			if knownPipelines[p.Backlog] {
				backlog = p.Backlog
			}
			// Unknown pipeline names fall back to the shared backlog
			// rather than dropping the idea.
		}
		task, err := s.st.AddTask(ctx, &domain.Task{
			Backlog: backlog,
			Type:    p.Type,
			Title:   strings.TrimSpace(p.Title),
			Detail:  p.Detail,
			Files:   p.Files,
			Origin:  domain.OriginAgent,
			Meta:    map[string]string{"proposedBy": from},
		}, false)
		if err != nil {
			return added, err
		}
		seen[key] = true
		added = append(added, task)
	}
	return added, nil
}

// statedirSharedLabel mirrors statedir.SharedLabel without the import cycle
// risk; agents may echo it back in proposals.
const statedirSharedLabel = "shared"

func normTitle(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(title)), " ")
}
