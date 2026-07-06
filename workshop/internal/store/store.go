// Package store is the canonical persistence layer: one SQLite file in the
// project state dir. Everything else (agent-facing JSON, UI state) is a
// projection of this database.
//
// Concurrency model: all writers are goroutines of one process. The pool is
// capped at a single connection, so every statement is serialized — claims
// are atomic by construction. Traffic is tiny; simplicity wins.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	dsn := "file:" + encodeURIPath(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// encodeURIPath turns a filesystem path into the filename portion of a SQLite
// "file:" URI. Reserved URI characters that are legal in Windows/Unix paths
// (#, ?, %) would otherwise be misread — a '#' starts a fragment and a '?'
// starts the query string, truncating the filename — so each '/'-separated
// segment is percent-encoded (the separators and the drive colon are kept).
// SQLite percent-decodes the filename, so it recovers the original path.
func encodeURIPath(path string) string {
	segs := strings.Split(filepath.ToSlash(path), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const schemaVersion = 1

var schema = []string{
	`CREATE TABLE IF NOT EXISTS kv (
		k TEXT PRIMARY KEY,
		v TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS tasks (
		id          TEXT PRIMARY KEY,
		backlog     TEXT NOT NULL DEFAULT '',
		type        TEXT NOT NULL DEFAULT '',
		title       TEXT NOT NULL,
		detail      TEXT NOT NULL DEFAULT '',
		files       TEXT NOT NULL DEFAULT '[]',
		pin_agent   TEXT NOT NULL DEFAULT '',
		pin_model   TEXT NOT NULL DEFAULT '',
		pin_effort  TEXT NOT NULL DEFAULT '',
		position    REAL NOT NULL DEFAULT 0,
		status      TEXT NOT NULL DEFAULT 'open',
		origin      TEXT NOT NULL DEFAULT 'operator',
		claimed_by  TEXT NOT NULL DEFAULT '',
		claim_pass  INTEGER NOT NULL DEFAULT 0,
		attempts    INTEGER NOT NULL DEFAULT 0,
		not_before  INTEGER NOT NULL DEFAULT 0,
		meta        TEXT NOT NULL DEFAULT '{}',
		created     INTEGER NOT NULL,
		updated     INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_claim ON tasks(status, backlog, position, created)`,
	`CREATE TABLE IF NOT EXISTS passes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		pipeline   TEXT NOT NULL,
		n          INTEGER NOT NULL,
		task_id    TEXT NOT NULL DEFAULT '',
		spice      TEXT NOT NULL DEFAULT '',
		personality TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		state      TEXT NOT NULL,
		started    INTEGER NOT NULL,
		ended      INTEGER NOT NULL DEFAULT 0,
		exit_code  INTEGER,
		commit_sha TEXT NOT NULL DEFAULT '',
		outcome    TEXT NOT NULL DEFAULT '',
		failure    TEXT NOT NULL DEFAULT '',
		log_path   TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_passes_pipeline ON passes(pipeline, id)`,
	`CREATE TABLE IF NOT EXISTS pipelines (
		name          TEXT PRIMARY KEY,
		iter_count    INTEGER NOT NULL DEFAULT 0,
		halted_reason TEXT NOT NULL DEFAULT '',
		updated       INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS integration (
		pipeline          TEXT PRIMARY KEY,
		last_seen_tip     TEXT NOT NULL DEFAULT '',
		blocked           INTEGER NOT NULL DEFAULT 0,
		blocked_by        TEXT NOT NULL DEFAULT '',
		proven_culprit    INTEGER NOT NULL DEFAULT 0,
		conflict_task_id  TEXT NOT NULL DEFAULT '',
		conflict_attempts INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS completions (
		id        TEXT PRIMARY KEY,
		task_id   TEXT NOT NULL DEFAULT '',
		pipeline  TEXT NOT NULL DEFAULT '',
		title     TEXT NOT NULL,
		result    TEXT NOT NULL DEFAULT '',
		completed INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		seq      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts       INTEGER NOT NULL,
		type     TEXT NOT NULL,
		pipeline TEXT NOT NULL DEFAULT '',
		pass     INTEGER NOT NULL DEFAULT 0,
		payload  TEXT NOT NULL DEFAULT '{}'
	)`,
}

func (s *Store) migrate() error {
	// Refuse to touch a database written by a NEWER workshop: our schema
	// statements are additive-only, so writing into a future layout could
	// silently corrupt it. (Older versions are fine — migrating forward is
	// exactly what this function does.)
	var stored string
	err := s.db.QueryRow(`SELECT v FROM kv WHERE k = 'schema_version'`).Scan(&stored)
	if err == nil {
		if v, perr := strconv.Atoi(stored); perr == nil && v > schemaVersion {
			return fmt.Errorf("store: this database was created by a newer workshop (schema %d, this binary supports %d) — upgrade workshop", v, schemaVersion)
		}
	}
	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("store: migrate: %w\nstatement: %s", err, stmt)
		}
	}
	// Columns added after a table first shipped: CREATE TABLE IF NOT EXISTS
	// won't touch an existing table, so they're grafted on here. All are
	// defaulted, so an older binary keeps working against the widened table
	// (the schema stays additive — no version bump).
	if err := s.ensureColumn("passes", "session_id", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("passes", "personality", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// DO UPDATE, not DO NOTHING: the stored version must track the binary
	// that last migrated the schema forward, or the newer-workshop guard
	// above compares against the version that first CREATED the database
	// and can never fire.
	_, err = s.db.Exec(
		`INSERT INTO kv(k, v) VALUES('schema_version', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		fmt.Sprint(schemaVersion))
	return err
}

// ensureColumn adds a column to an existing table if it is missing.
func (s *Store) ensureColumn(table, column, ddl string) error {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return fmt.Errorf("store: inspect %s.%s: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, ddl)); err != nil {
		return fmt.Errorf("store: add column %s.%s: %w", table, column, err)
	}
	return nil
}

// Prune caps the unbounded-growth tables. Events beyond keepEvents and
// finished passes beyond keepPasses (per whole table, newest kept) are
// deleted. Run at engine startup, never from read-only CLI paths.
func (s *Store) Prune(ctx context.Context, keepEvents, keepPasses int) error {
	if keepEvents > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM events WHERE seq < (
				SELECT COALESCE(MIN(seq), 0) FROM (
					SELECT seq FROM events ORDER BY seq DESC LIMIT ?))`,
			keepEvents); err != nil {
			return err
		}
	}
	if keepPasses > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM passes WHERE ended != 0 AND id < (
				SELECT COALESCE(MIN(id), 0) FROM (
					SELECT id FROM passes ORDER BY id DESC LIMIT ?))`,
			keepPasses); err != nil {
			return err
		}
	}
	return nil
}

// NewID mints a sortable task/completion id: "ws-" + lowercase ULID.
func NewID() string { return "ws-" + strings.ToLower(ulid.Make().String()) }

// PipelineBundle returns the live agent/model/effort override for a pipeline
// (zero bundle when unset). This is the operator's "switch model for the
// NEXT pass" dial — stored, not config, so it takes effect without a restart
// and is re-read every pass.
func (s *Store) PipelineBundle(ctx context.Context, pipeline string) (domain.Bundle, error) {
	var b domain.Bundle
	v, err := s.GetKV(ctx, "bundle."+pipeline)
	if errors.Is(err, ErrNotFound) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal([]byte(v), &b); err != nil {
		return domain.Bundle{}, err
	}
	return b, nil
}

// SetPipelineBundle stores the live override; a zero bundle clears it.
func (s *Store) SetPipelineBundle(ctx context.Context, pipeline string, b domain.Bundle) error {
	key := "bundle." + pipeline
	if b.IsZero() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE k = ?`, key)
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.SetKV(ctx, key, string(data))
}

// PipelineMode returns the live goal/discover/drain mode override for a
// pipeline ("" when unset, meaning "use the configured mode"). Like
// PipelineBundle, this is operator dial, not config — it takes effect
// without a restart and is re-read every pass.
func (s *Store) PipelineMode(ctx context.Context, pipeline string) (string, error) {
	v, err := s.GetKV(ctx, "mode."+pipeline)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return v, err
}

// SetPipelineMode stores the live mode override; "" clears it.
func (s *Store) SetPipelineMode(ctx context.Context, pipeline, mode string) error {
	key := "mode." + pipeline
	if mode == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE k = ?`, key)
		return err
	}
	return s.SetKV(ctx, key, mode)
}

// PipelinePersonality returns the live personality-selector override for a
// pipeline ("" when unset, meaning "use the configured personality"). Like
// PipelineBundle/PipelineMode, this is an operator dial, not config — it
// takes effect without a restart and is re-read every pass.
func (s *Store) PipelinePersonality(ctx context.Context, pipeline string) (string, error) {
	v, err := s.GetKV(ctx, "personality."+pipeline)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return v, err
}

// SetPipelinePersonality stores the live personality override; "" clears it
// (back to the configured selector). An explicit "none" is a distinct,
// storable value — it forces no personality even when the pipeline is
// configured with one, whereas "" just falls back to that configuration.
func (s *Store) SetPipelinePersonality(ctx context.Context, pipeline, personality string) error {
	key := "personality." + pipeline
	if personality == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE k = ?`, key)
		return err
	}
	return s.SetKV(ctx, key, personality)
}

// GetKV reads a kv value; ErrNotFound when absent.
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetKV upserts a kv value.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, value)
	return err
}

func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
