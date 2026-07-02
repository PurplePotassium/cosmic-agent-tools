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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	dsn := "file:" + strings.ReplaceAll(path, "\\", "/") +
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
	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("store: migrate: %w\nstatement: %s", err, stmt)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO kv(k, v) VALUES('schema_version', ?) ON CONFLICT(k) DO NOTHING`,
		fmt.Sprint(schemaVersion))
	return err
}

// NewID mints a sortable task/completion id: "ws-" + lowercase ULID.
func NewID() string { return "ws-" + strings.ToLower(ulid.Make().String()) }

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
