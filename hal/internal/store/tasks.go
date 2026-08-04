package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

const taskCols = `id, backlog, type, title, detail, files, pin_agent, pin_model, pin_effort,
	position, status, origin, claimed_by, claim_pass, attempts, not_before, meta, created, updated`

const positionStep = 1024.0

type rowScanner interface{ Scan(dest ...any) error }

// dbtx is the slice of *sql.DB / *sql.Tx that the task queries need, so a
// statement can run directly or inside a transaction unchanged.
type dbtx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

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
	return insertTask(ctx, s.db, t, top)
}

func insertTask(ctx context.Context, q dbtx, t *domain.Task, top bool) (*domain.Task, error) {
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

	files, _ := json.Marshal(t.Files)
	meta, _ := json.Marshal(t.Meta)
	if t.Files == nil {
		files = []byte("[]")
	}
	if t.Meta == nil {
		meta = []byte("{}")
	}
	// The position is computed INSIDE the insert: a separate read-then-write
	// races the engine, dashboard, and CLI (all are writers), letting two
	// adds claim the same slot and silently breaking `--first` ordering.
	edge, sign := "MAX", "+"
	if top {
		edge, sign = "MIN", "-"
	}
	err := q.QueryRowContext(ctx, `INSERT INTO tasks (`+taskCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,
			(SELECT COALESCE(`+edge+`(position) `+sign+` ?, ?) FROM tasks
			 WHERE backlog = ? AND status IN ('open','claimed')),
			?,?,?,?,?,?,?,?,?)
		RETURNING position`,
		t.ID, t.Backlog, t.Type, t.Title, t.Detail, string(files),
		t.Pin.Agent, t.Pin.Model, t.Pin.Effort,
		positionStep, positionStep, t.Backlog,
		t.Status, t.Origin, t.ClaimedBy, t.ClaimPass,
		t.Attempts, toMillis(t.NotBefore), string(meta), toMillis(t.Created), toMillis(t.Updated)).
		Scan(&t.Position)
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
	return listTasks(ctx, s.db, f)
}

func listTasks(ctx context.Context, db dbtx, f TaskFilter) ([]*domain.Task, error) {
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
	rows, err := db.QueryContext(ctx, q, args...)
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

// CompleteTask marks a claimed task done and records a completion.
func (s *Store) CompleteTask(ctx context.Context, id, pipeline, result string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var title string
	// The status guard makes completion idempotent: a retried finalization
	// must not insert a second completion row for the same task.
	err = tx.QueryRowContext(ctx,
		`UPDATE tasks SET status = 'done', updated = ?
		 WHERE id = ? AND status IN ('open', 'claimed') RETURNING title`,
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

// MoveTask reassigns a task to another backlog, placing it last there. The
// new position is computed INSIDE the UPDATE (a scalar subquery, exactly the
// shape AddTask uses): a read-then-write races the engine, dashboard, and CLI
// and can compute a stale edge, silently breaking the operator's "move" intent.
// The subquery excludes this task itself so its current position never skews
// the max (e.g. a move-to-bottom within the same backlog).
func (s *Store) MoveTask(ctx context.Context, id, backlog string) (*domain.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx,
		`UPDATE tasks SET backlog = ?,
			position = (SELECT COALESCE(MAX(position) + ?, ?) FROM tasks
				WHERE backlog = ? AND status IN ('open','claimed') AND id != ?),
			updated = ?
		 WHERE id = ? RETURNING `+taskCols,
		backlog, positionStep, positionStep, backlog, id, toMillis(time.Now().UTC()), id))
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
			rows.Close() //nolint:sqlclosecheck // must close before the follow-up UPDATEs reuse the single conn — defer would deadlock
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
