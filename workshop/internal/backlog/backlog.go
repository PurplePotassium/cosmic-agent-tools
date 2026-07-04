// Package backlog is the task-queue service over the store: claiming for
// pipelines, snapshotting the whole task space, and ingesting agent
// proposals with dedupe across every backlog.
package backlog

import (
	"context"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
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
	// The snapshot and the inserts run in ONE transaction: the store shares a
	// single connection, so an open transaction serializes concurrent ingests.
	// Without it, two workers finishing passes at once could both snapshot the
	// backlog before either inserts and admit the same-titled proposal twice.
	var added []*domain.Task
	err := s.st.WithTx(ctx, func(ctx context.Context, tx *store.TaskTx) error {
		existing, err := tx.ListTasks(ctx, store.TaskFilter{
			Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed},
		})
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, t := range existing {
			seen[normTitle(t.Title)] = true
		}
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
			task, err := tx.AddTask(ctx, &domain.Task{
				Backlog: backlog,
				Type:    p.Type,
				Title:   strings.TrimSpace(p.Title),
				Detail:  p.Detail,
				Files:   p.Files,
				Origin:  domain.OriginAgent,
				Meta:    map[string]string{"proposedBy": from},
			}, false)
			if err != nil {
				return err
			}
			seen[key] = true
			added = append(added, task)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

// statedirSharedLabel mirrors statedir.SharedLabel without the import cycle
// risk; agents may echo it back in proposals.
const statedirSharedLabel = "shared"

func normTitle(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(title)), " ")
}
