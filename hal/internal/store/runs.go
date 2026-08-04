package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

const passCols = `id, pipeline, n, task_id, spice, personality, session_id, state, started, ended, exit_code, commit_sha, outcome, failure, log_path`

func scanPass(r rowScanner) (*domain.Pass, error) {
	var p domain.Pass
	var started, ended int64
	var exit sql.NullInt64
	err := r.Scan(&p.ID, &p.Pipeline, &p.N, &p.TaskID, &p.Spice, &p.Personality, &p.SessionID, &p.State,
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
	TaskID      *string
	Spice       *string
	Personality *string
	SessionID   *string
	State       *domain.PassState
	ExitCode    *int
	CommitSHA   *string
	Outcome     *domain.PassOutcome
	Failure     *domain.FailKind
	LogPath     *string
	Ended       *time.Time
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
	if p.Personality != nil {
		add("personality", *p.Personality)
	}
	if p.SessionID != nil {
		add("session_id", *p.SessionID)
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

// CleanupOrphanPasses closes pass rows left "running" by a crashed process.
// Call once at startup — within a live process the engine always closes its
// own passes.
func (s *Store) CleanupOrphanPasses(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE passes SET state = 'failed', outcome = 'failed', failure = 'setup', ended = ?
		WHERE ended = 0`, toMillis(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunningPass returns the open pass for a pipeline, if any.
func (s *Store) RunningPass(ctx context.Context, pipeline string) (*domain.Pass, error) {
	p, err := scanPass(s.db.QueryRowContext(ctx,
		`SELECT `+passCols+` FROM passes WHERE pipeline = ? AND ended = 0 ORDER BY id DESC LIMIT 1`, pipeline))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// AddCompletion records a completion that has no backing task row (e.g. an
// invented/freeform pass).
func (s *Store) AddCompletion(ctx context.Context, c *domain.Completion) error {
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.Completed.IsZero() {
		c.Completed = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO completions (id, task_id, pipeline, title, result, completed) VALUES (?,?,?,?,?,?)`,
		c.ID, c.TaskID, c.Pipeline, c.Title, c.Result, toMillis(c.Completed))
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
