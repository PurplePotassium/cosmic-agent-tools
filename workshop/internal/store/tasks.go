package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

const taskCols = `id, backlog, type, title, detail, files, pin_agent, pin_model, pin_effort,
	position, status, origin, claimed_by, claim_pass, attempts, not_before, meta, created, updated`

const positionStep = 1024.0

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(r rowScanner) (*domain.Task, error) {
	var t domain.Task
	var files, meta string
	var notBefore, created, updated int64
	err := r.Scan(&t.ID, &t.Backlog, &t.Type, &t.Title, &t.Detail, &files,
		&t.Pin.Agent, &t.Pin.Model, &t.Pin.Effort,
		&t.Position, &t.Status, &t.Origin, &t.ClaimedBy, &t.ClaimPass,
		&t.Attempts, &notBefore, &meta, &created, &updated)
	if err != nil {
		return nil, err
	}
	if files != "" && files != "[]" {
		if err := json.Unmarshal([]byte(files), &t.Files); err != nil {
			return nil, fmt.Errorf("store: task %s files: %w", t.ID, err)
		}
	}
	if meta != "" && meta != "{}" {
		if err := json.Unmarshal([]byte(meta), &t.Meta); err != nil {
			return nil, fmt.Errorf("store: task %s meta: %w", t.ID, err)
		}
	}
	t.NotBefore = fromMillis(notBefore)
	t.Created = fromMillis(created)
	t.Updated = fromMillis(updated)
	return &t, nil
}

// AddTask inserts a task. Empty ID gets a fresh one; zero Created gets now.
// top=true places it before everything else in its backlog.
func (s *Store) AddTask(ctx context.Context, t *domain.Task, top bool) (*domain.Task, error) {
	if t.ID == "" {
		t.ID = NewID()
	}
	if t.Status == "" {
		t.Status = domain.TaskOpen
	}
	if t.Origin == "" {
		t.Origin = domain.OriginOperator
	}
	now := time.Now().UTC()
	if t.Created.IsZero() {
		t.Created = now
	}
	t.Updated = now

	edge := "MAX"
	if top {
		edge = "MIN"
	}
	var cur sql.NullFloat64
	if err := s.db.QueryRowContext(ctx,
		`SELECT `+edge+`(position) FROM tasks WHERE backlog = ? AND status IN ('open','claimed')`,
		t.Backlog).Scan(&cur); err != nil {
		return nil, err
	}
	if !cur.Valid {
		t.Position = positionStep
	} else if top {
		t.Position = cur.Float64 - positionStep
	} else {
		t.Position = cur.Float64 + positionStep
	}

	files, _ := json.Marshal(t.Files)
	meta, _ := json.Marshal(t.Meta)
	if t.Files == nil {
		files = []byte("[]")
	}
	if t.Meta == nil {
		meta = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Backlog, t.Type, t.Title, t.Detail, string(files),
		t.Pin.Agent, t.Pin.Model, t.Pin.Effort,
		t.Position, t.Status, t.Origin, t.ClaimedBy, t.ClaimPass,
		t.Attempts, toMillis(t.NotBefore), string(meta), toMillis(t.Created), toMillis(t.Updated))
	if err != nil {
		return nil, fmt.Errorf("store: add task: %w", err)
	}
	return t, nil
}

// GetTask fetches one task by id.
func (s *Store) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// TaskFilter narrows ListTasks. Zero value = all tasks.
type TaskFilter struct {
	Backlog  *string             // nil = any; pointer so "" (main) is expressible
	Statuses []domain.TaskStatus // nil = any
}

// ListTasks returns tasks ordered by backlog, then position.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]*domain.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks`
	var conds []string
	var args []any
	if f.Backlog != nil {
		conds = append(conds, "backlog = ?")
		args = append(args, *f.Backlog)
	}
	if len(f.Statuses) > 0 {
		ph := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ph[i] = "?"
			args = append(args, string(st))
		}
		conds = append(conds, "status IN ("+strings.Join(ph, ",")+")")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY backlog, position, created"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Claim atomically claims the next eligible task for a pipeline, two-tier:
// first the pipeline's own exclusive backlog (any type), then — if drainMain —
// the main backlog filtered by types (empty types = all). Returns
// (nil, nil) when nothing is eligible.
func (s *Store) Claim(ctx context.Context, pipeline string, drainMain bool, types []string, passID int64) (*domain.Task, error) {
	now := time.Now().UTC()

	claim := func(where string, args []any) (*domain.Task, error) {
		q := `UPDATE tasks SET status = 'claimed', claimed_by = ?, claim_pass = ?, updated = ?
			WHERE id = (SELECT id FROM tasks WHERE status = 'open' AND not_before <= ? AND ` + where + `
				ORDER BY position, created LIMIT 1)
			RETURNING ` + taskCols
		full := append([]any{pipeline, passID, toMillis(now), toMillis(now)}, args...)
		t, err := scanTask(s.db.QueryRowContext(ctx, q, full...))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return t, err
	}

	// Tier 1: own backlog, explicit assignment beats type filters.
	if t, err := claim("backlog = ?", []any{pipeline}); err != nil || t != nil {
		return t, err
	}
	if !drainMain {
		return nil, nil
	}
	// Tier 2: main backlog, honoring the pipeline's type filter.
	where := "backlog = ?"
	args := []any{domain.MainBacklog}
	if len(types) > 0 {
		ph := make([]string, len(types))
		for i, ty := range types {
			ph[i] = "?"
			args = append(args, ty)
		}
		where += " AND type IN (" + strings.Join(ph, ",") + ")"
	}
	return claim(where, args)
}

// CompleteTask marks a claimed task done and records a completion.
func (s *Store) CompleteTask(ctx context.Context, id, pipeline, result string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var title string
	err = tx.QueryRowContext(ctx,
		`UPDATE tasks SET status = 'done', updated = ? WHERE id = ? RETURNING title`,
		toMillis(now), id).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO completions (id, task_id, pipeline, title, result, completed) VALUES (?,?,?,?,?,?)`,
		NewID(), id, pipeline, title, result, toMillis(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// FailTask records a blocked/reverted/failed attempt: attempts++, backoff via
// notBefore, released back to open — or stuck once attempts reach maxAttempts.
func (s *Store) FailTask(ctx context.Context, id string, backoff time.Duration, maxAttempts int) (*domain.Task, error) {
	now := time.Now().UTC()
	t, err := scanTask(s.db.QueryRowContext(ctx, `
		UPDATE tasks SET
			attempts = attempts + 1,
			status = CASE WHEN attempts + 1 >= ? THEN 'stuck' ELSE 'open' END,
			claimed_by = '', claim_pass = 0,
			not_before = ?, updated = ?
		WHERE id = ?
		RETURNING `+taskCols, maxAttempts, toMillis(now.Add(backoff)), toMillis(now), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ReleaseTask returns a claimed task to open without penalty (pass never ran).
func (s *Store) ReleaseTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'open', claimed_by = '', claim_pass = 0, updated = ? WHERE id = ? AND status = 'claimed'`,
		toMillis(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateTaskFields patches title/detail/type/pin/meta of a task.
type TaskPatch struct {
	Title  *string
	Detail *string
	Type   *string
	Pin    *domain.Bundle
	Status *domain.TaskStatus
}

// UpdateTask applies a patch to a task.
func (s *Store) UpdateTask(ctx context.Context, id string, p TaskPatch) (*domain.Task, error) {
	var sets []string
	var args []any
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if p.Title != nil {
		add("title", *p.Title)
	}
	if p.Detail != nil {
		add("detail", *p.Detail)
	}
	if p.Type != nil {
		add("type", *p.Type)
	}
	if p.Pin != nil {
		add("pin_agent", p.Pin.Agent)
		add("pin_model", p.Pin.Model)
		add("pin_effort", p.Pin.Effort)
	}
	if p.Status != nil {
		add("status", string(*p.Status))
	}
	if len(sets) == 0 {
		return s.GetTask(ctx, id)
	}
	add("updated", toMillis(time.Now().UTC()))
	args = append(args, id)
	t, err := scanTask(s.db.QueryRowContext(ctx,
		`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ? RETURNING `+taskCols, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// MoveTask reassigns a task to another backlog, placing it last there.
func (s *Store) MoveTask(ctx context.Context, id, backlog string) (*domain.Task, error) {
	var cur sql.NullFloat64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(position) FROM tasks WHERE backlog = ? AND status IN ('open','claimed')`,
		backlog).Scan(&cur); err != nil {
		return nil, err
	}
	pos := positionStep
	if cur.Valid {
		pos = cur.Float64 + positionStep
	}
	t, err := scanTask(s.db.QueryRowContext(ctx,
		`UPDATE tasks SET backlog = ?, position = ?, updated = ? WHERE id = ? RETURNING `+taskCols,
		backlog, pos, toMillis(time.Now().UTC()), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ReorderBacklog rewrites the positions of one backlog to match ids' order.
// Tasks of that backlog not present in ids keep their relative order after
// the listed ones.
func (s *Store) ReorderBacklog(ctx context.Context, backlog string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := toMillis(time.Now().UTC())
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET position = ?, updated = ? WHERE id = ? AND backlog = ?`,
			float64(i+1)*positionStep, now, id, backlog); err != nil {
			return err
		}
	}
	// Push unlisted open tasks after the listed block, preserving order.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks
		WHERE backlog = ? AND status IN ('open','claimed') AND id NOT IN (`+placeholders(len(ids))+`)
		ORDER BY position, created`, append([]any{backlog}, toAny(ids)...)...)
	if err != nil {
		return err
	}
	var rest []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		rest = append(rest, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, id := range rest {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET position = ?, updated = ? WHERE id = ?`,
			float64(len(ids)+i+1)*positionStep, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteTask removes a task outright.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func placeholders(n int) string {
	if n == 0 {
		return "''" // impossible id — keeps the IN () clause valid
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
