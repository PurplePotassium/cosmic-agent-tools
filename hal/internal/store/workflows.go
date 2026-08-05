package store

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// Workflow persistence. One workflows row per human-gated unit of work, six
// wf_stages rows seeded at creation (the fixed stage ladder), one wf_turns
// row per agent process, and wf_messages as the durable chat history the
// dashboard replays.

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// NewWorkflowID mints the human-readable workflow id that doubles as the
// artifact directory name: YYYY-MM-DD-<kebab-title>-<4hex>. Sortable in `git
// log`, collision-proofed by the random suffix.
func NewWorkflowID(title string, now time.Time) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if slug == "" {
		slug = "workflow"
	}
	const maxSlug = 40
	if len(slug) > maxSlug {
		slug = strings.Trim(slug[:maxSlug], "-")
	}
	var b [2]byte
	_, _ = crand.Read(b[:])
	return fmt.Sprintf("%s-%s-%02x%02x", now.UTC().Format("2006-01-02"), slug, b[0], b[1])
}

// CreateWorkflow inserts the workflow and seeds all six stage rows (first
// stage active, rest pending) in one transaction.
func (s *Store) CreateWorkflow(ctx context.Context, wf domain.Workflow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := toMillis(wf.Created)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflows(id, title, brief, stage, status, error, base_sha,
			kind, auto_approve, bundle_agent, bundle_model, bundle_effort, stage_bundles, created, updated)
		VALUES(?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.Title, wf.Brief, string(wf.Stage), string(wf.Status), wf.Kind, wf.AutoApprove,
		wf.Bundle.Agent, wf.Bundle.Model, wf.Bundle.Effort, marshalStageBundles(wf.StageBundles),
		now, now); err != nil {
		return err
	}
	for _, stage := range domain.StageOrder {
		status := domain.StagePending
		started := int64(0)
		if stage == wf.Stage {
			status = domain.StageActive
			started = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wf_stages(workflow_id, stage, status, started)
			VALUES(?, ?, ?, ?)`,
			wf.ID, string(stage), status, started); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanWorkflow(row interface{ Scan(...any) error }) (domain.Workflow, error) {
	var wf domain.Workflow
	var stage, status, stageBundles string
	var created, updated, validated int64
	err := row.Scan(&wf.ID, &wf.Title, &wf.Brief, &stage, &status, &wf.Error,
		&wf.BaseSHA, &wf.Kind, &wf.AutoApprove, &validated, &wf.ValidatedBy,
		&wf.Bundle.Agent, &wf.Bundle.Model, &wf.Bundle.Effort, &stageBundles,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return wf, ErrNotFound
	}
	if err != nil {
		return wf, err
	}
	wf.Stage = domain.WorkflowStage(stage)
	wf.Status = domain.WorkflowStatus(status)
	wf.StageBundles = unmarshalStageBundles(stageBundles)
	wf.Validated = fromMillis(validated)
	wf.Created = fromMillis(created)
	wf.Updated = fromMillis(updated)
	return wf, nil
}

// marshalStageBundles serializes the per-stage override map for its TEXT
// column; empty/zero entries are dropped so "{}" stays the no-override shape.
func marshalStageBundles(m map[domain.WorkflowStage]domain.Bundle) string {
	clean := map[domain.WorkflowStage]domain.Bundle{}
	for stage, b := range m {
		if !b.IsZero() {
			clean[stage] = b
		}
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func unmarshalStageBundles(s string) map[domain.WorkflowStage]domain.Bundle {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[domain.WorkflowStage]domain.Bundle
	if err := json.Unmarshal([]byte(s), &m); err != nil || len(m) == 0 {
		return nil
	}
	return m
}

const workflowCols = `id, title, brief, stage, status, error, base_sha, kind, auto_approve,
	validated, validated_by, bundle_agent, bundle_model, bundle_effort, stage_bundles, created, updated`

// GetWorkflow reads one workflow.
func (s *Store) GetWorkflow(ctx context.Context, id string) (domain.Workflow, error) {
	return scanWorkflow(s.db.QueryRowContext(ctx,
		`SELECT `+workflowCols+` FROM workflows WHERE id = ?`, id))
}

// ListWorkflows returns workflows newest-first; all=false filters to
// non-terminal ones (the dashboard's default view).
func (s *Store) ListWorkflows(ctx context.Context, all bool) ([]domain.Workflow, error) {
	q := `SELECT ` + workflowCols + ` FROM workflows`
	if !all {
		q += ` WHERE status NOT IN ('completed', 'abandoned')`
	}
	q += ` ORDER BY created DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

// SetWorkflowStatus updates status (+ error detail; "" clears it).
func (s *Store) SetWorkflowStatus(ctx context.Context, id string, status domain.WorkflowStatus, errDetail string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET status = ?, error = ?, updated = ? WHERE id = ?`,
		string(status), errDetail, toMillis(time.Now()), id)
	return oneRow(res, err)
}

// AdvanceWorkflowStage moves the workflow to stage and flips the stage rows:
// the new stage becomes active (started stamped once).
func (s *Store) AdvanceWorkflowStage(ctx context.Context, id string, stage domain.WorkflowStage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := toMillis(time.Now())
	res, err := tx.ExecContext(ctx,
		`UPDATE workflows SET stage = ?, updated = ? WHERE id = ?`, string(stage), now, id)
	if err := oneRow(res, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wf_stages SET status = ?, started = CASE WHEN started = 0 THEN ? ELSE started END
		WHERE workflow_id = ? AND stage = ?`,
		domain.StageActive, now, id, string(stage)); err != nil {
		return err
	}
	return tx.Commit()
}

// SetWorkflowBaseSHA records trunk's tip at implement start.
func (s *Store) SetWorkflowBaseSHA(ctx context.Context, id, sha string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET base_sha = ?, updated = ? WHERE id = ?`, sha, toMillis(time.Now()), id)
	return oneRow(res, err)
}

// SetWorkflowBundle updates the per-workflow model override: the all-stages
// base bundle plus the per-stage override map.
func (s *Store) SetWorkflowBundle(ctx context.Context, id string, b domain.Bundle, stages map[domain.WorkflowStage]domain.Bundle) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET bundle_agent = ?, bundle_model = ?, bundle_effort = ?, stage_bundles = ?, updated = ? WHERE id = ?`,
		b.Agent, b.Model, b.Effort, marshalStageBundles(stages), toMillis(time.Now()), id)
	return oneRow(res, err)
}

// ---- validation runs ----

// ListValidationPending returns the task workflows a validation run must
// cover: completed, implement approved (not skipped — a skipped implement
// changed nothing), and not yet validated. Oldest first, so a run reads
// changelogs in landing order.
func (s *Store) ListValidationPending(ctx context.Context) ([]domain.Workflow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+workflowCols+` FROM workflows w
		WHERE w.kind = '' AND w.status = 'completed' AND w.validated = 0
		AND EXISTS (SELECT 1 FROM wf_stages s WHERE s.workflow_id = w.id
			AND s.stage = 'implement' AND s.status = 'approved')
		ORDER BY w.created, w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

// ActiveValidationRun returns the live validation run, if any (ErrNotFound
// otherwise) — at most one runs at a time.
func (s *Store) ActiveValidationRun(ctx context.Context) (domain.Workflow, error) {
	return scanWorkflow(s.db.QueryRowContext(ctx, `
		SELECT `+workflowCols+` FROM workflows
		WHERE kind = ? AND status NOT IN ('completed', 'abandoned')
		ORDER BY created DESC LIMIT 1`, domain.KindValidation))
}

// CountTurnRunning counts workflows with an agent turn in flight — the
// "no ongoing stages" input to the auto-validate trigger.
func (s *Store) CountTurnRunning(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflows WHERE status = ?`,
		string(domain.WorkflowTurnRunning)).Scan(&n)
	return n, err
}

// SetValidationTargets records which task workflows a validation run covers.
func (s *Store) SetValidationTargets(ctx context.Context, runID string, targetIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wf_validation_targets WHERE run_id = ?`, runID); err != nil {
		return err
	}
	for _, id := range targetIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wf_validation_targets(run_id, target_id) VALUES(?, ?)`, runID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ValidationTargets returns the target workflow ids a run covers, in the
// order they were recorded.
func (s *Store) ValidationTargets(ctx context.Context, runID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT target_id FROM wf_validation_targets WHERE run_id = ? ORDER BY target_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkValidated stamps a task workflow as covered by a validation run.
func (s *Store) MarkValidated(ctx context.Context, id, runID string) error {
	now := toMillis(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET validated = ?, validated_by = ?, updated = ? WHERE id = ?`,
		now, runID, now, id)
	return oneRow(res, err)
}

// WorkflowStages returns the six stage rows in ladder order.
func (s *Store) WorkflowStages(ctx context.Context, id string) ([]domain.WorkflowStageState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workflow_id, stage, status, session_id, artifact, decision_note,
			started, ended, decided
		FROM wf_stages WHERE workflow_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStage := map[domain.WorkflowStage]domain.WorkflowStageState{}
	for rows.Next() {
		var st domain.WorkflowStageState
		var stage string
		var started, ended, decided int64
		if err := rows.Scan(&st.WorkflowID, &stage, &st.Status, &st.SessionID,
			&st.Artifact, &st.DecisionNote, &started, &ended, &decided); err != nil {
			return nil, err
		}
		st.Stage = domain.WorkflowStage(stage)
		st.Started, st.Ended, st.Decided = fromMillis(started), fromMillis(ended), fromMillis(decided)
		byStage[st.Stage] = st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []domain.WorkflowStageState
	for _, stage := range domain.StageOrder {
		if st, ok := byStage[stage]; ok {
			out = append(out, st)
		}
	}
	return out, nil
}

// SetStageSession records the latest captured session id (the resume key).
func (s *Store) SetStageSession(ctx context.Context, id string, stage domain.WorkflowStage, sessionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wf_stages SET session_id = ? WHERE workflow_id = ? AND stage = ?`,
		sessionID, id, string(stage))
	return oneRow(res, err)
}

// SetStageArtifact records the stage's artifact path (repo-relative).
func (s *Store) SetStageArtifact(ctx context.Context, id string, stage domain.WorkflowStage, artifact string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wf_stages SET artifact = ? WHERE workflow_id = ? AND stage = ?`,
		artifact, id, string(stage))
	return oneRow(res, err)
}

// DecideStage settles a stage as approved or skipped.
func (s *Store) DecideStage(ctx context.Context, id string, stage domain.WorkflowStage, status, note string) error {
	if status != domain.StageApproved && status != domain.StageSkipped {
		return fmt.Errorf("store: invalid stage decision %q", status)
	}
	now := toMillis(time.Now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE wf_stages SET status = ?, decision_note = ?, decided = ?, ended = ?
		WHERE workflow_id = ? AND stage = ? AND status IN ('active', 'pending')`,
		status, note, now, now, id, string(stage))
	return oneRow(res, err)
}

// StartTurn opens a turn row (state running) with the next per-workflow N.
func (s *Store) StartTurn(ctx context.Context, t domain.WorkflowTurn) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO wf_turns(workflow_id, stage, n, session_id, resumed_from, state, log_path, started)
		VALUES(?, ?,
			(SELECT COALESCE(MAX(n), 0) + 1 FROM wf_turns WHERE workflow_id = ?),
			?, ?, ?, ?, ?)`,
		t.WorkflowID, string(t.Stage), t.WorkflowID,
		t.SessionID, t.ResumedFrom, domain.TurnStateRunning, t.LogPath, toMillis(t.Started))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetTurnLogPath records a turn's log path — known only after StartTurn
// mints the per-workflow N the filename derives from.
func (s *Store) SetTurnLogPath(ctx context.Context, id int64, logPath string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE wf_turns SET log_path = ? WHERE id = ?`, logPath, id)
	return oneRow(res, err)
}

// FinishTurn settles a turn row.
func (s *Store) FinishTurn(ctx context.Context, id int64, state string, exitCode int, errDetail, sessionID string, costUSD float64, numTurns int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE wf_turns SET state = ?, exit_code = ?, error = ?, session_id = ?,
			cost_usd = ?, num_turns = ?, ended = ?
		WHERE id = ?`,
		state, exitCode, errDetail, sessionID, costUSD, numTurns, toMillis(time.Now()), id)
	return oneRow(res, err)
}

// ListTurns returns a workflow's turns oldest-first.
func (s *Store) ListTurns(ctx context.Context, workflowID string) ([]domain.WorkflowTurn, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_id, stage, n, session_id, resumed_from, state,
			COALESCE(exit_code, 0), error, log_path, cost_usd, num_turns, started, ended
		FROM wf_turns WHERE workflow_id = ? ORDER BY id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WorkflowTurn
	for rows.Next() {
		var t domain.WorkflowTurn
		var stage string
		var started, ended int64
		if err := rows.Scan(&t.ID, &t.WorkflowID, &stage, &t.N, &t.SessionID,
			&t.ResumedFrom, &t.State, &t.ExitCode, &t.Error, &t.LogPath,
			&t.CostUSD, &t.NumTurns, &started, &ended); err != nil {
			return nil, err
		}
		t.Stage = domain.WorkflowStage(stage)
		t.Started, t.Ended = fromMillis(started), fromMillis(ended)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTurn reads one turn row.
func (s *Store) GetTurn(ctx context.Context, id int64) (domain.WorkflowTurn, error) {
	var t domain.WorkflowTurn
	var stage string
	var started, ended int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, stage, n, session_id, resumed_from, state,
			COALESCE(exit_code, 0), error, log_path, cost_usd, num_turns, started, ended
		FROM wf_turns WHERE id = ?`, id).Scan(
		&t.ID, &t.WorkflowID, &stage, &t.N, &t.SessionID, &t.ResumedFrom, &t.State,
		&t.ExitCode, &t.Error, &t.LogPath, &t.CostUSD, &t.NumTurns, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.Stage = domain.WorkflowStage(stage)
	t.Started, t.Ended = fromMillis(started), fromMillis(ended)
	return t, nil
}

// AppendMessage stores one chat-history row and returns its id.
func (s *Store) AppendMessage(ctx context.Context, m domain.WorkflowMessage) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO wf_messages(workflow_id, stage, turn_id, role, kind, content, created)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		m.WorkflowID, string(m.Stage), m.TurnID, m.Role, m.Kind, m.Content, toMillis(m.Created))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListMessages returns messages with id > afterID, oldest-first, capped at
// limit (0 = no cap). The dashboard replays with afterID=0 then follows SSE.
func (s *Store) ListMessages(ctx context.Context, workflowID string, afterID int64, limit int) ([]domain.WorkflowMessage, error) {
	q := `SELECT id, workflow_id, stage, turn_id, role, kind, content, created
		FROM wf_messages WHERE workflow_id = ? AND id > ? ORDER BY id`
	args := []any{workflowID, afterID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WorkflowMessage
	for rows.Next() {
		var m domain.WorkflowMessage
		var stage string
		var created int64
		if err := rows.Scan(&m.ID, &m.WorkflowID, &stage, &m.TurnID, &m.Role,
			&m.Kind, &m.Content, &created); err != nil {
			return nil, err
		}
		m.Stage = domain.WorkflowStage(stage)
		m.Created = fromMillis(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// CleanupOrphanTurns settles turns left "running" by a crashed engine:
// each becomes interrupted, and its workflow (if itself left turn-running)
// drops to awaiting-user so the operator's next message resumes it. Returns
// the affected workflow ids. Engine-startup only.
func (s *Store) CleanupOrphanTurns(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT workflow_id FROM wf_turns WHERE state = ?`, domain.TurnStateRunning)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	now := toMillis(time.Now())
	if _, err := tx.ExecContext(ctx, `
		UPDATE wf_turns SET state = ?, error = 'engine restarted mid-turn', ended = ?
		WHERE state = ?`, domain.TurnStateInterrupted, now, domain.TurnStateRunning); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workflows SET status = ?, updated = ? WHERE status = ?`,
		string(domain.WorkflowAwaitingUser), now, string(domain.WorkflowTurnRunning)); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// PruneWorkflows caps chat/turn history of TERMINAL workflows only: the
// newest keepTurns turns and keepMessages messages per finished workflow
// survive. Active workflows are never touched.
func (s *Store) PruneWorkflows(ctx context.Context, keepTurns, keepMessages int) error {
	if keepTurns > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM wf_turns WHERE id IN (
				SELECT t.id FROM wf_turns t
				JOIN workflows w ON w.id = t.workflow_id
				WHERE w.status IN ('completed', 'abandoned')
				AND t.id < (
					SELECT COALESCE(MIN(id), 0) FROM (
						SELECT id FROM wf_turns t2 WHERE t2.workflow_id = t.workflow_id
						ORDER BY id DESC LIMIT ?)))`, keepTurns); err != nil {
			return err
		}
	}
	if keepMessages > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM wf_messages WHERE id IN (
				SELECT m.id FROM wf_messages m
				JOIN workflows w ON w.id = m.workflow_id
				WHERE w.status IN ('completed', 'abandoned')
				AND m.id < (
					SELECT COALESCE(MIN(id), 0) FROM (
						SELECT id FROM wf_messages m2 WHERE m2.workflow_id = m.workflow_id
						ORDER BY id DESC LIMIT ?)))`, keepMessages); err != nil {
			return err
		}
	}
	return nil
}

// oneRow converts a no-op UPDATE into ErrNotFound.
func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
