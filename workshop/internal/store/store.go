// Package store is the single-file SQLite persistence layer (pure-Go modernc driver
// so cross-compilation stays trivial). It holds the project registry plus each
// project's backlog, completions, progress self-report, and run/iteration history.
// The DB replaces today's JSON read-modify-write races: the UI edits the DB, the
// engine materializes the agent-facing JSON files from it at pass start and
// reconciles them back at pass end.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database. MaxOpenConns is pinned to 1 so every read/write
// serializes through a single connection — the simplest way to make a local
// single-user app immune to "database is locked" without sprinkling retries.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	// WAL + a generous busy timeout are belt-and-suspenders on top of MaxOpenConns(1).
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)",
		url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS projects (
  id        TEXT PRIMARY KEY,
  name      TEXT NOT NULL,
  repo_path TEXT NOT NULL UNIQUE,
  state_dir TEXT NOT NULL,
  branch    TEXT NOT NULL DEFAULT '',
  agent     TEXT NOT NULL DEFAULT 'claude',
  model     TEXT NOT NULL DEFAULT 'claude-sonnet-4-6',
  status    TEXT NOT NULL DEFAULT 'stopped',
  preview   TEXT NOT NULL DEFAULT '',
  created   TIMESTAMP NOT NULL,
  updated   TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS backlog (
  id         TEXT NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title      TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  position   REAL NOT NULL,
  created    TEXT NOT NULL,
  PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_backlog_pos ON backlog(project_id, position);

CREATE TABLE IF NOT EXISTS completions (
  id         TEXT NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title      TEXT NOT NULL,
  result     TEXT NOT NULL DEFAULT '',
  completed  TEXT NOT NULL,
  PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_completions_when ON completions(project_id, completed);

CREATE TABLE IF NOT EXISTS progress (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  phase      TEXT NOT NULL DEFAULT '',
  task       TEXT NOT NULL DEFAULT '',
  plan       TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL DEFAULT '',
  updated    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS runs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  started    TIMESTAMP NOT NULL,
  ended      TIMESTAMP,
  iterations INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'running'
);

CREATE TABLE IF NOT EXISTS iterations (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  run_id        INTEGER NOT NULL DEFAULT 0,
  num           INTEGER NOT NULL,
  agent         TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL DEFAULT '',
  spice         TEXT NOT NULL DEFAULT '',
  log_path      TEXT NOT NULL DEFAULT '',
  commit_sha    TEXT NOT NULL DEFAULT '',
  files_changed TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT '',
  started       TIMESTAMP NOT NULL,
  ended         TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_project ON iterations(project_id, id);
`

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

// now is a tiny indirection so tests could stub it if needed.
func now() time.Time { return time.Now().UTC() }
