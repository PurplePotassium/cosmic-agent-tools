package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
)

const passCols = `id, pipeline, n, task_id, spice, state, started, ended, exit_code, commit_sha, outcome, failure, log_path`

func scanPass(r rowScanner) (*domain.Pass, error) {
	var p domain.Pass
	var started, ended int64
	var exit sql.NullInt64
	err := r.Scan(&p.ID, &p.Pipeline, &p.N, &p.TaskID, &p.Spice, &p.State,
		&started, &ended, &exit, &p.CommitSHA, &p.Outcome, &p.Failure, &p.LogPath)
	if err != nil {
		return nil, err
	}
	p.Started = fromMillis(started)
	p.Ended = fromMillis(ended)
	if exit.Valid {
		code := int(exit.Int64)
		p.ExitCode = &code
	}
	return &p, nil
}

// StartPass bumps the pipeline's iteration counter and opens a pass record.
func (s *Store) StartPass(ctx context.Context, pipeline string) (*domain.Pass, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var n int
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO pipelines (name, iter_count, updated) VALUES (?, 1, ?)
		ON CONFLICT(name) DO UPDATE SET iter_count = iter_count + 1, updated = excluded.updated
		RETURNING iter_count`, pipeline, toMillis(now)).Scan(&n); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO passes (pipeline, n, state, started) VALUES (?,?,?,?)`,
		pipeline, n, string(domain.PassClaiming), toMillis(now))
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.Pass{ID: id, Pipeline: pipeline, N: n, State: domain.PassClaiming, Started: now}, nil
}

// PassPatch updates mutable pass fields; nil fields are left alone.
type PassPatch struct {
	TaskID    *string
	Spice     *string
	State     *domain.PassState
	ExitCode  *int
	CommitSHA *string
	Outcome   *domain.PassOutcome
	Failure   *domain.FailKind
	LogPath   *string
	Ended     *time.Time
}

// UpdatePass applies a patch to a pass row.
func (s *Store) UpdatePass(ctx context.Context, id int64, p PassPatch) error {
	sets, args := []string{}, []any{}
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if p.TaskID != nil {
		add("task_id", *p.TaskID)
	}
	if p.Spice != nil {
		add("spice", *p.Spice)
	}
	if p.State != nil {
		add("state", string(*p.State))
	}
	if p.ExitCode != nil {
		add("exit_code", *p.ExitCode)
	}
	if p.CommitSHA != nil {
		add("commit_sha", *p.CommitSHA)
	}
	if p.Outcome != nil {
		add("outcome", string(*p.Outcome))
	}
	if p.Failure != nil {
		add("failure", string(*p.Failure))
	}
	if p.LogPath != nil {
		add("log_path", *p.LogPath)
	}
	if p.Ended != nil {
		add("ended", toMillis(*p.Ended))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, `UPDATE passes SET `+joinComma(sets)+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecentPasses lists the newest passes, optionally for one pipeline.
func (s *Store) RecentPasses(ctx context.Context, pipeline string, limit int) ([]*domain.Pass, error) {
	q := `SELECT ` + passCols + ` FROM passes`
	var args []any
	if pipeline != "" {
		q += ` WHERE pipeline = ?`
		args = append(args, pipeline)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Pass
	for rows.Next() {
		p, err := scanPass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPass fetches one pass.
func (s *Store) GetPass(ctx context.Context, id int64) (*domain.Pass, error) {
	p, err := scanPass(s.db.QueryRowContext(ctx, `SELECT `+passCols+` FROM passes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SetHalted records (or clears, with "") a pipeline halt reason.
func (s *Store) SetHalted(ctx context.Context, pipeline, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pipelines (name, halted_reason, updated) VALUES (?,?,?)
		ON CONFLICT(name) DO UPDATE SET halted_reason = excluded.halted_reason, updated = excluded.updated`,
		pipeline, reason, toMillis(time.Now().UTC()))
	return err
}

// HaltedReason returns the halt reason for a pipeline ("" = not halted).
func (s *Store) HaltedReason(ctx context.Context, pipeline string) (string, error) {
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT halted_reason FROM pipelines WHERE name = ?`, pipeline).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return r, err
}

// GetIntegration returns the merge-queue state for a pipeline (zero value if
// none recorded yet).
func (s *Store) GetIntegration(ctx context.Context, pipeline string) (*domain.LaneIntegration, error) {
	var li domain.LaneIntegration
	var blocked, culprit int
	err := s.db.QueryRowContext(ctx, `SELECT pipeline, last_seen_tip, blocked, blocked_by,
		proven_culprit, conflict_task_id, conflict_attempts FROM integration WHERE pipeline = ?`, pipeline).
		Scan(&li.Pipeline, &li.LastSeenTip, &blocked, &li.BlockedBy, &culprit, &li.ConflictTaskID, &li.ConflictAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return &domain.LaneIntegration{Pipeline: pipeline}, nil
	}
	if err != nil {
		return nil, err
	}
	li.Blocked = blocked != 0
	li.ProvenCulprit = culprit != 0
	return &li, nil
}

// PutIntegration upserts the merge-queue state for a pipeline.
func (s *Store) PutIntegration(ctx context.Context, li *domain.LaneIntegration) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO integration (pipeline, last_seen_tip, blocked, blocked_by, proven_culprit, conflict_task_id, conflict_attempts)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(pipeline) DO UPDATE SET
			last_seen_tip = excluded.last_seen_tip,
			blocked = excluded.blocked,
			blocked_by = excluded.blocked_by,
			proven_culprit = excluded.proven_culprit,
			conflict_task_id = excluded.conflict_task_id,
			conflict_attempts = excluded.conflict_attempts`,
		li.Pipeline, li.LastSeenTip, boolInt(li.Blocked), li.BlockedBy,
		boolInt(li.ProvenCulprit), li.ConflictTaskID, li.ConflictAttempts)
	return err
}

// ListCompletions returns the newest completions first.
func (s *Store) ListCompletions(ctx context.Context, limit int) ([]*domain.Completion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, pipeline, title, result, completed FROM completions ORDER BY completed DESC, id DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Completion
	for rows.Next() {
		var c domain.Completion
		var done int64
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Pipeline, &c.Title, &c.Result, &done); err != nil {
			return nil, err
		}
		c.Completed = fromMillis(done)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// AppendEvent persists an event and returns its sequence number.
func (s *Store) AppendEvent(ctx context.Context, ev *domain.Event) (int64, error) {
	payload := "{}"
	if ev.Payload != nil {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return 0, err
		}
		payload = string(b)
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events (ts, type, pipeline, pass, payload) VALUES (?,?,?,?,?)`,
		toMillis(ev.TS), ev.Type, ev.Pipeline, ev.Pass, payload)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EventsSince returns up to limit events with seq > since, oldest first.
func (s *Store) EventsSince(ctx context.Context, since int64, limit int) ([]*domain.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, ts, type, pipeline, pass, payload FROM events WHERE seq > ? ORDER BY seq LIMIT ?`,
		since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Event
	for rows.Next() {
		var ev domain.Event
		var ts int64
		var payload string
		if err := rows.Scan(&ev.Seq, &ts, &ev.Type, &ev.Pipeline, &ev.Pass, &payload); err != nil {
			return nil, err
		}
		ev.TS = fromMillis(ts)
		if payload != "" && payload != "{}" {
			_ = json.Unmarshal([]byte(payload), &ev.Payload)
		}
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
